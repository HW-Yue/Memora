package sqlstore_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/sqlstore"
)

// A Table's shape is the engine's: the only Columns a client may declare are the
// two the engine knows how to display and search — a title label and the document
// body. Anything else is the client inventing structure the product deliberately
// does not have, and it has to be refused when it is declared.
func shapeFailure(t *testing.T, h *harness, source string) (result.Code, string) {
	t.Helper()
	h.seq++
	envelope := h.session.ExecuteBatch(context.Background(), executor.BatchRequest{
		RequestID: "shape" + strings.Repeat("x", h.seq), Source: source,
		Statements: []executor.StatementInput{{
			Mutation: executor.MutationOptions{}, Authorization: authorization,
		}},
	})
	if envelope.Error != nil {
		return envelope.Error.Code, envelope.Error.Message
	}
	statement := envelope.Results[0]
	if statement.Error == nil {
		t.Fatalf("%s unexpectedly succeeded", source)
	}
	return statement.Error.Code, statement.Error.Message
}

func TestATableCannotDeclareAColumnOutsideTheShape(t *testing.T) {
	h := newHarness(t)
	h.run(`CREATE DATABASE work PURPOSE 'Work memory' SCOPE 'Projects'`, nil, executor.MutationOptions{})

	for _, test := range []struct {
		name   string
		source string
	}{
		{"a column with no role", `CREATE TABLE work.notes PURPOSE 'Notes' ROW SEMANTICS 'One fact' ` +
			`(title TEXT NOT NULL PURPOSE 'Title' ROLE title, company TEXT PURPOSE 'Company')`},
		{"a status column", `CREATE TABLE work.notes PURPOSE 'Notes' ROW SEMANTICS 'One fact' ` +
			`(title TEXT NOT NULL PURPOSE 'Title' ROLE title, status TEXT PURPOSE 'Status' ROLE status)`},
		{"a fact column", `CREATE TABLE work.notes PURPOSE 'Notes' ROW SEMANTICS 'One fact' ` +
			`(title TEXT NOT NULL PURPOSE 'Title' ROLE title, body TEXT PURPOSE 'Body' ROLE fact)`},
	} {
		code, message := shapeFailure(t, h, test.source)
		if code != result.CodeValidation {
			t.Fatalf("%s: code = %s, want %s", test.name, code, result.CodeValidation)
		}
		// The refusal has to say what the shape is, so the caller fixes it in one
		// try instead of guessing at the enum.
		for _, expected := range []string{"title", "summary", "semantic tree"} {
			if !strings.Contains(message, expected) {
				t.Fatalf("%s: the refusal must name the shape, got %q", test.name, message)
			}
		}
	}

	// The shape itself is accepted, whether both Columns are there or only the
	// title: the engine caps what may be declared, it does not force a title-only
	// Table to grow a body before it can exist.
	h.run(`CREATE TABLE work.notes PURPOSE 'Notes' ROW SEMANTICS 'One fact' `+
		`(title TEXT NOT NULL PURPOSE 'Title' ROLE title, summary TEXT(2500) NOT NULL PURPOSE 'Body' ROLE summary)`,
		nil, executor.MutationOptions{})
	h.run(`CREATE TABLE work.other PURPOSE 'Other' ROW SEMANTICS 'One fact' `+
		`(title TEXT NOT NULL PURPOSE 'Title' ROLE title)`, nil, executor.MutationOptions{})
}

func TestAddingAColumnOutsideTheShapeIsRefused(t *testing.T) {
	h := newHarness(t)
	h.seedTree()

	code, message := shapeFailure(t, h,
		`ALTER TABLE work.notes ADD COLUMN company TEXT NULL PURPOSE 'Company'`)
	if code != result.CodeValidation || !strings.Contains(message, "summary") {
		t.Fatalf("adding an invented column = %s (%q), want a validation refusal naming the shape", code, message)
	}
	// The engine's own second Column is still addable to a Table that lacks it.
	h.run(`ALTER TABLE work.notes ADD COLUMN summary TEXT(2500) NULL PURPOSE 'Body' ROLE summary`,
		nil, executor.MutationOptions{})
	columns := h.run(`SHOW COLUMNS FROM work.notes LIMIT 10`, nil, executor.MutationOptions{}).Rows
	if len(columns) != 2 {
		t.Fatalf("the canonical second Column must be addable: %v", columns)
	}
}

// The cap applies to what is *declared*, never to what is already stored. An
// Instance written before this rule keeps its Columns: it opens, its Rows are
// readable, and it keeps accepting writes. A guard on the load path would turn
// "you may not add a Column" into "this Database no longer opens".
func TestAnInstanceWithLegacyColumnsStillOpensAndWrites(t *testing.T) {
	h := newHarness(t)
	h.seedTree()
	rowID := h.insertAlongPath("storage engine", pathOf("architecture", "sqlite"))

	var body string
	if err := h.db.SQL().QueryRow(`SELECT body FROM mem_tables WHERE name = 'notes'`).Scan(&body); err != nil {
		t.Fatal(err)
	}
	stored := map[string]any{}
	if err := json.Unmarshal([]byte(body), &stored); err != nil {
		t.Fatal(err)
	}
	columns, _ := stored["columns"].([]any)
	stored["columns"] = append(columns,
		map[string]any{
			"column_id": "col_legacy_company", "name": "company", "aliases": []string{},
			"type": "TEXT", "max_characters": 120, "nullable": true, "purpose": "公司",
			"schema_version": 1, "created_at": "2026-09-13T00:00:00Z", "updated_at": "2026-09-13T00:00:00Z",
		},
		map[string]any{
			"column_id": "col_legacy_status", "name": "status", "aliases": []string{},
			"type": "TEXT", "max_characters": 40, "nullable": true, "purpose": "状态",
			"semantic_role": "status", "schema_version": 1,
			"created_at": "2026-09-13T00:00:00Z", "updated_at": "2026-09-13T00:00:00Z",
		})
	encoded, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.SQL().Exec(`UPDATE mem_tables SET body = ? WHERE name = 'notes'`, string(encoded)); err != nil {
		t.Fatal(err)
	}

	h.reopen()
	described := h.run(`DESCRIBE TABLE work.notes`, nil, executor.MutationOptions{})
	names := []string{}
	for _, column := range described.Rows[0]["columns"].([]any) {
		names = append(names, text(column.(map[string]any)["name"]))
	}
	if len(names) != 3 {
		t.Fatalf("a legacy Table keeps its Columns: %v", names)
	}
	if found := h.run(`SELECT row_id FROM work.notes WHERE row_id = :row LIMIT 1`,
		map[string]any{"row": rowID}, executor.MutationOptions{}); len(found.Rows) != 1 {
		t.Fatalf("a legacy Table stays readable: %v", found.Rows)
	}
	mutation := write("keep writing to a legacy Table")
	mutation.ExpectedRevision = 1
	h.run(`UPDATE work.notes SET title = 'storage engine, revised' WHERE row_id = :row`,
		map[string]any{"row": rowID}, mutation)
	if _, err := sqlstore.Open(h.path, sqlstore.Options{CheckInvariants: true}); err != nil {
		t.Fatalf("a legacy Instance must still open: %v", err)
	}
}
