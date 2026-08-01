package parser

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/ast"
)

func TestParseCoreStatementsGolden(t *testing.T) {
	t.Parallel()

	sources := []string{
		"SHOW DATABASES",
		"DESCRIBE TABLE work.notes COMPACT",
		"CREATE TABLE notes (title TEXT NOT NULL, body TEXT(1200))",
		"SELECT row_id, title FROM work.notes WHERE revision >= :revision AND title != 'old' LIMIT ?",
		"INSERT INTO notes (title, body) VALUES (:title, :body), ('fallback', NULL)",
		"UPDATE notes SET title = :title, revision = revision + 1 WHERE row_id = ?",
		"DELETE FROM notes WHERE row_id = :row_id",
		"PLAN ROUTE MUTATION FOR TABLE work.notes USING :proposal",
	}
	var encoded strings.Builder
	for _, source := range sources {
		document, err := Parse(source)
		if err != nil {
			t.Fatalf("Parse(%q) error = %v", source, err)
		}
		line, err := json.Marshal(document)
		if err != nil {
			t.Fatalf("Marshal(%q) error = %v", source, err)
		}
		encoded.Write(line)
		encoded.WriteByte('\n')
	}
	want, err := os.ReadFile(filepath.Join("testdata", "core_statements.golden"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if encoded.String() != string(want) {
		t.Fatalf("AST golden mismatch\n--- got ---\n%s--- want ---\n%s", encoded.String(), want)
	}
}

func TestParseTracksParametersInSourceOrder(t *testing.T) {
	t.Parallel()

	document, err := Parse("SELECT * FROM notes WHERE owner = :owner AND revision > ? LIMIT :limit")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	parameters := document.Parameters()
	if len(parameters) != 3 || parameters[0].Name != "owner" || parameters[0].Ordinal != 1 || parameters[1].Style != "positional" || parameters[1].Ordinal != 2 || parameters[2].Name != "limit" || parameters[2].Ordinal != 3 {
		t.Fatalf("Parameters() = %#v", parameters)
	}
}

func TestParseHistoryStatementsAndParameters(t *testing.T) {
	t.Parallel()

	batch, err := ParseBatch(`
		SELECT title FROM work.notes AS OF REVISION :revision WHERE row_id = :row_id LIMIT 1;
		SHOW HISTORY FROM work.notes FOR ROW :row_id CURSOR :cursor LIMIT :limit;
		RESTORE work.notes ROW :row_id TO REVISION :target
	`)
	if err != nil {
		t.Fatalf("ParseBatch() error = %v", err)
	}
	if len(batch.Statements) != 3 {
		t.Fatalf("statements = %#v", batch.Statements)
	}
	selectStatement := batch.Statements[0].Select
	if selectStatement == nil || selectStatement.AsOf == nil ||
		selectStatement.AsOf.Kind != "REVISION" || selectStatement.AsOf.Value.Parameter == nil {
		t.Fatalf("AS OF AST = %#v", selectStatement)
	}
	show := batch.Statements[1].Show
	if show == nil || show.Object != "HISTORY" || show.Table == nil ||
		show.Row == nil || show.Cursor == nil || show.Limit == nil {
		t.Fatalf("SHOW HISTORY AST = %#v", show)
	}
	restore := batch.Statements[2].Restore
	if restore == nil || len(restore.Table.Parts) != 2 ||
		restore.Row == nil || restore.Revision == nil {
		t.Fatalf("RESTORE AST = %#v", restore)
	}
	parameters := (ast.Document{Statement: batch.Statements[0]}).Parameters()
	if len(parameters) != 2 || parameters[0].Name != "revision" || parameters[1].Name != "row_id" {
		t.Fatalf("SELECT history parameters = %#v", parameters)
	}
}

func TestParseReportsPreciseErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		source string
		code   ErrorCode
		line   int
		column int
	}{
		{"missing from", "SELECT * notes", ErrorUnexpectedToken, 1, 10},
		{"missing expression", "SELECT (1 + ) FROM notes", ErrorUnexpectedToken, 1, 13},
		{"unsupported", "DROP TABLE notes", ErrorUnsupportedStatement, 1, 1},
		{"second statement", "SHOW DATABASES; SHOW TABLES", ErrorUnexpectedToken, 1, 17},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := Parse(test.source)
			var parseError *Error
			if !errors.As(err, &parseError) {
				t.Fatalf("Parse() error = %v, want parser Error", err)
			}
			if parseError.Code != test.code || parseError.Span.Start.Line != test.line || parseError.Span.Start.Column != test.column {
				t.Fatalf("Parse() error = %#v", parseError)
			}
		})
	}
}
