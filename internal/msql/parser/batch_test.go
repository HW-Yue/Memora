package parser

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/ast"
)

func TestParseBatchStatementListGolden(t *testing.T) {
	t.Parallel()

	batch, err := ParseBatch("; SHOW DATABASES; START TRANSACTION; SELECT * FROM notes; COMMIT;;")
	if err != nil {
		t.Fatalf("ParseBatch() error = %v", err)
	}
	encoded, err := json.Marshal(batch)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	want := `{"version":"memora.msql.ast/v1","statements":[{"kind":"SHOW","show":{"object":"DATABASES"}},{"kind":"BEGIN","transaction":{"action":"BEGIN"}},{"kind":"SELECT","select":{"projections":[{"kind":"star"}],"from":{"parts":[{"value":"notes"}]}}},{"kind":"COMMIT","transaction":{"action":"COMMIT"}}]}`
	if string(encoded) != want {
		t.Fatalf("batch AST =\n%s\nwant =\n%s", encoded, want)
	}
}

func TestParseBatchRejectsEmptyInput(t *testing.T) {
	t.Parallel()

	_, err := ParseBatch(" ; /* empty */ ; -- still empty\n")
	var parseError *Error
	if !errors.As(err, &parseError) || parseError.Code != ErrorEmptyBatch || parseError.StatementIndex != -1 {
		t.Fatalf("ParseBatch() error = %#v", err)
	}
}

func TestParseBatchReportsFailedStatementIndex(t *testing.T) {
	t.Parallel()

	_, err := ParseBatch("SHOW DATABASES; SELECT * notes; DELETE FROM notes")
	var parseError *Error
	if !errors.As(err, &parseError) {
		t.Fatalf("ParseBatch() error = %v, want parser Error", err)
	}
	if parseError.Code != ErrorUnexpectedToken || parseError.StatementIndex != 1 || parseError.Span.Start.Column != 26 {
		t.Fatalf("ParseBatch() error = %#v", parseError)
	}
}

func TestParseBatchItemsRecoversAfterStatementError(t *testing.T) {
	t.Parallel()

	items, err := ParseBatchItems(`
		SELECT * FROM work.notes LIMIT 1;
		SELECT * work.notes WHERE title = ?;
		SELECT * FROM work.notes WHERE row_id = ? LIMIT 1
	`)
	if err != nil {
		t.Fatalf("ParseBatchItems() request error = %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("item count = %d, want 3: %#v", len(items), items)
	}
	if items[0].Statement == nil || items[0].Issue != nil {
		t.Fatalf("first item = %#v", items[0])
	}
	if items[1].Statement != nil || items[1].Issue == nil ||
		items[1].Issue.StatementIndex != 1 || items[1].Kind != "SELECT" {
		t.Fatalf("failed item = %#v", items[1])
	}
	if items[2].Statement == nil || items[2].Issue != nil {
		t.Fatalf("recovered item = %#v", items[2])
	}
	parameters := (ast.Document{Statement: *items[2].Statement}).Parameters()
	if len(parameters) != 1 || parameters[0].Ordinal != 2 {
		t.Fatalf("recovered parameters = %#v, want global ordinal 2", parameters)
	}
}
