package parser

import (
	"encoding/json"
	"errors"
	"testing"
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
