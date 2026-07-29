package executor_test

import (
	"context"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/msql/parser"
	"github.com/HW-Yue/Memora/internal/result"
)

func TestExecuteParameterizedInsertTreatsInjectionTextAsData(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	subject, rows, closeStore := queryFixture(t, ctx)
	defer closeStore()
	injection := "note'); DELETE FROM work.notes; --"
	output, err := execute(ctx, subject,
		"INSERT INTO work.notes (title, count, done) VALUES (:title, ?, :done)",
		executor.Parameters{
			Named:      map[string]any{"title": injection, "done": false},
			Positional: []any{int64(7)},
		},
		executor.MutationOptions{ExpectedSchemaVersion: 1, MaxAffectedRows: 1},
	)
	if err != nil {
		t.Fatalf("Execute(INSERT) error = %v", err)
	}
	if output.AffectedRows != 1 || output.Revision == nil || *output.Revision != 1 {
		t.Fatalf("insert output = %#v", output)
	}
	stored, err := rows.List(ctx, "work", "notes", 100)
	if err != nil || len(stored) != 1 || stored[0].Values["title"] != injection {
		t.Fatalf("stored injection-shaped value = %#v, %v", stored, err)
	}

	_, err = execute(ctx, subject,
		"INSERT INTO work.notes (title) VALUES ('one'), ('two')",
		executor.Parameters{},
		executor.MutationOptions{ExpectedSchemaVersion: 1, MaxAffectedRows: 1},
	)
	assertExecutorCode(t, err, result.CodeConstraint)
	stored, err = rows.List(ctx, "work", "notes", 100)
	if err != nil || len(stored) != 1 {
		t.Fatalf("failed multi-row insert changed rows = %#v, %v", stored, err)
	}
}

func TestExecuteParameterizedUpdateAndDeleteUseExpectedRevision(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	subject, rows, closeStore := queryFixture(t, ctx)
	defer closeStore()
	first := insertQueryRow(t, ctx, rows, map[string]any{"title": "first", "count": 1, "done": false})
	second := insertQueryRow(t, ctx, rows, map[string]any{"title": "second", "count": 2, "done": false})

	updated, err := execute(ctx, subject,
		"UPDATE work.notes SET title = :title, count = count + :delta WHERE row_id = :row_id",
		executor.Parameters{Named: map[string]any{
			"title": "first revised", "delta": int64(3), "row_id": first.ID,
		}},
		executor.MutationOptions{ExpectedSchemaVersion: 1, ExpectedRevision: 1, MaxAffectedRows: 1},
	)
	if err != nil {
		t.Fatalf("Execute(UPDATE) error = %v", err)
	}
	if updated.AffectedRows != 1 || updated.Revision == nil || *updated.Revision != 2 {
		t.Fatalf("update output = %#v", updated)
	}
	current, err := rows.Get(ctx, "work", "notes", first.ID)
	if err != nil || current.Values["title"] != "first revised" || current.Values["count"] != int64(4) {
		t.Fatalf("updated row = %#v, %v", current, err)
	}

	_, err = execute(ctx, subject,
		"UPDATE work.notes SET title = 'stale' WHERE row_id = :row_id",
		executor.Parameters{Named: map[string]any{"row_id": first.ID}},
		executor.MutationOptions{ExpectedSchemaVersion: 1, ExpectedRevision: 1, MaxAffectedRows: 1},
	)
	assertExecutorCode(t, err, result.CodeRevisionConflict)

	deleted, err := execute(ctx, subject,
		"DELETE FROM work.notes WHERE row_id = ?",
		executor.Parameters{Positional: []any{second.ID}},
		executor.MutationOptions{ExpectedSchemaVersion: 1, ExpectedRevision: 1, MaxAffectedRows: 1},
	)
	if err != nil {
		t.Fatalf("Execute(DELETE) error = %v", err)
	}
	if deleted.AffectedRows != 1 || deleted.Revision == nil || *deleted.Revision != 2 {
		t.Fatalf("delete output = %#v", deleted)
	}
	_, err = rows.Get(ctx, "work", "notes", second.ID)
	assertExecutorCode(t, err, result.CodeNotFound)
}

func TestExecuteChecksAffectedBudgetBeforeAnyMutation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	subject, rows, closeStore := queryFixture(t, ctx)
	defer closeStore()
	first := insertQueryRow(t, ctx, rows, map[string]any{"title": "first", "done": false})
	second := insertQueryRow(t, ctx, rows, map[string]any{"title": "second", "done": false})

	_, err := execute(ctx, subject,
		"UPDATE work.notes SET done = TRUE WHERE done = FALSE",
		executor.Parameters{},
		executor.MutationOptions{ExpectedSchemaVersion: 1, ExpectedRevision: 1, MaxAffectedRows: 1},
	)
	assertExecutorCode(t, err, result.CodeConstraint)
	for _, rowID := range []string{first.ID, second.ID} {
		current, getErr := rows.Get(ctx, "work", "notes", rowID)
		if getErr != nil || current.Revision != 1 || current.Values["done"] != false {
			t.Fatalf("budget failure mutated %s: %#v, %v", rowID, current, getErr)
		}
	}

	_, err = execute(ctx, subject,
		"UPDATE work.notes SET done = TRUE WHERE done = FALSE",
		executor.Parameters{},
		executor.MutationOptions{ExpectedSchemaVersion: 1, ExpectedRevision: 1, MaxAffectedRows: 2},
	)
	assertExecutorCode(t, err, result.CodeValidation)

	_, err = execute(ctx, subject,
		"DELETE FROM work.notes WHERE row_id = :row_id",
		executor.Parameters{Named: map[string]any{"row_id": first.ID}},
		executor.MutationOptions{ExpectedSchemaVersion: 1, ExpectedRevision: 1},
	)
	assertExecutorCode(t, err, result.CodeValidation)
}

func TestExecuteBindsAssignmentsBeforeLookingForRows(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	subject, _, closeStore := queryFixture(t, ctx)
	defer closeStore()
	for _, test := range []struct {
		source     string
		parameters executor.Parameters
		code       result.Code
	}{
		{
			"UPDATE work.notes SET missing = 1 WHERE row_id = 'row_missing'",
			executor.Parameters{}, result.CodeValidation,
		},
		{
			"UPDATE work.notes SET count = :count WHERE row_id = 'row_missing'",
			executor.Parameters{Named: map[string]any{"count": "not an integer"}}, result.CodeConstraint,
		},
	} {
		_, err := execute(ctx, subject, test.source, test.parameters, executor.MutationOptions{
			ExpectedSchemaVersion: 1, ExpectedRevision: 1, MaxAffectedRows: 1,
		})
		assertExecutorCode(t, err, test.code)
	}
}

func execute(ctx context.Context, subject *executor.Engine, source string, parameters executor.Parameters, options executor.MutationOptions) (executor.Output, error) {
	document, err := parser.Parse(source)
	if err != nil {
		return executor.Output{}, err
	}
	return subject.Execute(ctx, document.Statement, parameters, options)
}
