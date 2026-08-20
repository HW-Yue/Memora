package router_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/store"
	nativekvstore "github.com/HW-Yue/Memora/internal/store/nativekv"
)

func deleteFixture(t *testing.T) (context.Context, *router.Service, store.Tx, router.Node, router.Node, router.Locator) {
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

func TestDeletingASubtreeLeavesMembershipRecordsIntact(t *testing.T) {
	t.Parallel()

	ctx, service, tx, branch, leaf, locator := deleteFixture(t)
	if err := service.DeleteNodeIn(ctx, tx, branch.ID, branch.Revision); err != nil {
		t.Fatal(err)
	}

	if _, _, err := service.ListChildrenIn(ctx, tx, branch.ID, "", 10); err == nil {
		t.Fatal("expected listing children of a deleted branch to fail")
	}
	if _, err := service.ListLeafIn(ctx, tx, leaf.ID, 10); err == nil {
		t.Fatal("expected opening a deleted leaf to fail")
	}
	found, err := service.MembershipsForRowIn(ctx, tx, locator.DatabaseID, locator.TableID, locator.RowID)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 0 {
		t.Fatalf("memberships under a deleted leaf must be hidden, got %#v", found)
	}

	// Deleting is lossless in the structural sense — the membership records are
	// untouched on disk — but it is final: nothing brings the node back.
	all, err := service.ListLeafIncludingDeletedIn(ctx, tx, leaf.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].RowID != "row_01" {
		t.Fatalf("deleting a leaf must not corrupt its membership records, got %#v", all)
	}
}

func TestDeletedSiblingsDoNotConsumeFanoutBudget(t *testing.T) {
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
		t.Fatalf("a deleted sibling must free its fan-out slot: %v", err)
	}
}

func TestDeletedParentRefusesNewChildren(t *testing.T) {
	t.Parallel()

	ctx, service, tx, branch, leaf, _ := deleteFixture(t)
	if err := service.DeleteNodeIn(ctx, tx, branch.ID, branch.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateNodeIn(ctx, tx, branch.ID, router.NodeDefinition{
		Name: "another", Kind: router.KindLeaf, Purpose: "another scope",
	}); err == nil {
		t.Fatal("expected creating under a deleted parent to fail")
	}
	if err := service.DeleteNodeIn(ctx, tx, leaf.ID, leaf.Revision); err == nil {
		t.Fatal("expected deleting under a deleted parent to fail")
	}
}
