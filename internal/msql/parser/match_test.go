package parser

import "testing"

func TestParseParameterizedMatch(t *testing.T) {
	t.Parallel()
	document, err := Parse("MATCH work.notes QUERY :query TERMS :terms LIMIT :limit")
	if err != nil {
		t.Fatal(err)
	}
	if document.Statement.Match == nil || document.Statement.Kind != "MATCH" ||
		len(document.Parameters()) != 3 {
		t.Fatalf("MATCH AST = %#v", document)
	}
	for _, source := range []string{
		"MATCH work.notes TERMS :terms LIMIT 10",
		"MATCH work.notes QUERY :query LIMIT 10",
		"MATCH work.notes QUERY :query TERMS :terms",
	} {
		if _, err := Parse(source); err == nil {
			t.Fatalf("Parse(%q) succeeded", source)
		}
	}
}
