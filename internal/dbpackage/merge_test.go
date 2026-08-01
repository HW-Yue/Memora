package dbpackage_test

import (
	"context"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/dbpackage"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/security"
)

func TestPackageMergeKeepsLocalChangesAndAddsNonConflictingUpstreamRows(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	source := openStore(t, "merge-source.db")
	defer source.Close()
	seedSource(t, ctx, source)
	basePackage, _, err := dbpackage.New(source).Pack(ctx, "work", "Alice")
	if err != nil {
		t.Fatal(err)
	}
	target := openStore(t, "merge-target.db")
	defer target.Close()
	packages := dbpackage.New(target)
	if _, err := packages.Install(installContext(ctx, basePackage), basePackage, dbpackage.InstallOptions{Trusted: true}); err != nil {
		t.Fatal(err)
	}
	forkPlan, err := packages.PlanFork(ctx, "work", "db_work_local", "work-local")
	if err != nil {
		t.Fatal(err)
	}
	forkCtx := security.WithAuthorization(ctx, security.Authorization{
		Version: security.AuthorizationVersion, Actor: "user:test", DefaultLevel: security.LevelStructural,
		AuthorizedDatabases: []string{"db_work", "db_work_local"}, Approval: &security.Approval{
			Version: security.ApprovalVersion, Action: security.ActionForkPackageDatabase,
			SubjectSHA256: strings.TrimPrefix(forkPlan.Hash, "sha256:"), Confirmed: true},
	})
	if _, err := packages.ApplyFork(forkCtx, forkPlan); err != nil {
		t.Fatal(err)
	}
	targetRows := row.New(target, catalog.New(target, catalog.Options{}), row.Options{})
	if _, err := targetRows.Update(ctx, "work-local", "notes", "row_note", map[string]any{"body": "local-only"}, row.WriteOptions{ExpectedSchemaVersion: 1, ExpectedRevision: 2}); err != nil {
		t.Fatal(err)
	}
	sourceRows := row.New(source, catalog.New(source, catalog.Options{}), row.Options{})
	upstream, err := sourceRows.Insert(ctx, "work", "notes", map[string]any{"body": "upstream-only"}, row.WriteOptions{ExpectedSchemaVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	incomingPackage, _, err := dbpackage.New(source).Pack(ctx, "work", "Alice")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := packages.PlanMerge(ctx, "work-local", basePackage, incomingPackage)
	if err != nil || plan.Status != "ready" || len(plan.Conflicts) != 0 || plan.MergedSnapshotSHA256 == "" {
		t.Fatalf("PlanMerge() = %#v, %v", plan, err)
	}
	mergeCtx := security.WithAuthorization(ctx, security.Authorization{
		Version: security.AuthorizationVersion, Actor: "user:test", DefaultLevel: security.LevelStructural,
		AuthorizedDatabases: []string{"db_work_local"}, Approval: &security.Approval{
			Version: security.ApprovalVersion, Action: security.ActionMergePackageFork,
			SubjectSHA256: strings.TrimPrefix(plan.Hash, "sha256:"), Confirmed: true},
	})
	receipt, err := packages.ApplyMerge(mergeCtx, basePackage, incomingPackage, plan)
	if err != nil || receipt.Status != "committed" || receipt.ReadOnly {
		t.Fatalf("ApplyMerge() = %#v, %v", receipt, err)
	}
	local, err := targetRows.Get(ctx, "work-local", "notes", "row_note")
	if err != nil || local.Values["body"] != "local-only" {
		t.Fatalf("local Row = %#v, %v", local, err)
	}
	added, err := targetRows.Get(ctx, "work-local", "notes", upstream.ID)
	if err != nil || added.Values["body"] != "upstream-only" {
		t.Fatalf("upstream Row = %#v, %v", added, err)
	}
}

func TestPackageMergeReportsDivergentRowWithoutWriting(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	source := openStore(t, "merge-conflict-source.db")
	defer source.Close()
	seedSource(t, ctx, source)
	basePackage, _, err := dbpackage.New(source).Pack(ctx, "work", "Alice")
	if err != nil {
		t.Fatal(err)
	}
	target := openStore(t, "merge-conflict-target.db")
	defer target.Close()
	packages := dbpackage.New(target)
	if _, err := packages.Install(installContext(ctx, basePackage), basePackage, dbpackage.InstallOptions{Trusted: true}); err != nil {
		t.Fatal(err)
	}
	forkPlan, err := packages.PlanFork(ctx, "work", "db_conflict_fork", "conflict-fork")
	if err != nil {
		t.Fatal(err)
	}
	forkCtx := security.WithAuthorization(ctx, security.Authorization{
		Version: security.AuthorizationVersion, Actor: "user:test", DefaultLevel: security.LevelStructural,
		AuthorizedDatabases: []string{"db_work", "db_conflict_fork"}, Approval: &security.Approval{
			Version: security.ApprovalVersion, Action: security.ActionForkPackageDatabase,
			SubjectSHA256: strings.TrimPrefix(forkPlan.Hash, "sha256:"), Confirmed: true},
	})
	if _, err := packages.ApplyFork(forkCtx, forkPlan); err != nil {
		t.Fatal(err)
	}
	targetRows := row.New(target, catalog.New(target, catalog.Options{}), row.Options{})
	if _, err := targetRows.Update(ctx, "conflict-fork", "notes", "row_note", map[string]any{"body": "local"}, row.WriteOptions{ExpectedSchemaVersion: 1, ExpectedRevision: 2}); err != nil {
		t.Fatal(err)
	}
	sourceRows := row.New(source, catalog.New(source, catalog.Options{}), row.Options{})
	if _, err := sourceRows.Update(ctx, "work", "notes", "row_note", map[string]any{"body": "upstream"}, row.WriteOptions{ExpectedSchemaVersion: 1, ExpectedRevision: 2}); err != nil {
		t.Fatal(err)
	}
	incoming, _, err := dbpackage.New(source).Pack(ctx, "work", "Alice")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := packages.PlanMerge(ctx, "conflict-fork", basePackage, incoming)
	if err != nil || plan.Status != "conflict" || len(plan.Conflicts) != 1 || !strings.HasPrefix(plan.Conflicts[0], "ROW:") {
		t.Fatalf("conflict PlanMerge() = %#v, %v", plan, err)
	}
	if _, err := packages.ApplyMerge(ctx, basePackage, incoming, plan); err == nil {
		t.Fatal("conflicting merge plan applied")
	}
	current, err := targetRows.Get(ctx, "conflict-fork", "notes", "row_note")
	if err != nil || current.Values["body"] != "local" {
		t.Fatalf("conflict changed fork = %#v, %v", current, err)
	}
}
