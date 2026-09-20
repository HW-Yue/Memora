package parser

import "testing"

func TestParseRejectsRetiredLexicalRebuild(t *testing.T) {
	for _, source := range []string{
		"REBUILD LEXICAL INDEX",
		"REBUILD INDEX",
		"REBUILD FULLTEXT INDEX",
		"REBUILD LEXICAL INDEX work",
		"REBUILD LEXICAL INDEX FORCE",
	} {
		if _, err := Parse(source); err == nil {
			t.Fatalf("Parse(%q) succeeded", source)
		}
	}
}
