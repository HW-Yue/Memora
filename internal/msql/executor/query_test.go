package executor_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/msql/parser"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
	sqlitestore "github.com/HW-Yue/Memora/internal/store/sqlite"
)

func TestQueryBindsNamedAndPositionalParametersWithoutInterpolation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	subject, rows, closeStore := queryFixture(t, ctx)
	defer closeStore()
	injection := "x' OR 1=1 --"
	first := insertQueryRow(t, ctx, rows, map[string]any{
		"title": "safe", "count": 2, "done": true, "at": "2026-07-30T10:00:00+08:00",
	})
	second := insertQueryRow(t, ctx, rows, map[string]any{
		"title": injection, "count": 7, "done": false,
	})

	output, err := query(ctx, subject,
		"SELECT row_id, title, count FROM work.notes WHERE title = :title AND revision = ? LIMIT :limit",
		executor.Parameters{Named: map[string]any{"title": injection, "limit": 2}, Positional: []any{int64(1)}},
	)
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if output.Truncated || len(output.Rows) != 1 || output.Rows[0]["row_id"] != second.ID ||
		output.Rows[0]["title"] != injection || output.Rows[0]["count"] != int64(7) {
		t.Fatalf("query output = %#v", output)
	}
	if len(output.Columns) != 3 || output.Columns[0].Name != "row_id" || output.Columns[1].Type != "TEXT" {
		t.Fatalf("query columns = %#v", output.Columns)
	}
	if output.Rows[0]["row_id"] == first.ID {
		t.Fatal("parameter text changed the predicate into an injection")
	}
	exact, err := query(ctx, subject,
		"SELECT title FROM work.notes WHERE row_id = :row_id LIMIT 1",
		executor.Parameters{Named: map[string]any{"row_id": second.ID}},
	)
	if err != nil || len(exact.Rows) != 1 || exact.Rows[0]["title"] != injection {
		t.Fatalf("exact row query = %#v, %v", exact, err)
	}
	missing, err := query(ctx, subject,
		"SELECT title FROM work.notes WHERE row_id = :row_id LIMIT 1",
		executor.Parameters{Named: map[string]any{"row_id": "row_missing"}},
	)
	if err != nil || len(missing.Rows) != 0 {
		t.Fatalf("missing exact row query = %#v, %v", missing, err)
	}
	all, err := rows.List(ctx, "work", "notes", 100)
	if err != nil || len(all) != 2 {
		t.Fatalf("rows after injection-shaped query = %#v, %v", all, err)
	}
}

func TestQueryEvaluatesTypedPredicatesAndStarProjection(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	subject, rows, closeStore := queryFixture(t, ctx)
	defer closeStore()
	first := insertQueryRow(t, ctx, rows, map[string]any{
		"title": "first", "count": 1, "done": false, "at": "2026-07-30T10:00:00+08:00",
	})
	second := insertQueryRow(t, ctx, rows, map[string]any{"title": "second", "count": 4, "done": true})

	output, err := query(ctx, subject,
		"SELECT * FROM work.notes WHERE done = TRUE AND count + 1 >= 5 AND commit_sequence = 2 LIMIT 1",
		executor.Parameters{},
	)
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if len(output.Rows) != 1 || output.Rows[0]["row_id"] != second.ID ||
		output.Rows[0]["revision"] != uint64(1) || output.Rows[0]["commit_sequence"] != uint64(2) ||
		output.Rows[0]["row_state"] != "live" {
		t.Fatalf("star output = %#v", output)
	}
	if len(output.Columns) != 9 {
		t.Fatalf("star columns = %#v", output.Columns)
	}

	timeOutput, err := query(ctx, subject,
		"SELECT title FROM work.notes WHERE \"at\" = :at LIMIT 1",
		executor.Parameters{Named: map[string]any{"at": "2026-07-30T10:00:00+08:00"}},
	)
	if err != nil {
		t.Fatalf("timestamp Query() error = %v", err)
	}
	if len(timeOutput.Rows) != 1 || timeOutput.Rows[0]["title"] != first.Values["title"] {
		t.Fatalf("timestamp match = %#v", timeOutput)
	}
}

