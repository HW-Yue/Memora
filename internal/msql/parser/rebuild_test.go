package parser

import "testing"

func TestParseRebuildLexicalIndex(t *testing.T) {
	document, err := Parse("REBUILD LEXICAL INDEX")
	if err != nil {
		t.Fatal(err)
	}
	if document.Statement.Kind != "REBUILD_LEXICAL_INDEX" || document.Statement.Rebuild == nil ||
		document.Statement.Rebuild.Object != "LEXICAL_INDEX" || len(document.Parameters()) != 0 {
		t.Fatalf("REBUILD AST = %#v", document.Statement)
	}
}

func TestParseRebuildLexicalIndexRejectsExpandedSurface(t *testing.T) {
	for _, source := range []string{
		"REBUILD INDEX", "REBUILD FULLTEXT INDEX", "REBUILD LEXICAL INDEX work", "REBUILD LEXICAL INDEX FORCE",
	} {
		if _, err := Parse(source); err == nil {
			t.Fatalf("Parse(%q) succeeded", source)
		}
	}
}
