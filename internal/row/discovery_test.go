package row_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/discovery"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/row"
	sqlitestore "github.com/HW-Yue/Memora/internal/store/sqlite"
)

func TestRowDiscoveryFusesRealRouterIndexesAndRelationsInOneSnapshot(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, err := sqlitestore.Open(filepath.Join(t.TempDir(), "database.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	dictionary := catalog.New(databaseStore, catalog.Options{
		IDs: &idSource{values: []string{"database", "table", "title", "body"}},
	})
	createSchema(t, ctx, dictionary)
	service := row.New(databaseStore, dictionary, row.Options{
		IDs:         &idSource{values: []string{"wrong", "correct", "related"}},
		RelationIDs: &relationIDSource{values: []string{"context"}},
		Router: router.Options{IDs: &routeIDSource{
			values: []string{"root", "wrong", "right"},
		}},
	})
	transaction, err := service.BeginTransaction(ctx)
	if err != nil {
		t.Fatal(err)
	}
	root, err := transaction.CreateRouterRoot(ctx, "db_database", "Work Router")
	if err != nil {
		t.Fatal(err)
	}
	wrongLeaf, err := transaction.CreateRouterNode(ctx, root.ID, router.NodeDefinition{
		Name: "wrong", Kind: router.KindLeaf, Purpose: "Deliberately wrong branch",
	})
	if err != nil {
		t.Fatal(err)
	}
	rightLeaf, err := transaction.CreateRouterNode(ctx, root.ID, router.NodeDefinition{
		Name: "right", Kind: router.KindLeaf, Purpose: "Correct branch",
	})
	if err != nil {
		t.Fatal(err)
	}
	wrong, err := transaction.Insert(ctx, "work", "notes", map[string]any{
		"title": "Unrelated note",
	}, row.WriteOptions{
		ExpectedSchemaVersion: 1, IndexTerms: []string{"unrelated"},
		RouteLeafIDs: []string{wrongLeaf.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	correct, err := transaction.Insert(ctx, "work", "notes", map[string]any{
		"title": "Architecture decision",
	}, row.WriteOptions{
		ExpectedSchemaVersion: 1, IndexTerms: []string{"architecture"},
		RouteLeafIDs: []string{rightLeaf.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	related, err := transaction.Insert(ctx, "work", "notes", map[string]any{
		"title": "Supporting context",
	}, row.WriteOptions{ExpectedSchemaVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.Relate(ctx, row.RelationDefinition{
		Source: endpoint("work", "notes", correct.ID),
		Type:   "supported_by",
		Target: endpoint("work", "notes", related.ID),
	}); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}

	found, err := service.Discover(ctx, discovery.Request{
		DatabaseID: "db_database", RoutePaths: []string{"/wrong"},
		Query: "architecture", QueryTerms: []string{"architecture"}, Limit: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found.Candidates) != 3 ||
		found.Candidates[0].RowID != correct.ID ||
		found.Candidates[0].SearchScore != 1 ||
		found.Candidates[1].RowID != wrong.ID ||
		found.Candidates[1].RouteScore != 1 ||
		found.Candidates[2].RowID != related.ID ||
		found.Candidates[2].RelationScore != 1 {
		t.Fatalf("Discover() = %#v", found)
	}
}
