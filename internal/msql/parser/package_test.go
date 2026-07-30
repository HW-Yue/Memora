package parser_test

import (
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/parser"
)

func TestParseDatabasePackageManagementStatements(t *testing.T) {
	t.Parallel()

	packed, err := parser.Parse(`PACK DATABASE work BY :author`)
	if err != nil {
		t.Fatal(err)
	}
	if packed.Statement.Kind != "PACK_DATABASE" || packed.Statement.Package == nil ||
		packed.Statement.Package.Action != "PACK" || len(packed.Statement.Package.Database.Parts) != 1 ||
		packed.Statement.Package.Database.Parts[0].Value != "work" || packed.Statement.Package.Author == nil {
		t.Fatalf("PACK AST = %#v", packed.Statement)
	}
	if parameters := packed.Parameters(); len(parameters) != 1 || parameters[0].Name != "author" {
		t.Fatalf("PACK parameters = %#v", parameters)
	}

	opened, err := parser.Parse(`OPEN PACKAGE :package READ ONLY`)
	if err != nil {
		t.Fatal(err)
	}
	if opened.Statement.Kind != "OPEN_PACKAGE" || opened.Statement.Package == nil ||
		opened.Statement.Package.Action != "OPEN" || !opened.Statement.Package.ReadOnly ||
		opened.Statement.Package.Value == nil {
		t.Fatalf("OPEN AST = %#v", opened.Statement)
	}

	installed, err := parser.Parse(`INSTALL PACKAGE ? TRUSTED`)
	if err != nil {
		t.Fatal(err)
	}
	if installed.Statement.Kind != "INSTALL_PACKAGE" || installed.Statement.Package == nil ||
		installed.Statement.Package.Action != "INSTALL" || !installed.Statement.Package.Trusted {
		t.Fatalf("INSTALL AST = %#v", installed.Statement)
	}
}

func TestPackageManagementRequiresExplicitSafetyClauses(t *testing.T) {
	t.Parallel()

	for _, source := range []string{
		`PACK DATABASE work`,
		`OPEN PACKAGE :package`,
		`INSTALL PACKAGE :package`,
	} {
		if _, err := parser.Parse(source); err == nil {
			t.Fatalf("Parse(%q) succeeded without explicit safety clause", source)
		}
	}
}
