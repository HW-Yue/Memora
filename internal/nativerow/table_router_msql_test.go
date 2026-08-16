package nativerow

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/nativecatalog"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/security"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

func TestRouteReadProtocolPaginatesChildrenAndReturnsOneLocatorWithoutRowBodies(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "database.memora")
	file, err := nativestore.Create(path, nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	now := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	dictionary := nativecatalog.NewService(nativecatalog.New(file), nativecatalog.ServiceOptions{
		IDs: &testIDs{values: []string{"database", "table", "title"}}, Clock: testClock{value: now},
	})
	rows := NewService(New(file), dictionary, ServiceOptions{
		IDs:   &testIDs{values: []string{"root", "alpha", "beta", "gamma", "first", "second", "third", "delta", "fourth"}},
		Clock: testClock{value: now},
	})
	engine := executor.New(dictionary, rows)
	executeMSQL(t, ctx, engine, "CREATE DATABASE work PURPOSE 'Work' SCOPE 'Projects'", executor.Parameters{}, executor.MutationOptions{})
	executeMSQL(t, ctx, engine, "CREATE TABLE work.notes PURPOSE 'Notes' ROW SEMANTICS 'One note' (title TEXT(40) NOT NULL PURPOSE 'Title')", executor.Parameters{}, executor.MutationOptions{})
	emptyRoutes := executeMSQL(t, ctx, engine,
		"SHOW ROUTES FROM TABLE work.notes AT ROOT LIMIT 2", executor.Parameters{}, executor.MutationOptions{})
	if len(emptyRoutes.Rows) != 0 || emptyRoutes.Page == nil || emptyRoutes.Page.Snapshot == "" || emptyRoutes.Page.Truncated {
		t.Fatalf("Route page before root creation = %#v", emptyRoutes)
	}
	executeMSQL(t, ctx, engine, "CREATE ROUTE ROOT FOR TABLE work.notes PURPOSE 'Notes Router'", executor.Parameters{}, executor.MutationOptions{MaxAffectedRows: 1})
	for _, name := range []string{"alpha", "beta", "gamma"} {
		executeMSQL(t, ctx, engine,
			"CREATE ROUTE UNDER :parent NAME :name KIND 'leaf' PURPOSE :purpose",
			executor.Parameters{Named: map[string]any{
				"parent": "route_root", "name": name, "purpose": name + " notes",
			}}, executor.MutationOptions{MaxAffectedRows: 1})
	}
	for index, name := range []string{"first", "second", "third"} {
		leafID := []string{"route_alpha", "route_beta", "route_gamma"}[index]
		executeMSQL(t, ctx, engine, "INSERT INTO work.notes (title) VALUES (:title)",
			executor.Parameters{Named: map[string]any{"title": name}}, executor.MutationOptions{
				ExpectedSchemaVersion: 1, MaxAffectedRows: 1, RouteLeafIDs: []string{leafID},
			})
	}

	children := executeMSQL(t, ctx, engine,
		"SHOW ROUTES FROM TABLE work.notes AT ROOT LIMIT 2", executor.Parameters{}, executor.MutationOptions{})
	if children.Page == nil || children.Page.Version != result.ListPageVersion ||
		children.Page.Snapshot == "" || !children.Truncated || children.NextCursor == "" ||
		len(children.Rows) != 2 || children.Rows[0]["name"] != "alpha" ||
		children.Rows[1]["name"] != "beta" {
		t.Fatalf("first children page = %#v", children)
	}
	for _, child := range children.Rows {
		if len(child) != 10 || child["aliases"] == nil || child["synopsis"] != nil || child["title"] != nil ||
			child["database_id"] != "db_database" || child["table_id"] != "tbl_table" {
			t.Fatalf("child frame leaked on-demand or Row content = %#v", child)
		}
	}
	described := executeMSQL(t, ctx, engine, "DESCRIBE ROUTE 'route_alpha'", executor.Parameters{}, executor.MutationOptions{})
	if len(described.Rows) != 1 || len(described.Rows[0]) != 11 ||
		described.Rows[0]["route_id"] != "route_alpha" ||
		described.Rows[0]["database_id"] != "db_database" ||
		described.Rows[0]["table_id"] != "tbl_table" {
		t.Fatalf("Route point read = %#v", described)
	}
	remainingChildren := executeMSQL(t, ctx, engine,
		"SHOW ROUTES FROM TABLE work.notes AT ROOT CURSOR :cursor LIMIT 2",
		executor.Parameters{Named: map[string]any{"cursor": children.NextCursor}}, executor.MutationOptions{})
	if remainingChildren.Page == nil || remainingChildren.Page.Cursor != children.NextCursor ||
		remainingChildren.Page.Snapshot != children.Page.Snapshot || remainingChildren.Truncated ||
		len(remainingChildren.Rows) != 1 || remainingChildren.Rows[0]["name"] != "gamma" {
		t.Fatalf("second children page = %#v", remainingChildren)
	}

	locators := executeMSQL(t, ctx, engine, "OPEN ROUTE 'route_alpha' LIMIT 1", executor.Parameters{}, executor.MutationOptions{})
	if locators.Page == nil || locators.Page.Version != result.ListPageVersion ||
		locators.Page.Snapshot == "" || locators.Truncated || locators.NextCursor != "" || len(locators.Rows) != 1 {
		t.Fatalf("single locator page = %#v", locators)
	}
	for _, locator := range locators.Rows {
		if len(locator) != 4 || locator["row_id"] == "" || locator["title"] != nil {
			t.Fatalf("locator leaked Row body = %#v", locator)
		}
	}
	executeMSQL(t, ctx, engine,
		"CREATE ROUTE UNDER 'route_root' NAME 'delta' KIND 'leaf' PURPOSE 'Delta notes'",
		executor.Parameters{}, executor.MutationOptions{MaxAffectedRows: 1})
	currentChildren := executeMSQL(t, ctx, engine,
		"SHOW ROUTES FROM TABLE work.notes AT ROOT LIMIT 10", executor.Parameters{}, executor.MutationOptions{})
	if len(currentChildren.Rows) != 4 {
		t.Fatalf("children after Route mutation = %#v", currentChildren)
	}
	changedChildren, err := runMSQL(ctx, engine,
		"SHOW ROUTES FROM TABLE work.notes AT ROOT CURSOR :cursor LIMIT 2",
		executor.Parameters{Named: map[string]any{"cursor": children.NextCursor}}, executor.MutationOptions{})
	if !hasCode(err, string(result.CodeRevisionConflict)) {
		t.Fatalf("changed children rows = %#v, page = %+v, original page = %+v, error = %v",
			changedChildren.Rows, changedChildren.Page, children.Page, err)
	}
	executeMSQL(t, ctx, engine, "INSERT INTO work.notes (title) VALUES ('fourth')",
		executor.Parameters{}, executor.MutationOptions{
			ExpectedSchemaVersion: 1, MaxAffectedRows: 1, RouteLeafIDs: []string{"route_delta"},
		})
	_, err = runMSQL(ctx, engine, "SHOW ROUTES UNDER 'route_alpha' LIMIT 1", executor.Parameters{}, executor.MutationOptions{})
	if !hasCode(err, string(result.CodeConstraint)) {
		t.Fatalf("SHOW under leaf error = %v", err)
	}
	_, err = runMSQL(ctx, engine, "OPEN ROUTE 'route_root' LIMIT 1", executor.Parameters{}, executor.MutationOptions{})
	if !hasCode(err, string(result.CodeConstraint)) {
		t.Fatalf("OPEN root error = %v", err)
	}
	_, err = runMSQL(ctx, engine, "DESCRIBE ROUTE 'route_missing'", executor.Parameters{}, executor.MutationOptions{})
	if !hasCode(err, string(result.CodeNotFound)) {
		t.Fatalf("missing Route error = %v", err)
	}
	unauthorized := security.WithAuthorization(ctx, security.Authorization{
		Version: security.AuthorizationVersion, Actor: "agent:test", AuthorizedDatabases: []string{"private"},
	})
	for _, source := range []string{
		"SHOW ROUTES FROM TABLE work.notes AT ROOT LIMIT 1",
		"SHOW ROUTES UNDER 'route_root' LIMIT 1",
		"OPEN ROUTE 'route_alpha' LIMIT 1",
		"DESCRIBE ROUTE 'route_alpha'",
	} {
		if _, err := runMSQL(unauthorized, engine, source, executor.Parameters{}, executor.MutationOptions{}); !hasCode(err, string(result.CodePermissionDenied)) {
			t.Fatalf("unauthorized %s error = %v", source, err)
		}
	}
}

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
	opened := executeMSQL(t, ctx, engine, "OPEN ROUTE :leaf LIMIT 1", executor.Parameters{Named: map[string]any{"leaf": "route_leaf"}}, executor.MutationOptions{})
	selected := executeMSQL(t, ctx, engine, "SELECT * FROM work.notes WHERE row_id = :row LIMIT 1", executor.Parameters{Named: map[string]any{"row": "row_first"}}, executor.MutationOptions{})
	if len(databases.Rows) != 1 || len(tables.Rows) != 1 || len(described.Rows) != 1 {
		t.Fatalf("discovery frames = %#v, %#v, %#v", databases.Rows, tables.Rows, described.Rows)
	}
	if len(top.Rows) != 1 || top.Rows[0]["route_id"] != "route_branch" ||
		len(top.Rows[0]) != 10 || top.Rows[0]["aliases"] == nil ||
		top.Rows[0]["database_id"] != tables.Rows[0]["database_id"] ||
		top.Rows[0]["table_id"] != tables.Rows[0]["table_id"] {
		t.Fatalf("top Route Frame = %#v", top)
	}
	if len(children.Rows) != 1 || children.Rows[0]["route_id"] != "route_leaf" ||
		children.Rows[0]["database_id"] != tables.Rows[0]["database_id"] ||
		children.Rows[0]["table_id"] != tables.Rows[0]["table_id"] {
		t.Fatalf("child Route Frame = %#v", children)
	}
	if len(opened.Rows) != 1 || opened.Rows[0]["row_id"] != "row_first" || len(opened.Rows[0]) != 4 {
		t.Fatalf("OPEN locator frame = %#v", opened)
	}
	if len(selected.Rows) != 1 || selected.Rows[0]["title"] != "native authority" {
		t.Fatalf("exact RowID SELECT = %#v", selected)
	}
	if paths, ok := selected.Rows[0]["route_paths"].([]string); !ok || len(paths) != 1 || paths[0] != "/architecture/storage" {
		t.Fatalf("exact RowID SELECT route_paths = %#v", selected.Rows[0]["route_paths"])
	}
	updated := executeMSQL(t, ctx, engine,
		"UPDATE work.notes SET title = :title WHERE row_id = :row",
		executor.Parameters{Named: map[string]any{"title": "native authority revised", "row": "row_first"}},
		executor.MutationOptions{
			ExpectedSchemaVersion: 1, ExpectedRevision: 1, MaxAffectedRows: 1,
			RouteLeafIDs: []string{"route_leaf"},
		},
	)
	openedAfterUpdate := executeMSQL(t, ctx, engine, "OPEN ROUTE :leaf LIMIT 1",
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
	openedWithoutMembership := executeMSQL(t, ctx, engine, "OPEN ROUTE :leaf LIMIT 1",
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
	openedAfterReattach := executeMSQL(t, ctx, engine, "OPEN ROUTE :leaf LIMIT 1",
		executor.Parameters{Named: map[string]any{"leaf": "route_leaf"}}, executor.MutationOptions{})
	if reattached.Revision == nil || *reattached.Revision != 4 ||
		len(openedAfterReattach.Rows) != 1 ||
		openedAfterReattach.Rows[0]["row_id"] != "row_first" ||
		openedAfterReattach.Rows[0]["revision"] != uint64(4) {
		t.Fatalf("reattached Route locator = updated %#v, opened %#v", reattached, openedAfterReattach)
	}
	asOfCommit := executeMSQL(t, ctx, engine,
		"SELECT title, revision FROM work.notes AS OF COMMIT_SEQUENCE :sequence WHERE row_id = :row LIMIT 1",
		executor.Parameters{Named: map[string]any{"sequence": *updated.CommitSequence, "row": "row_first"}},
		executor.MutationOptions{},
	)
	if len(asOfCommit.Rows) != 1 || asOfCommit.Rows[0]["title"] != "native authority revised" ||
		asOfCommit.Rows[0]["revision"] != uint64(2) {
		t.Fatalf("AS OF COMMIT_SEQUENCE = %#v", asOfCommit)
	}
	executeMSQL(t, ctx, engine,
		"DELETE FROM work.notes WHERE row_id = :row",
		executor.Parameters{Named: map[string]any{"row": "row_first"}},
		executor.MutationOptions{ExpectedSchemaVersion: 1, ExpectedRevision: 4, MaxAffectedRows: 1},
	)
	openedAfterDelete := executeMSQL(t, ctx, engine, "OPEN ROUTE :leaf LIMIT 1",
		executor.Parameters{Named: map[string]any{"leaf": "route_leaf"}}, executor.MutationOptions{})
	if len(openedAfterDelete.Rows) != 0 {
		t.Fatalf("deleted Route locator = %#v", openedAfterDelete)
	}
	restored := executeMSQL(t, ctx, engine,
		"RESTORE work.notes ROW :row TO REVISION :revision",
		executor.Parameters{Named: map[string]any{"row": "row_first", "revision": 2}},
		executor.MutationOptions{
			ExpectedSchemaVersion: 1, ExpectedRevision: 5, MaxAffectedRows: 1,
			RouteLeafIDs: []string{"route_leaf"},
		},
	)
	openedAfterRestore := executeMSQL(t, ctx, engine, "OPEN ROUTE :leaf LIMIT 1",
		executor.Parameters{Named: map[string]any{"leaf": "route_leaf"}}, executor.MutationOptions{})
	if restored.Revision == nil || *restored.Revision != 6 ||
		len(openedAfterRestore.Rows) != 1 ||
		openedAfterRestore.Rows[0]["row_id"] != "row_first" ||
		openedAfterRestore.Rows[0]["revision"] != uint64(6) {
		t.Fatalf("RESTORE Route locator = restored %#v, opened %#v", restored, openedAfterRestore)
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
	afterRestart := executeMSQL(t, ctx, engine, "OPEN ROUTE 'route_leaf' LIMIT 1", executor.Parameters{}, executor.MutationOptions{})
	if len(afterRestart.Rows) != 1 || afterRestart.Rows[0]["row_id"] != "row_first" ||
		afterRestart.Rows[0]["revision"] != uint64(6) {
		t.Fatalf("OPEN after restart = %#v", afterRestart)
	}
}
