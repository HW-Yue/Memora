package parser

import (
	"testing"
)

func TestRelationshipClauseWordsRemainValidBusinessIdentifiers(t *testing.T) {
	t.Parallel()

	if _, err := Parse("SELECT type, description, direction FROM work.relations LIMIT 10"); err != nil {
		t.Fatalf("relationship clause words became reserved identifiers: %v", err)
	}
}

func TestParseRejectsRetiredRelationshipStatements(t *testing.T) {
	t.Parallel()

	for _, source := range []string{
		`RELATE work.notes ROW :source TO work.tasks ROW :target TYPE :type DESCRIPTION :description`,
		`SHOW RELATIONS FROM work.notes FOR ROW :row_id DIRECTION OUTGOING LIMIT :limit`,
		`UNRELATE :relation_id`,
	} {
		if _, err := Parse(source); err == nil {
			t.Fatalf("Parse(%q) succeeded", source)
		}
	}
}
