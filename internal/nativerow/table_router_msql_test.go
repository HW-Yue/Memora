package nativerow

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/nativecatalog"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

func TestAITableRouterMSQLNavigatesOneLayerAtATimeToExactRowID(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "database.memora")
	file, err := nativestore.Create(path, nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 30, 21, 0, 0, 0, time.UTC)
	dictionary := nativecatalog.NewService(nativecatalog.New(file), nativecatalog.ServiceOptions{IDs: &testIDs{values: []string{"database", "table", "title"}}, Clock: testClock{value: now}})
	rows := NewService(New(file), dictionary, ServiceOptions{IDs: &testIDs{values: []string{"root", "branch", "leaf", "first"}}, Clock: testClock{value: now}})
	engine := executor.New(dictionary, rows)
	executeMSQL(t, ctx, engine, "CREATE DATABASE work PURPOSE 'Work' SCOPE 'Projects'", executor.Parameters{}, executor.MutationOptions{})
	executeMSQL(t, ctx, engine, "CREATE TABLE work.notes PURPOSE 'Notes' ROW SEMANTICS 'One note' (title TEXT(40) NOT NULL PURPOSE 'Title')", executor.Parameters{}, executor.MutationOptions{})
	executeMSQL(t, ctx, engine, "CREATE ROUTE ROOT FOR TABLE work.notes PURPOSE 'Notes Router'", executor.Parameters{}, executor.MutationOptions{MaxAffectedRows: 1})
	executeMSQL(t, ctx, engine, "CREATE ROUTE UNDER :parent NAME :name KIND :kind PURPOSE :purpose", executor.Parameters{Named: map[string]any{"parent": "route_root", "name": "architecture", "kind": "branch", "purpose": "Architecture knowledge"}}, executor.MutationOptions{MaxAffectedRows: 1})
	executeMSQL(t, ctx, engine, "CREATE ROUTE UNDER :parent NAME :name KIND :kind PURPOSE :purpose", executor.Parameters{Named: map[string]any{"parent": "route_branch", "name": "storage", "kind": "leaf", "purpose": "Storage decisions"}}, executor.MutationOptions{MaxAffectedRows: 1})
	executeMSQL(t, ctx, engine, "INSERT INTO work.notes (title) VALUES (:title)", executor.Parameters{Named: map[string]any{"title": "native authority"}}, executor.MutationOptions{ExpectedSchemaVersion: 1, MaxAffectedRows: 1, RouteLeafIDs: []string{"route_leaf"}})

	databases := executeMSQL(t, ctx, engine, "SHOW DATABASES COMPACT", executor.Parameters{}, executor.MutationOptions{})
	tables := executeMSQL(t, ctx, engine, "SHOW TABLES FROM work COMPACT", executor.Parameters{}, executor.MutationOptions{})
	described := executeMSQL(t, ctx, engine, "DESCRIBE TABLE work.notes COMPACT", executor.Parameters{}, executor.MutationOptions{})
	top := executeMSQL(t, ctx, engine, "SHOW ROUTES FROM TABLE work.notes AT ROOT LIMIT 12", executor.Parameters{}, executor.MutationOptions{})
	children := executeMSQL(t, ctx, engine, "SHOW ROUTES UNDER :route LIMIT 12", executor.Parameters{Named: map[string]any{"route": "route_branch"}}, executor.MutationOptions{})
	opened := executeMSQL(t, ctx, engine, "OPEN ROUTE :leaf LIMIT 20", executor.Parameters{Named: map[string]any{"leaf": "route_leaf"}}, executor.MutationOptions{})
	selected := executeMSQL(t, ctx, engine, "SELECT * FROM work.notes WHERE row_id = :row LIMIT 1", executor.Parameters{Named: map[string]any{"row": "row_first"}}, executor.MutationOptions{})
	if len(databases.Rows) != 1 || len(tables.Rows) != 1 || len(described.Rows) != 1 {
		t.Fatalf("discovery frames = %#v, %#v, %#v", databases.Rows, tables.Rows, described.Rows)
	}
	if len(top.Rows) != 1 || top.Rows[0]["route_id"] != "route_branch" || len(top.Rows[0]) != 7 {
		t.Fatalf("top Route Frame = %#v", top)
	}
	if len(children.Rows) != 1 || children.Rows[0]["route_id"] != "route_leaf" {
		t.Fatalf("child Route Frame = %#v", children)
	}
	if len(opened.Rows) != 1 || opened.Rows[0]["row_id"] != "row_first" || len(opened.Rows[0]) != 4 {
		t.Fatalf("OPEN locator frame = %#v", opened)
	}
	if len(selected.Rows) != 1 || selected.Rows[0]["title"] != "native authority" {
		t.Fatalf("exact RowID SELECT = %#v", selected)
	}
	updated := executeMSQL(t, ctx, engine,
		"UPDATE work.notes SET title = :title WHERE row_id = :row",
		executor.Parameters{Named: map[string]any{"title": "native authority revised", "row": "row_first"}},
		executor.MutationOptions{
			ExpectedSchemaVersion: 1, ExpectedRevision: 1, MaxAffectedRows: 1,
			RouteLeafIDs: []string{"route_leaf"},
		},
	)
	openedAfterUpdate := executeMSQL(t, ctx, engine, "OPEN ROUTE :leaf LIMIT 20",
		executor.Parameters{Named: map[string]any{"leaf": "route_leaf"}}, executor.MutationOptions{})
	if updated.Revision == nil || *updated.Revision != 2 ||
		len(openedAfterUpdate.Rows) != 1 ||
		openedAfterUpdate.Rows[0]["row_id"] != "row_first" ||
		openedAfterUpdate.Rows[0]["revision"] != uint64(2) {
		t.Fatalf("UPDATE Route locator = updated %#v, opened %#v", updated, openedAfterUpdate)
	}
	executeMSQL(t, ctx, engine,
		"UPDATE work.notes SET title = :title WHERE row_id = :row",
		executor.Parameters{Named: map[string]any{"title": "temporarily unassigned", "row": "row_first"}},
		executor.MutationOptions{
			ExpectedSchemaVersion: 1, ExpectedRevision: 2, MaxAffectedRows: 1,
			RouteLeafIDs: []string{},
		},
	)
	openedWithoutMembership := executeMSQL(t, ctx, engine, "OPEN ROUTE :leaf LIMIT 20",
		executor.Parameters{Named: map[string]any{"leaf": "route_leaf"}}, executor.MutationOptions{})
	if len(openedWithoutMembership.Rows) != 0 {
		t.Fatalf("cleared Route locator = %#v", openedWithoutMembership)
	}
	reattached := executeMSQL(t, ctx, engine,
		"UPDATE work.notes SET title = :title WHERE row_id = :row",
		executor.Parameters{Named: map[string]any{"title": "native authority reattached", "row": "row_first"}},
		executor.MutationOptions{
			ExpectedSchemaVersion: 1, ExpectedRevision: 3, MaxAffectedRows: 1,
			RouteLeafIDs: []string{"route_leaf"},
		},
	)
	openedAfterReattach := executeMSQL(t, ctx, engine, "OPEN ROUTE :leaf LIMIT 20",
		executor.Parameters{Named: map[string]any{"leaf": "route_leaf"}}, executor.MutationOptions{})
	if reattached.Revision == nil || *reattached.Revision != 4 ||
		len(openedAfterReattach.Rows) != 1 ||
		openedAfterReattach.Rows[0]["row_id"] != "row_first" ||
		openedAfterReattach.Rows[0]["revision"] != uint64(4) {
		t.Fatalf("reattached Route locator = updated %#v, opened %#v", reattached, openedAfterReattach)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := nativestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	dictionary = nativecatalog.NewService(nativecatalog.New(reopened), nativecatalog.ServiceOptions{})
	rows = NewService(New(reopened), dictionary, ServiceOptions{})
	engine = executor.New(dictionary, rows)
	afterRestart := executeMSQL(t, ctx, engine, "OPEN ROUTE 'route_leaf' LIMIT 20", executor.Parameters{}, executor.MutationOptions{})
	if len(afterRestart.Rows) != 1 || afterRestart.Rows[0]["row_id"] != "row_first" ||
		afterRestart.Rows[0]["revision"] != uint64(4) {
		t.Fatalf("OPEN after restart = %#v", afterRestart)
	}
}
