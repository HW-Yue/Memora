package parser_test

import (
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/parser"
)

func TestParseWikiExportStatement(t *testing.T) {
	t.Parallel()

	document, err := parser.Parse(`EXPORT WIKI TO :path PROFILE :profile`)
	if err != nil {
		t.Fatal(err)
	}
	if document.Statement.Kind != "EXPORT_WIKI" || document.Statement.Export == nil ||
		document.Statement.Export.Format != "WIKI" || document.Statement.Export.Path == nil ||
		document.Statement.Export.Profile == nil {
		t.Fatalf("EXPORT WIKI AST = %#v", document.Statement)
	}
	parameters := document.Parameters()
	if len(parameters) != 2 || parameters[0].Name != "path" || parameters[1].Name != "profile" {
		t.Fatalf("parameters = %#v", parameters)
	}
}

func TestWikiExportRequiresDestinationAndProfile(t *testing.T) {
	t.Parallel()

	for _, source := range []string{`EXPORT WIKI`, `EXPORT WIKI TO :path`, `EXPORT DATABASE TO :path PROFILE :profile`} {
		if _, err := parser.Parse(source); err == nil {
			t.Fatalf("Parse(%q) succeeded", source)
		}
	}
}
