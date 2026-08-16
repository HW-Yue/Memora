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

func TestParseRoutePolicyStatements(t *testing.T) {
	t.Parallel()
	tests := []struct {
		source     string
		kind       string
		key        string
		parameters int
	}{
		{"SHOW CONFIGURATION ROUTE_POLICY", "SHOW", "ROUTE_POLICY", 0},
		{"SHOW CONFIGURATION ROUTE_POLICY HISTORY LIMIT :limit", "SHOW", "ROUTE_POLICY", 1},
		{"SHOW CONFIGURATION", "SHOW", "QUERY_BUDGETS", 0},
		{"ALTER CONFIGURATION ROUTE_POLICY SET BRANCH_FANOUT :fanout", "ALTER_CONFIGURATION", "ROUTE_POLICY", 1},
		{"RESTORE CONFIGURATION ROUTE_POLICY TO REVISION :revision", "RESTORE_CONFIGURATION", "ROUTE_POLICY", 1},
	}
	for _, test := range tests {
		document, err := Parse(test.source)
		if err != nil {
			t.Fatalf("Parse(%q) error = %v", test.source, err)
		}
		if document.Statement.Kind != test.kind || len(document.Parameters()) != test.parameters {
			t.Fatalf("Parse(%q) = kind %q, parameters %d", test.source, document.Statement.Kind, len(document.Parameters()))
		}
		key := ""
		if document.Statement.Show != nil {
			key = document.Statement.Show.Key
		} else if document.Statement.Configuration != nil {
			key = document.Statement.Configuration.Key
		}
		if key != test.key {
			t.Fatalf("Parse(%q) key = %q, want %q", test.source, key, test.key)
		}
	}
}

// branch_fanout is a structural limit, never a query budget field, and the two
// keys must not accept each other's fields.
func TestParseRoutePolicyRejectsQueryBudgetFields(t *testing.T) {
	t.Parallel()
	for _, source := range []string{
		"ALTER CONFIGURATION ROUTE_POLICY SET ROUTE_CHILDREN 12",
		"ALTER CONFIGURATION QUERY_BUDGETS SET BRANCH_FANOUT 12",
		"ALTER CONFIGURATION ROUTE_POLICY SET BRANCH_FANOUT 12, ROUTE_CHILDREN 12",
		"SHOW CONFIGURATION BRANCH_FANOUT",
	} {
		if _, err := Parse(source); err == nil {
			t.Fatalf("Parse(%q) succeeded", source)
		}
	}
}
