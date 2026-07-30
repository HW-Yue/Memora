package row_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/row"
	nativekvstore "github.com/HW-Yue/Memora/internal/store/nativekv"
)

func TestRowWritesAtomicallyMaintainMultiLeafRouterMemberships(t *testing.T) {
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
	assertRouteMemberships(t, ctx, service, inserted, first.ID, second.ID)

	updated, err := service.Update(ctx, "work", "notes", inserted.ID, map[string]any{
		"title": "Moved",
	}, row.WriteOptions{
		ExpectedSchemaVersion: 1, ExpectedRevision: 1,
		RouteLeafIDs: []string{second.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertRouteMemberships(t, ctx, service, updated, second.ID)

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
	if memberships, err := transaction.RouterMemberships(ctx, updated); err != nil ||
		len(memberships) != 1 || memberships[0].LeafID != first.ID {
		t.Fatalf("read-own Router membership = %#v, %v", memberships, err)
	}
	if err := transaction.Rollback(); err != nil {
		t.Fatal(err)
	}
	assertRouteMemberships(t, ctx, service, updated, second.ID)

	deleted, err := service.Delete(ctx, "work", "notes", inserted.ID, row.WriteOptions{
		ExpectedSchemaVersion: 1, ExpectedRevision: updated.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertRouteMemberships(t, ctx, service, deleted)
}

func assertRouteMemberships(
	t *testing.T,
	ctx context.Context,
	service *row.Service,
	value row.Row,
	leafIDs ...string,
) {
	t.Helper()
	memberships, err := service.RouterMemberships(ctx, value)
	if err != nil || len(memberships) != len(leafIDs) {
		t.Fatalf("RouterMemberships() = %#v, %v", memberships, err)
	}
	for index, leafID := range leafIDs {
		if memberships[index].LeafID != leafID || memberships[index].Revision != value.Revision {
			t.Fatalf("membership %d = %#v, want %s rev %d", index, memberships[index], leafID, value.Revision)
		}
	}
}

type routeIDSource struct {
	values []string
}

func (source *routeIDSource) Next() (string, error) {
	value := source.values[0]
	source.values = source.values[1:]
	return value, nil
}
