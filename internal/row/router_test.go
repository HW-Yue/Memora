package row_test

import (
	"context"
	"path/filepath"
	"sort"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/row"
	nativekvstore "github.com/HW-Yue/Memora/internal/store/nativekv"
)

func TestRowWritesAtomicallyMaintainMultiLeafRouterMounts(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, err := nativekvstore.Open(filepath.Join(t.TempDir(), "database.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	dictionary := catalog.New(databaseStore, catalog.Options{
		IDs: &idSource{values: []string{"database", "table", "title", "body"}},
	})
	createSchema(t, ctx, dictionary)
	service := row.New(databaseStore, dictionary, row.Options{
		IDs:    &idSource{values: []string{"note"}},
		Router: router.Options{IDs: &routeIDSource{values: []string{"root", "first", "second"}}},
	})
	tx, err := service.BeginTransaction(ctx)
	if err != nil {
		t.Fatal(err)
	}
	root, err := tx.CreateRouterRoot(ctx, "db_database", "Work Router")
	if err != nil {
		t.Fatal(err)
	}
	first, err := tx.CreateRouterNode(ctx, root.ID, router.NodeDefinition{
		Name: "first", Kind: router.KindLeaf, Purpose: "First scope",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := tx.CreateRouterNode(ctx, root.ID, router.NodeDefinition{
		Name: "second", Kind: router.KindLeaf, Purpose: "Second scope",
	})
	if err != nil {
		t.Fatal(err)
	}
	inserted, err := tx.Insert(ctx, "work", "notes", map[string]any{
		"title": "Routed",
	}, row.WriteOptions{
		ExpectedSchemaVersion: 1, RouteLeafIDs: []string{first.ID, second.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	assertMountedLeaves(t, ctx, service, inserted, first.ID, second.ID)

	updated, err := service.Update(ctx, "work", "notes", inserted.ID, map[string]any{
		"title": "Moved",
	}, row.WriteOptions{
		ExpectedSchemaVersion: 1, ExpectedRevision: 1,
		RouteLeafIDs: []string{second.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertMountedLeaves(t, ctx, service, updated, second.ID)

	transaction, err := service.BeginTransaction(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.Update(ctx, "work", "notes", inserted.ID, map[string]any{
		"title": "Rolled back",
	}, row.WriteOptions{
		ExpectedSchemaVersion: 1, ExpectedRevision: 2,
		RouteLeafIDs: []string{first.ID},
	}); err != nil {
		t.Fatal(err)
	}
	nodes, err := transaction.ListRouterNodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if held := leavesHolding(nodes, updated.ID); len(held) != 1 || held[0] != first.ID {
		t.Fatalf("read-own mount = %#v", held)
	}
	if err := transaction.Rollback(); err != nil {
		t.Fatal(err)
	}
	assertMountedLeaves(t, ctx, service, updated, second.ID)

	deleted, err := service.Delete(ctx, "work", "notes", inserted.ID, row.WriteOptions{
		ExpectedSchemaVersion: 1, ExpectedRevision: updated.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertMountedLeaves(t, ctx, service, deleted)
}

// assertMountedLeaves checks which leaves hold a Row by reading the leaves
// themselves — the only place the mount lives now.
func assertMountedLeaves(
	t *testing.T,
	ctx context.Context,
	service *row.Service,
	value row.Row,
	leafIDs ...string,
) {
	t.Helper()
	nodes, err := service.ListRouterNodes(ctx)
	if err != nil {
		t.Fatalf("ListRouterNodes() = %v", err)
	}
	held := leavesHolding(nodes, value.ID)
	if len(held) != len(leafIDs) {
		t.Fatalf("leaves holding %s = %#v, want %#v", value.ID, held, leafIDs)
	}
	for index, leafID := range leafIDs {
		if held[index] != leafID {
			t.Fatalf("leaf %d = %s, want %s", index, held[index], leafID)
		}
	}
}

func leavesHolding(nodes []router.Node, rowID string) []string {
	held := []string{}
	for _, node := range nodes {
		if !node.Deleted && node.RowID == rowID {
			held = append(held, node.ID)
		}
	}
	sort.Strings(held)
	return held
}

type routeIDSource struct {
	values []string
}

func (source *routeIDSource) Next() (string, error) {
	value := source.values[0]
	source.values = source.values[1:]
	return value, nil
}
