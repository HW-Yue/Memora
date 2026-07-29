package router_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/store"
	sqlitestore "github.com/HW-Yue/Memora/internal/store/sqlite"
)

func TestRouterBuildsMultiBranchTreeAndMaintainsMultiLeafReverseMembership(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, err := sqlitestore.Open(filepath.Join(t.TempDir(), "database.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	service := router.New(databaseStore, router.Options{
		IDs: &idSource{values: []string{"root", "projects", "memora", "storage", "msql"}},
	})
	tx := mustBegin(t, ctx, databaseStore)
	root, err := service.CreateRootIn(ctx, tx, "db_work", "Work knowledge")
	if err != nil {
		t.Fatal(err)
	}
	projects := mustCreateNode(t, ctx, service, tx, root.ID, "projects", router.KindBranch)
	memora := mustCreateNode(t, ctx, service, tx, projects.ID, "memora", router.KindBranch)
	storage := mustCreateNode(t, ctx, service, tx, memora.ID, "存储引擎", router.KindLeaf)
	msql := mustCreateNode(t, ctx, service, tx, memora.ID, "msql", router.KindLeaf)
	row := router.Locator{
		DatabaseID: "db_work", TableID: "tbl_notes", RowID: "row_note", Revision: 1,
	}
	if _, err := service.ReplaceMembershipsIn(ctx, tx, row, []string{storage.ID, msql.ID}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	memberships, err := service.MembershipsForRow(ctx, row.DatabaseID, row.TableID, row.RowID)
	if err != nil || len(memberships) != 2 ||
		memberships[0].LeafID != msql.ID || memberships[1].LeafID != storage.ID {
		t.Fatalf("reverse memberships = %#v, %v", memberships, err)
	}
	storageRows, err := service.ListLeaf(ctx, storage.ID, 10)
	if err != nil || len(storageRows) != 1 || storageRows[0] != row {
		t.Fatalf("storage leaf = %#v, %v", storageRows, err)
	}

	tx = mustBegin(t, ctx, databaseStore)
	renamed, err := service.RenameIn(ctx, tx, memora.ID, "memora-db", 1)
	if err != nil {
		t.Fatal(err)
	}
	if renamed.ID != memora.ID || renamed.Revision != 2 ||
		renamed.Path != "/projects/memora-db" {
		t.Fatalf("renamed = %#v", renamed)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/projects/memora", "/projects/memora-db"} {
		resolved, err := service.ResolvePath(ctx, "db_work", path)
		if err != nil || resolved.ID != memora.ID {
			t.Fatalf("ResolvePath(%q) = %#v, %v", path, resolved, err)
		}
	}
	descendant, err := service.ResolvePath(ctx, "db_work", "/projects/memora-db/存储引擎")
	if err != nil || descendant.ID != storage.ID {
		t.Fatalf("renamed descendant = %#v, %v", descendant, err)
	}
}

func TestRouterSupportsExplicitSplitMergeDeleteAndTransactionRollback(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, err := sqlitestore.Open(filepath.Join(t.TempDir(), "database.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	service := router.New(databaseStore, router.Options{
		IDs: &idSource{values: []string{"root", "combined", "left", "right", "merged", "rolled"}},
	})
	tx := mustBegin(t, ctx, databaseStore)
	root, err := service.CreateRootIn(ctx, tx, "db_work", "Work")
	if err != nil {
		t.Fatal(err)
	}
	combined := mustCreateNode(t, ctx, service, tx, root.ID, "combined", router.KindLeaf)
	left := mustCreateNode(t, ctx, service, tx, root.ID, "left", router.KindLeaf)
	right := mustCreateNode(t, ctx, service, tx, root.ID, "right", router.KindLeaf)
	first := router.Locator{DatabaseID: "db_work", TableID: "tbl_notes", RowID: "row_first", Revision: 1}
	second := router.Locator{DatabaseID: "db_work", TableID: "tbl_notes", RowID: "row_second", Revision: 1}
	if _, err := service.ReplaceMembershipsIn(ctx, tx, first, []string{combined.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReplaceMembershipsIn(ctx, tx, second, []string{combined.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReplaceMembershipsIn(ctx, tx, first, []string{left.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReplaceMembershipsIn(ctx, tx, second, []string{right.ID}); err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteNodeIn(ctx, tx, combined.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteNodeIn(ctx, tx, right.ID, 1); err != nil {
		t.Fatal(err)
	}
	merged := mustCreateNode(t, ctx, service, tx, root.ID, "merged", router.KindLeaf)
	if _, err := service.ReplaceMembershipsIn(ctx, tx, first, []string{merged.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReplaceMembershipsIn(ctx, tx, second, []string{merged.ID}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	rows, err := service.ListLeaf(ctx, merged.ID, 10)
	if err != nil || len(rows) != 2 {
		t.Fatalf("merged rows = %#v, %v", rows, err)
	}

	tx = mustBegin(t, ctx, databaseStore)
	rolled := mustCreateNode(t, ctx, service, tx, root.ID, "rolled", router.KindLeaf)
	if _, err := service.ReplaceMembershipsIn(ctx, tx, first, []string{rolled.ID}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Get(ctx, rolled.ID); err == nil {
		t.Fatal("rolled-back Router node remained visible")
	}
	rows, err = service.ListLeaf(ctx, merged.ID, 10)
	if err != nil || len(rows) != 2 {
		t.Fatalf("memberships after rollback = %#v, %v", rows, err)
	}
}

func TestRouterEnforcesCapacityAndProvidesStableChildCursor(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, err := sqlitestore.Open(filepath.Join(t.TempDir(), "database.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	service := router.New(databaseStore, router.Options{
		IDs:         &idSource{values: []string{"root", "a", "b", "overflow"}},
		MaxChildren: 2, MaxLeafRows: 2,
	})
	tx := mustBegin(t, ctx, databaseStore)
	root, err := service.CreateRootIn(ctx, tx, "db_work", "Work")
	if err != nil {
		t.Fatal(err)
	}
	mustCreateNode(t, ctx, service, tx, root.ID, "b", router.KindLeaf)
	a := mustCreateNode(t, ctx, service, tx, root.ID, "a", router.KindLeaf)
	_, err = service.CreateNodeIn(ctx, tx, root.ID, router.NodeDefinition{
		Name: "overflow", Kind: router.KindLeaf, Purpose: "Overflow",
	})
	assertCode(t, err, result.CodeConstraint)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	tx = mustBegin(t, ctx, databaseStore)
	for _, rowID := range []string{"row_first", "row_second"} {
		if _, err := service.ReplaceMembershipsIn(ctx, tx, router.Locator{
			DatabaseID: "db_work", TableID: "tbl_notes", RowID: rowID, Revision: 1,
		}, []string{a.ID}); err != nil {
			t.Fatal(err)
		}
	}
	_, err = service.ReplaceMembershipsIn(ctx, tx, router.Locator{
		DatabaseID: "db_work", TableID: "tbl_notes", RowID: "row_third", Revision: 1,
	}, []string{a.ID})
	assertCode(t, err, result.CodeConstraint)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	first, cursor, err := service.ListChildren(ctx, root.ID, "", 1)
	if err != nil || len(first) != 1 || first[0].Name != "a" || cursor == "" {
		t.Fatalf("first page = %#v, %q, %v", first, cursor, err)
	}
	second, next, err := service.ListChildren(ctx, root.ID, cursor, 1)
	if err != nil || len(second) != 1 || second[0].Name != "b" || next != "" {
		t.Fatalf("second page = %#v, %q, %v", second, next, err)
	}
	if _, _, err := service.ListChildren(ctx, root.ID, "invalid", 1); err == nil {
		t.Fatal("invalid cursor succeeded")
	}
}

func mustCreateNode(
	t *testing.T,
	ctx context.Context,
	service *router.Service,
	tx store.Tx,
	parentID, name string,
	kind router.Kind,
) router.Node {
	t.Helper()
	node, err := service.CreateNodeIn(ctx, tx, parentID, router.NodeDefinition{
		Name: name, Kind: kind, Purpose: name + " scope",
	})
	if err != nil {
		t.Fatal(err)
	}
	return node
}

func mustBegin(t *testing.T, ctx context.Context, database store.Store) store.Tx {
	t.Helper()
	tx, err := database.Begin(ctx, store.ReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

func assertCode(t *testing.T, err error, want result.Code) {
	t.Helper()
	var stable interface{ StableCode() string }
	if !errors.As(err, &stable) || stable.StableCode() != string(want) {
		t.Fatalf("error = %v, want code %q", err, want)
	}
}

type idSource struct {
	values []string
}

func (source *idSource) Next() (string, error) {
	value := source.values[0]
	source.values = source.values[1:]
	return value, nil
}