func TestQueryRejectsUnboundedInvalidOrUnboundRequests(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	subject, _, closeStore := queryFixture(t, ctx)
	defer closeStore()

	tests := []struct {
		source     string
		parameters executor.Parameters
		code       result.Code
	}{
		{"SELECT title FROM work.notes", executor.Parameters{}, result.CodeValidation},
		{"SELECT missing FROM work.notes LIMIT 1", executor.Parameters{}, result.CodeValidation},
		{"SELECT title FROM missing.notes LIMIT 1", executor.Parameters{}, result.CodeNotFound},
		{"SELECT title FROM work.notes WHERE title = :title LIMIT 1", executor.Parameters{}, result.CodeValidation},
		{"SELECT title FROM work.notes WHERE revision = ? LIMIT 1", executor.Parameters{Positional: []any{}}, result.CodeValidation},
		{"SELECT title FROM work.notes LIMIT ?", executor.Parameters{Positional: []any{0}}, result.CodeValidation},
		{"SELECT title FROM work.notes LIMIT 1", executor.Parameters{Named: map[string]any{"unused": true}}, result.CodeValidation},
		{"SELECT title FROM work.notes WHERE count = :count LIMIT 1", executor.Parameters{Named: map[string]any{"count": "4"}}, result.CodeConstraint},
		{"UPDATE work.notes SET title = 'no'", executor.Parameters{}, result.CodeUnsupported},
	}
	for _, test := range tests {
		_, err := query(ctx, subject, test.source, test.parameters)
		assertExecutorCode(t, err, test.code)
	}
}

func query(ctx context.Context, subject *executor.Engine, source string, parameters executor.Parameters) (executor.Output, error) {
	document, err := parser.Parse(source)
	if err != nil {
		return executor.Output{}, err
	}
	return subject.Query(ctx, document.Statement, parameters)
}

func queryFixture(t *testing.T, ctx context.Context) (*executor.Engine, *row.Service, func()) {
	t.Helper()
	databaseStore, err := sqlitestore.Open(filepath.Join(t.TempDir(), "database.db"))
	if err != nil {
		t.Fatal(err)
	}
	dictionary := catalog.New(databaseStore, catalog.Options{
		IDs: &idSource{values: []string{"database", "table", "title", "count", "done", "at"}},
	})
	if _, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{
		Name: "work", Purpose: "Work", Scope: "Projects",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := dictionary.CreateTable(ctx, "work", catalog.TableDefinition{
		Name: "notes", Purpose: "Notes", RowSemantics: "One note",
		Columns: []catalog.ColumnDefinition{
			{Name: "title", Type: "TEXT(100)", Purpose: "Title"},
			{Name: "count", Type: "INTEGER", Nullable: true, Purpose: "Count"},
			{Name: "done", Type: "BOOLEAN", Nullable: true, Purpose: "Done"},
			{Name: "at", Type: "TIMESTAMP", Nullable: true, Purpose: "Time"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	rows := row.New(databaseStore, dictionary, row.Options{
		IDs: &idSource{values: []string{"first", "second", "third", "fourth"}},
	})
	return executor.New(dictionary, rows), rows, func() {
		if err := databaseStore.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	}
}

func insertQueryRow(t *testing.T, ctx context.Context, rows *row.Service, values map[string]any) row.Row {
	t.Helper()
	inserted, err := rows.Insert(ctx, "work", "notes", values, row.WriteOptions{ExpectedSchemaVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	return inserted
}

func assertExecutorCode(t *testing.T, err error, want result.Code) {
	t.Helper()
	var stable interface{ StableCode() string }
	if !errors.As(err, &stable) || stable.StableCode() != string(want) {
		t.Fatalf("error = %v, want stable code %q", err, want)
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
