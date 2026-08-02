package nativerow

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/nativecatalog"
	"github.com/HW-Yue/Memora/internal/result"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

func TestRouteLeafRejectsSecondRowAndPreservesMultiLeafRow(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	file, err := nativestore.Create(filepath.Join(t.TempDir(), "database.memora"), nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	now := time.Date(2026, 8, 2, 8, 0, 0, 0, time.UTC)
	dictionary := nativecatalog.NewService(nativecatalog.New(file), nativecatalog.ServiceOptions{
		IDs: &testIDs{values: []string{"database", "table", "title"}}, Clock: testClock{value: now},
	})
	rows := NewService(New(file), dictionary, ServiceOptions{
		IDs:   &testIDs{values: []string{"root", "primary", "alias", "first", "second"}},
		Clock: testClock{value: now},
	})
	engine := executor.New(dictionary, rows)
	executeMSQL(t, ctx, engine, "CREATE DATABASE work PURPOSE 'Work' SCOPE 'Projects'", executor.Parameters{}, executor.MutationOptions{})
	executeMSQL(t, ctx, engine, "CREATE TABLE work.notes PURPOSE 'Notes' ROW SEMANTICS 'One note' (title TEXT(40) NOT NULL PURPOSE 'Title')", executor.Parameters{}, executor.MutationOptions{})
	executeMSQL(t, ctx, engine, "CREATE ROUTE ROOT FOR TABLE work.notes PURPOSE 'Notes Router'", executor.Parameters{}, executor.MutationOptions{MaxAffectedRows: 1})
	for _, name := range []string{"primary", "alias"} {
		executeMSQL(t, ctx, engine,
			"CREATE ROUTE UNDER 'route_root' NAME :name KIND 'leaf' PURPOSE :purpose",
			executor.Parameters{Named: map[string]any{"name": name, "purpose": name + " locator"}},
			executor.MutationOptions{MaxAffectedRows: 1})
	}
	executeMSQL(t, ctx, engine, "INSERT INTO work.notes (title) VALUES ('first')",
		executor.Parameters{}, executor.MutationOptions{
			ExpectedSchemaVersion: 1, MaxAffectedRows: 1,
			RouteLeafIDs: []string{"route_primary", "route_alias"},
		})

	_, err = runMSQL(ctx, engine, "INSERT INTO work.notes (title) VALUES ('second')",
		executor.Parameters{}, executor.MutationOptions{
			ExpectedSchemaVersion: 1, MaxAffectedRows: 1,
			RouteLeafIDs: []string{"route_primary"},
		})
	if !hasCode(err, string(result.CodeConstraint)) {
		t.Fatalf("second Row in occupied leaf error = %v", err)
	}

	for _, leafID := range []string{"route_primary", "route_alias"} {
		opened := executeMSQL(t, ctx, engine, "OPEN ROUTE :leaf LIMIT 1",
			executor.Parameters{Named: map[string]any{"leaf": leafID}}, executor.MutationOptions{})
		if len(opened.Rows) != 1 || opened.Rows[0]["row_id"] != "row_first" || opened.Truncated {
			t.Fatalf("OPEN %s after rejected insert = %#v", leafID, opened)
		}
	}
	listed := executeMSQL(t, ctx, engine, "SELECT row_id, title FROM work.notes LIMIT 10",
		executor.Parameters{}, executor.MutationOptions{})
	if len(listed.Rows) != 1 || listed.Rows[0]["row_id"] != "row_first" {
		t.Fatalf("rows after rejected insert = %#v", listed)
	}
}

func TestConcurrentRowsCompetingForOneLeafHaveOneWinner(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	file, err := nativestore.Create(filepath.Join(t.TempDir(), "database.memora"), nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	now := time.Date(2026, 8, 2, 8, 30, 0, 0, time.UTC)
	dictionary := nativecatalog.NewService(nativecatalog.New(file), nativecatalog.ServiceOptions{
		IDs: &testIDs{values: []string{"database", "table", "title"}}, Clock: testClock{value: now},
	})
	rows := NewService(New(file), dictionary, ServiceOptions{
		IDs: &testIDs{values: []string{"root", "leaf", "first", "second"}}, Clock: testClock{value: now},
	})
	engine := executor.New(dictionary, rows)
	executeMSQL(t, ctx, engine, "CREATE DATABASE work PURPOSE 'Work' SCOPE 'Projects'", executor.Parameters{}, executor.MutationOptions{})
	executeMSQL(t, ctx, engine, "CREATE TABLE work.notes PURPOSE 'Notes' ROW SEMANTICS 'One note' (title TEXT(40) NOT NULL PURPOSE 'Title')", executor.Parameters{}, executor.MutationOptions{})
	executeMSQL(t, ctx, engine, "CREATE ROUTE ROOT FOR TABLE work.notes PURPOSE 'Notes Router'", executor.Parameters{}, executor.MutationOptions{MaxAffectedRows: 1})
	executeMSQL(t, ctx, engine,
		"CREATE ROUTE UNDER 'route_root' NAME 'leaf' KIND 'leaf' PURPOSE 'One locator'",
		executor.Parameters{}, executor.MutationOptions{MaxAffectedRows: 1})

	start := make(chan struct{})
	results := make(chan error, 2)
	for _, title := range []string{"first", "second"} {
		title := title
		go func() {
			<-start
			_, writeErr := runMSQL(ctx, engine, "INSERT INTO work.notes (title) VALUES (:title)",
				executor.Parameters{Named: map[string]any{"title": title}}, executor.MutationOptions{
					ExpectedSchemaVersion: 1, MaxAffectedRows: 1, RouteLeafIDs: []string{"route_leaf"},
				})
			results <- writeErr
		}()
	}
	close(start)
	succeeded, rejected := 0, 0
	for range 2 {
		writeErr := <-results
		switch {
		case writeErr == nil:
			succeeded++
		case hasCode(writeErr, string(result.CodeConstraint)):
			rejected++
		default:
			t.Fatalf("competing INSERT error = %v", writeErr)
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatalf("competing INSERT results: succeeded=%d rejected=%d", succeeded, rejected)
	}
	opened := executeMSQL(t, ctx, engine, "OPEN ROUTE 'route_leaf' LIMIT 1",
		executor.Parameters{}, executor.MutationOptions{})
	listed := executeMSQL(t, ctx, engine, "SELECT row_id, title FROM work.notes LIMIT 10",
		executor.Parameters{}, executor.MutationOptions{})
	if len(opened.Rows) != 1 || opened.Truncated || len(listed.Rows) != 1 ||
		opened.Rows[0]["row_id"] != listed.Rows[0]["row_id"] {
		t.Fatalf("one-winner state: opened=%#v listed=%#v", opened, listed)
	}
}
