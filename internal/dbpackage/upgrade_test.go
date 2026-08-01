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

func TestPackageUpgradeRequiresReviewedCurrentBoundPlanAndReplacesAtomically(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	source := openStore(t, "upgrade-source.db")
	defer source.Close()
	seedSource(t, ctx, source)
	packages := dbpackage.New(source)
	first, _, err := packages.Pack(ctx, "work", "Alice")
	if err != nil {
		t.Fatal(err)
	}
	target := openStore(t, "upgrade-target.db")
	defer target.Close()
	installed := dbpackage.New(target)
	if _, err := installed.Install(installContext(ctx, first), first, dbpackage.InstallOptions{Trusted: true}); err != nil {
		t.Fatal(err)
	}

	rows := row.New(source, catalog.New(source, catalog.Options{}), row.Options{})
	if _, err := rows.Update(ctx, "work", "notes", "row_note", map[string]any{"body": "package upgrade"}, row.WriteOptions{
		ExpectedSchemaVersion: 1, ExpectedRevision: 2,
	}); err != nil {
		t.Fatal(err)
	}
	second, _, err := packages.Pack(ctx, "work", "Alice")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := installed.PlanUpgrade(ctx, second)
	if err != nil || plan.Status != "review_required" || plan.CurrentPackageSHA256 != dbpackage.Hash(first) ||
		plan.IncomingPackageSHA256 != dbpackage.Hash(second) {
		t.Fatalf("PlanUpgrade() = %#v, %v", plan, err)
	}
	before, err := row.New(target, catalog.New(target, catalog.Options{}), row.Options{}).Get(ctx, "work", "notes", "row_note")
	if err != nil || before.Revision != 2 {
		t.Fatalf("read-only plan changed target = %#v, %v", before, err)
	}
	if _, err := installed.ApplyUpgrade(ctx, second, plan); err == nil {
		t.Fatal("ApplyUpgrade without approval succeeded")
	}
	approved := security.WithAuthorization(ctx, security.Authorization{
		Version: security.AuthorizationVersion, Actor: "user:test", DefaultLevel: security.LevelStructural,
		AuthorizedDatabases: []string{"db_work"}, Approval: &security.Approval{
			Version: security.ApprovalVersion, Action: security.ActionApplyPackageUpgrade,
			SubjectSHA256: strings.TrimPrefix(plan.Hash, "sha256:"), Confirmed: true,
		},
	})
	receipt, err := installed.ApplyUpgrade(approved, second, plan)
	if err != nil || receipt.Status != "committed" || !receipt.ReadOnly {
		t.Fatalf("ApplyUpgrade() = %#v, %v", receipt, err)
	}
	after, err := row.New(target, catalog.New(target, catalog.Options{}), row.Options{}).Get(ctx, "work", "notes", "row_note")
	if err != nil || after.Revision != 3 || after.Values["body"] != "package upgrade" {
		t.Fatalf("upgraded Row = %#v, %v", after, err)
	}
	if _, err := installed.ApplyUpgrade(approved, second, plan); err == nil {
		t.Fatal("stale package upgrade replay succeeded")
	}
}
