package row_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/reindex"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/row"
	nativekvstore "github.com/HW-Yue/Memora/internal/store/nativekv"
)

func TestPendingReindexInvalidatesStaleSemanticIndexesAndRejectsOldWorkers(t *testing.T) {
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
		IDs: &idSource{values: []string{"note"}},
		Router: router.Options{IDs: &routeIDSource{
			values: []string{"root", "leaf"},
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
	leaf, err := transaction.CreateRouterNode(ctx, root.ID, router.NodeDefinition{
		Name: "notes", Kind: router.KindLeaf, Purpose: "Notes",
	})
	if err != nil {
		t.Fatal(err)
	}
	inserted, err := transaction.Insert(ctx, "work", "notes", map[string]any{
		"title": "Initial",
	}, row.WriteOptions{
		ExpectedSchemaVersion: 1, IndexTerms: []string{"initial"},
		RouteLeafIDs: []string{leaf.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}

	updated, err := service.Update(ctx, "work", "notes", inserted.ID, map[string]any{
		"title": "Updated",
	}, row.WriteOptions{ExpectedSchemaVersion: 1, ExpectedRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	assertSemanticIndexesEmpty(t, ctx, service, updated, "initial")
	assertMechanicalLookup(t, ctx, service, updated.DatabaseID, "updated", updated.ID, 2)
	tasks, err := service.ListReindexTasks(ctx)
	if err != nil || len(tasks) != 1 || tasks[0].Revision != 2 ||
		tasks[0].State != reindex.StatePending {
		t.Fatalf("pending tasks = %#v, %v", tasks, err)
	}
	oldClaim, err := service.ClaimReindex(ctx, "worker-a", time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	newest, err := service.Update(ctx, "work", "notes", inserted.ID, map[string]any{
		"title": "Newest",
	}, row.WriteOptions{ExpectedSchemaVersion: 1, ExpectedRevision: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.CompleteReindex(ctx, oldClaim, []string{"stale"}, []string{leaf.ID}); err == nil {
		t.Fatal("stale worker result succeeded")
	} else {
		assertCode(t, err, result.CodeRevisionConflict)
	}
	assertSemanticIndexesEmpty(t, ctx, service, newest, "stale")

	current, err := service.ClaimReindex(ctx, "worker-b", time.Minute)
	if err != nil || current.Revision != 3 || current.Attempts != 1 {
		t.Fatalf("current claim = %#v, %v", current, err)
	}
	if err := service.FailReindex(ctx, current, "agent temporarily unavailable"); err != nil {
		t.Fatal(err)
	}
	tasks, err = service.ListReindexTasks(ctx)
	if err != nil || len(tasks) != 1 || tasks[0].State != reindex.StateFailed ||
		tasks[0].LastError != "agent temporarily unavailable" {
		t.Fatalf("failed task = %#v, %v", tasks, err)
	}
	if err := service.RetryReindex(ctx, newest.DatabaseID, newest.TableID, newest.ID); err != nil {
		t.Fatal(err)
	}
	retry, err := service.ClaimReindex(ctx, "worker-c", time.Minute)
	if err != nil || retry.Attempts != 2 {
		t.Fatalf("retry claim = %#v, %v", retry, err)
	}
	if err := service.CompleteReindex(ctx, retry, []string{"newest"}, []string{leaf.ID}); err != nil {
		t.Fatal(err)
	}
	if err := service.CompleteReindex(ctx, retry, []string{"newest"}, []string{leaf.ID}); err != nil {
		t.Fatalf("idempotent CompleteReindex() error = %v", err)
	}
	assertAgentLookup(t, ctx, service, newest.DatabaseID, "newest", newest.ID, 3)
	assertRouteMemberships(t, ctx, service, newest, leaf.ID)

	indexed, err := service.Update(ctx, "work", "notes", newest.ID, map[string]any{
		"title": "Already indexed",
	}, row.WriteOptions{
		ExpectedSchemaVersion: 1, ExpectedRevision: 3,
		IndexTerms: []string{"indexed"}, RouteLeafIDs: []string{leaf.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if tasks, err := service.ListReindexTasks(ctx); err != nil || len(tasks) != 0 {
		t.Fatalf("tasks after explicit snapshots = %#v, %v", tasks, err)
	}
	if _, err := service.Delete(ctx, "work", "notes", indexed.ID, row.WriteOptions{
		ExpectedSchemaVersion: 1, ExpectedRevision: 4,
	}); err != nil {
		t.Fatal(err)
	}
	if tasks, err := service.ListReindexTasks(ctx); err != nil || len(tasks) != 0 {
		t.Fatalf("tasks after DELETE = %#v, %v", tasks, err)
	}
}

func assertSemanticIndexesEmpty(
	t *testing.T,
	ctx context.Context,
	service *row.Service,
	value row.Row,
	term string,
) {
	t.Helper()
	if matches, err := service.LookupAgentTerm(ctx, value.DatabaseID, term, 10); err != nil || len(matches) != 0 {
		t.Fatalf("Agent matches = %#v, %v", matches, err)
	}
	if memberships, err := service.RouterMemberships(ctx, value); err != nil || len(memberships) != 0 {
		t.Fatalf("Router memberships = %#v, %v", memberships, err)
	}
}
