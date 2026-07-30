package parser

import "testing"

func TestParseDatabaseConfigurationStatements(t *testing.T) {
	t.Parallel()
	tests := []struct {
		source     string
		kind       string
		parameters int
	}{
		{"SHOW CONFIGURATION", "SHOW", 0},
		{"SHOW CONFIGURATION HISTORY LIMIT :limit", "SHOW", 1},
		{
			"ALTER CONFIGURATION QUERY_BUDGETS SET " +
				"ROUTE_CHILDREN :routes, OPEN_LOCATORS :locators, SELECT_SCAN :scan, " +
				"SELECT_ROWS :rows, ROUTE_FRAME_NODES :frame",
			"ALTER_CONFIGURATION", 5,
		},
		{"RESTORE CONFIGURATION QUERY_BUDGETS TO REVISION :revision", "RESTORE_CONFIGURATION", 1},
	}
	for _, test := range tests {
		document, err := Parse(test.source)
		if err != nil {
			t.Fatalf("Parse(%q) error = %v", test.source, err)
		}
		if document.Statement.Kind != test.kind || len(document.Parameters()) != test.parameters {
			t.Fatalf("Parse(%q) = kind %q, parameters %d", test.source, document.Statement.Kind, len(document.Parameters()))
		}
	}
}

func TestParseDatabaseConfigurationRejectsPartialReplacement(t *testing.T) {
	t.Parallel()
	for _, source := range []string{
		"ALTER CONFIGURATION QUERY_BUDGETS SET SELECT_ROWS 3",
		"SHOW CONFIGURATION HISTORY",
		"RESTORE CONFIGURATION QUERY_BUDGETS REVISION 1",
	} {
		if _, err := Parse(source); err == nil {
			t.Fatalf("Parse(%q) succeeded", source)
		}
	}
}
