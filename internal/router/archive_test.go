package router_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/store"
	nativekvstore "github.com/HW-Yue/Memora/internal/store/nativekv"
)

func archiveFixture(t *testing.T) (context.Context, *router.Service, store.Tx, router.Node, router.Node, router.Locator) {
	t.Helper()
	ctx := context.Background()
	databaseStore, err := nativekvstore.Open(filepath.Join(t.TempDir(), "database.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = databaseStore.Close() })
	service := router.New(databaseStore, router.Options{
		IDs: &idSource{values: []string{"root", "branch", "leaf", "sibling", "second"}},
	})
	tx := mustBegin(t, ctx, databaseStore)
	root, err := service.CreateRootIn(ctx, tx, "db_work", "Work")
	if err != nil {
		t.Fatal(err)
	}
	branch := mustCreateNode(t, ctx, service, tx, root.ID, "branch", router.KindBranch)
	leaf := mustCreateNode(t, ctx, service, tx, branch.ID, "leaf", router.KindLeaf)
	locator := router.Locator{DatabaseID: "db_work", TableID: "tbl_notes", RowID: "row_01", Revision: 1}
	if _, err := service.ReplaceMembershipsIn(ctx, tx, locator, []string{leaf.ID}); err != nil {
		t.Fatal(err)
	}
	return ctx, service, tx, branch, leaf, locator
}

func TestArchivingASubtreeIsLosslessAndRestorable(t *testing.T) {
	t.Parallel()

	ctx, service, tx, branch, leaf, locator := archiveFixture(t)
	if err := service.DeleteNodeIn(ctx, tx, branch.ID, branch.Revision); err != nil {
		t.Fatal(err)
	}

	if _, _, err := service.ListChildrenIn(ctx, tx, branch.ID, "", 10); err == nil {
		t.Fatal("expected listing children of an archived branch to fail")
	}
	if _, err := service.ListLeafIn(ctx, tx, leaf.ID, 10); err == nil {
		t.Fatal("expected opening an archived leaf to fail")
	}
	found, err := service.MembershipsForRowIn(ctx, tx, locator.DatabaseID, locator.TableID, locator.RowID)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 0 {
		t.Fatalf("memberships under an archived leaf must be hidden, got %#v", found)
	}

	// The archive swept the branch and its leaf together, so restoring the
	// branch brings both back with their memberships intact.
	if err := service.RestoreNodeIn(ctx, tx, branch.ID, branch.Revision+1); err != nil {
		t.Fatal(err)
	}
	locators, err := service.ListLeafIn(ctx, tx, leaf.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(locators) != 1 || locators[0].RowID != "row_01" {
		t.Fatalf("restore must bring memberships back, got %#v", locators)
	}
	found, err = service.MembershipsForRowIn(ctx, tx, locator.DatabaseID, locator.TableID, locator.RowID)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].LeafID != leaf.ID {
		t.Fatalf("restore must bring reverse memberships back, got %#v", found)
	}
}

func TestArchivedSiblingsDoNotConsumeFanoutBudget(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, err := nativekvstore.Open(filepath.Join(t.TempDir(), "database.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = databaseStore.Close() })
	service := router.New(databaseStore, router.Options{
		IDs:         &idSource{values: []string{"root", "first", "second"}},
		MaxChildren: 1,
	})
	tx := mustBegin(t, ctx, databaseStore)
	root, err := service.CreateRootIn(ctx, tx, "db_work", "Work")
	if err != nil {
		t.Fatal(err)
	}
	first := mustCreateNode(t, ctx, service, tx, root.ID, "first", router.KindLeaf)
	if _, err := service.CreateNodeIn(ctx, tx, root.ID, router.NodeDefinition{
		Name: "second", Kind: router.KindLeaf, Purpose: "second scope",
	}); err == nil {
		t.Fatal("expected the fan-out limit to be enforced")
	}
	if err := service.DeleteNodeIn(ctx, tx, first.ID, first.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateNodeIn(ctx, tx, root.ID, router.NodeDefinition{
		Name: "second", Kind: router.KindLeaf, Purpose: "second scope",
	}); err != nil {
		t.Fatalf("an archived sibling must free its fan-out slot: %v", err)
	}
}

func TestArchivedParentRefusesNewChildrenAndRestore(t *testing.T) {
	t.Parallel()

	ctx, service, tx, branch, leaf, _ := archiveFixture(t)
	if err := service.DeleteNodeIn(ctx, tx, branch.ID, branch.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateNodeIn(ctx, tx, branch.ID, router.NodeDefinition{
		Name: "another", Kind: router.KindLeaf, Purpose: "another scope",
	}); err == nil {
		t.Fatal("expected creating under an archived parent to fail")
	}
	if err := service.RestoreNodeIn(ctx, tx, leaf.ID, leaf.Revision+1); err == nil {
		t.Fatal("expected restoring under an archived parent to fail")
	}
	if err := service.RestoreNodeIn(ctx, tx, branch.ID, branch.Revision); err == nil {
		t.Fatal("expected a stale revision to be refused")
	}
}
