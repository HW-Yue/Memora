package parser

import (
	"testing"
)

func TestParseRelationshipStatementsAndParameters(t *testing.T) {
	t.Parallel()

	tests := []struct {
		source     string
		kind       string
		parameters int
	}{
		{
			source: `
				RELATE work.notes ROW :source
				TO work.tasks ROW :target
				TYPE :type DESCRIPTION :description
			`,
			kind: "RELATE", parameters: 4,
		},
		{
			source: `
				SHOW RELATIONS FROM work.notes FOR ROW :row_id
				DIRECTION OUTGOING LIMIT :limit
			`,
			kind: "SHOW", parameters: 2,
		},
		{
			source: "UNRELATE :relation_id",
			kind:   "UNRELATE", parameters: 1,
		},
	}
	for _, test := range tests {
		document, err := Parse(test.source)
		if err != nil {
			t.Fatalf("Parse(%q) error = %v", test.source, err)
		}
		if document.Statement.Kind != test.kind {
			t.Fatalf("Parse(%q) kind = %q", test.source, document.Statement.Kind)
		}
		if got := len(document.Parameters()); got != test.parameters {
			t.Fatalf("Parse(%q) parameters = %d, want %d", test.source, got, test.parameters)
		}
	}
}

func TestParseRelationshipStatementsRejectsIncompleteSyntax(t *testing.T) {
	t.Parallel()

	for _, source := range []string{
		"RELATE work.notes ROW :source TO work.tasks ROW :target",
		"SHOW RELATIONS FROM work.notes FOR ROW :row DIRECTION SIDEWAYS LIMIT 10",
		"SHOW RELATIONS FROM work.notes FOR ROW :row DIRECTION OUTGOING",
		"UNRELATE",
	} {
		if _, err := Parse(source); err == nil {
			t.Fatalf("Parse(%q) succeeded", source)
		}
	}
}

func TestRelationshipClauseWordsRemainValidBusinessIdentifiers(t *testing.T) {
	t.Parallel()

	if _, err := Parse("SELECT type, description, direction FROM work.relations LIMIT 10"); err != nil {
		t.Fatalf("relationship clause words became reserved identifiers: %v", err)
	}
}
