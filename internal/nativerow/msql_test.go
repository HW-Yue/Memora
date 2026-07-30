package nativerow

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/msql/parser"
	"github.com/HW-Yue/Memora/internal/nativecatalog"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

func TestNativeInsertAndExactSelectMSQLSurviveReopen(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "database.memora")
	file, err := nativestore.Create(path, nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 30, 14, 0, 0, 123456789, time.UTC)
	dictionary := nativecatalog.NewService(nativecatalog.New(file), nativecatalog.ServiceOptions{
		IDs:   &testIDs{values: []string{"database", "table", "title", "optional"}},
		Clock: testClock{value: now},
	})
	rows := NewService(New(file), dictionary, ServiceOptions{IDs: &testIDs{values: []string{"first"}}, Clock: testClock{value: now}})
	engine := executor.New(dictionary, rows)
	executeMSQL(t, ctx, engine, "CREATE DATABASE work PURPOSE 'Work' SCOPE 'Projects'", executor.Parameters{}, executor.MutationOptions{})
	executeMSQL(t, ctx, engine, "CREATE TABLE work.notes PURPOSE 'Notes' ROW SEMANTICS 'One note' (title TEXT(20) NOT NULL PURPOSE 'Title', optional TEXT(20) NULL PURPOSE 'Optional')", executor.Parameters{}, executor.MutationOptions{})
	inserted := executeMSQL(t, ctx, engine,
		"INSERT INTO work.notes (title, optional) VALUES (:title, :optional)",
		executor.Parameters{Named: map[string]any{"title": "原生知识", "optional": nil}},
		executor.MutationOptions{ExpectedSchemaVersion: 1, MaxAffectedRows: 1},
	)
	rowID, ok := inserted.Rows[0]["row_id"].(string)
	if !ok || rowID != "row_first" {
		t.Fatalf("INSERT RowID = %#v", inserted.Rows)
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
	selected := executeMSQL(t, ctx, engine,
		"SELECT title, optional FROM work.notes WHERE row_id = :row_id LIMIT 1",
		executor.Parameters{Named: map[string]any{"row_id": rowID}}, executor.MutationOptions{},
	)
	if len(selected.Rows) != 1 || selected.Rows[0]["title"] != "原生知识" || selected.Rows[0]["optional"] != nil {
		t.Fatalf("SELECT rows = %#v", selected.Rows)
	}
	missing := executeMSQL(t, ctx, engine,
		"SELECT title FROM work.notes WHERE row_id = :row_id LIMIT 1",
		executor.Parameters{Named: map[string]any{"row_id": "row_missing"}}, executor.MutationOptions{},
	)
	if len(missing.Rows) != 0 {
		t.Fatalf("missing SELECT rows = %#v", missing.Rows)
	}
}

func executeMSQL(t *testing.T, ctx context.Context, engine *executor.Engine, source string, parameters executor.Parameters, options executor.MutationOptions) executor.Output {
	t.Helper()
	document, err := parser.Parse(source)
	if err != nil {
		t.Fatalf("Parse(%q) error = %v", source, err)
	}
	output, err := engine.Execute(ctx, document.Statement, parameters, options)
	if err != nil {
		t.Fatalf("Execute(%q) error = %v", source, err)
	}
	return output
}

type testIDs struct {
	values []string
	index  int
}

func (source *testIDs) Next() (string, error) {
	value := source.values[source.index]
	source.index++
	return value, nil
}

type testClock struct{ value time.Time }

func (clock testClock) Now() time.Time { return clock.value }
