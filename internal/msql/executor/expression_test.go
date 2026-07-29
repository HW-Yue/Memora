package executor

import (
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/msql/parser"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
)

func TestBindingsMapMixedParametersByOccurrence(t *testing.T) {
	t.Parallel()

	document, err := parser.Parse("SELECT title FROM work.notes WHERE title = :title AND revision = ? LIMIT :limit")
	if err != nil {
		t.Fatal(err)
	}
	bound, err := bindParameters(document.Statement, Parameters{
		Named: map[string]any{"title": "safe", "limit": 2}, Positional: []any{int64(7)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if bound[1] != "safe" || bound[2] != int64(7) || bound[3] != 2 {
		t.Fatalf("bindings = %#v", bound)
	}
	for _, supplied := range []Parameters{
		{},
		{Named: map[string]any{"title": "safe", "limit": 2}, Positional: []any{1, 2}},
		{Named: map[string]any{"title": "safe", "limit": 2, "unused": true}, Positional: []any{1}},
	} {
		if _, err := bindParameters(document.Statement, supplied); err == nil {
			t.Fatalf("bindParameters(%#v) succeeded", supplied)
		}
	}
}

func TestEvaluateUsesTypedRowValuesAndParameters(t *testing.T) {
	t.Parallel()

	document, err := parser.Parse("SELECT title FROM work.notes WHERE done = TRUE AND count + ? >= :minimum LIMIT 1")
	if err != nil {
		t.Fatal(err)
	}
	bound, err := bindParameters(document.Statement, Parameters{
		Named: map[string]any{"minimum": int64(5)}, Positional: []any{int64(1)},
	})
	if err != nil {
		t.Fatal(err)
	}
	table := catalog.Table{Columns: []catalog.Column{
		{ID: "col_title", Name: "title", Type: "TEXT"},
		{ID: "col_count", Name: "count", Type: "INTEGER"},
		{ID: "col_done", Name: "done", Type: "BOOLEAN"},
	}}
	current := row.Row{Revision: 3, Values: map[string]any{"title": "note", "count": int64(4), "done": true}}
	value, err := evaluate(document.Statement.Select.Where, table, &current, bound)
	if err != nil {
		t.Fatal(err)
	}
	if value != true {
		t.Fatalf("predicate = %#v", value)
	}

	document, err = parser.Parse("SELECT title FROM work.notes WHERE count / 0 > 1 LIMIT 1")
	if err != nil {
		t.Fatal(err)
	}
	_, err = evaluate(document.Statement.Select.Where, table, &current, bindings{})
	executorError, ok := err.(*Error)
	if !ok || executorError.Code != result.CodeConstraint {
		t.Fatalf("division error = %#v", err)
	}

	document, err = parser.Parse("SELECT title FROM work.notes WHERE count + 9223372036854775807 > 1 LIMIT 1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := evaluate(document.Statement.Select.Where, table, &current, bindings{}); err == nil {
		t.Fatal("overflow expression succeeded")
	}

	document, err = parser.Parse("SELECT title FROM work.notes WHERE title != NULL LIMIT 1")
	if err != nil {
		t.Fatal(err)
	}
	value, err = evaluate(document.Statement.Select.Where, table, &current, bindings{})
	if err != nil || value != false {
		t.Fatalf("NULL comparison = %#v, %v", value, err)
	}
}
