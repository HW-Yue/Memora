package parser_test

import (
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/parser"
)

func TestParseRejectsRetiredPackageStatements(t *testing.T) {
	t.Parallel()

	for _, source := range []string{
		`PACK DATABASE work BY :author`,
		`OPEN PACKAGE :package READ ONLY`,
		`INSTALL PACKAGE ? TRUSTED`,
		`PACK DATABASE work`,
		`OPEN PACKAGE :package`,
		`INSTALL PACKAGE :package`,
	} {
		if _, err := parser.Parse(source); err == nil {
			t.Fatalf("Parse(%q) succeeded after package statements were retired", source)
		}
	}
}
