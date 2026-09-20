package parser_test

import (
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/parser"
)

func TestParseRejectsRetiredWikiExportStatement(t *testing.T) {
	t.Parallel()

	for _, source := range []string{
		`EXPORT WIKI TO :path PROFILE :profile`,
		`EXPORT WIKI`,
		`EXPORT WIKI TO :path`,
		`EXPORT DATABASE TO :path PROFILE :profile`,
	} {
		if _, err := parser.Parse(source); err == nil {
			t.Fatalf("Parse(%q) succeeded after Wiki export was retired", source)
		}
	}
}
