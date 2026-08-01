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

func TestInstalledPackageForkCreatesIndependentWritableDatabase(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	source := openStore(t, "fork-source.db")
	defer source.Close()
	seedSource(t, ctx, source)
	encoded, _, err := dbpackage.New(source).Pack(ctx, "work", "Alice")
	if err != nil {
		t.Fatal(err)
	}
	target := openStore(t, "fork-target.db")
	defer target.Close()
	packages := dbpackage.New(target)
	if _, err := packages.Install(installContext(ctx, encoded), encoded, dbpackage.InstallOptions{Trusted: true}); err != nil {
		t.Fatal(err)
	}
	plan, err := packages.PlanFork(ctx, "work", "db_work_local", "work-local")
	if err != nil || plan.Status != "review_required" {
		t.Fatalf("PlanFork() = %#v, %v", plan, err)
	}
	approved := security.WithAuthorization(ctx, security.Authorization{
		Version: security.AuthorizationVersion, Actor: "user:test", DefaultLevel: security.LevelStructural,
		AuthorizedDatabases: []string{"db_work", "db_work_local"}, Approval: &security.Approval{
			Version: security.ApprovalVersion, Action: security.ActionForkPackageDatabase,
			SubjectSHA256: strings.TrimPrefix(plan.Hash, "sha256:"), Confirmed: true,
		},
	})
	receipt, err := packages.ApplyFork(approved, plan)
	if err != nil || receipt.Status != "committed" || receipt.ReadOnly {
		t.Fatalf("ApplyFork() = %#v, %v", receipt, err)
	}
	dictionary := catalog.New(target, catalog.Options{})
	forked, err := dictionary.DescribeDatabase(ctx, "work-local")
	if err != nil || forked.ReadOnly || forked.ForkedFromDatabaseID != "db_work" ||
		forked.ForkedFromSnapshotSHA256 == "" || forked.Tables[0].ID == "tbl_notes" {
		t.Fatalf("forked Database = %#v, %v", forked, err)
	}
	rows := row.New(target, dictionary, row.Options{})
	current, err := rows.Get(ctx, "work-local", "notes", "row_note")
	if err != nil || current.Values["body"] != "portable knowledge revised" {
		t.Fatalf("forked Row = %#v, %v", current, err)
	}
	updated, err := rows.Update(ctx, "work-local", "notes", "row_note", map[string]any{"body": "local fork"}, row.WriteOptions{
		ExpectedSchemaVersion: 1, ExpectedRevision: 2,
	})
	if err != nil || updated.Revision != 3 {
		t.Fatalf("fork write = %#v, %v", updated, err)
	}
	original, err := rows.Get(ctx, "work", "notes", "row_note")
	if err != nil || original.Revision != 2 || original.Values["body"] != "portable knowledge revised" {
		t.Fatalf("source changed by fork = %#v, %v", original, err)
	}
}
