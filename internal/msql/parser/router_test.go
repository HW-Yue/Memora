package parser

import "testing"

func TestParseParameterizedRouterStatements(t *testing.T) {
	t.Parallel()

	tests := []struct {
		source     string
		kind       string
		parameters int
	}{
		{
			source:     "CREATE ROUTE ROOT FOR DATABASE :database PURPOSE :purpose",
			kind:       "CREATE_ROUTE",
			parameters: 2,
		},
		{
			source:     "CREATE ROUTE UNDER :parent NAME :name KIND :kind PURPOSE :purpose",
			kind:       "CREATE_ROUTE",
			parameters: 4,
		},
		{
			source:     "ALTER ROUTE :route RENAME TO :name",
			kind:       "RENAME_ROUTE",
			parameters: 2,
		},
		{
			source:     "DELETE ROUTE :route",
			kind:       "DELETE_ROUTE",
			parameters: 1,
		},
		{
			source:     "SHOW ROUTES UNDER :parent CURSOR :cursor LIMIT :limit",
			kind:       "SHOW",
			parameters: 3,
		},
		{
			source:     "SHOW ROUTES FROM DATABASE :database AT :path CURSOR :cursor LIMIT :limit",
			kind:       "SHOW",
			parameters: 4,
		},
		{
			source:     "OPEN ROUTE :route LIMIT :limit",
			kind:       "OPEN_ROUTE",
			parameters: 2,
		},
		{
			source:     "OPEN ROUTE FROM DATABASE :database AT :path LIMIT :limit",
			kind:       "OPEN_ROUTE",
			parameters: 3,
		},
	}
	for _, test := range tests {
		document, err := Parse(test.source)
		if err != nil {
			t.Fatalf("Parse(%q) error = %v", test.source, err)
		}
		if document.Statement.Kind != test.kind {
			t.Fatalf("Parse(%q) kind = %q, want %q", test.source, document.Statement.Kind, test.kind)
		}
		if got := len(document.Parameters()); got != test.parameters {
			t.Fatalf("Parse(%q) parameters = %d, want %d", test.source, got, test.parameters)
		}
	}
}

func TestParseRouterStatementsRejectsIncompleteSyntax(t *testing.T) {
	t.Parallel()

	for _, source := range []string{
		"CREATE ROUTE ROOT FOR DATABASE :database",
		"CREATE ROUTE UNDER :parent NAME :name PURPOSE :purpose",
		"ALTER ROUTE :route RENAME",
		"DELETE ROUTE",
		"SHOW ROUTES UNDER :parent LIMIT 10",
		"SHOW ROUTES FROM DATABASE :database AT :path LIMIT 10",
		"OPEN ROUTE :route",
		"OPEN ROUTE FROM DATABASE :database AT :path",
	} {
		if _, err := Parse(source); err == nil {
			t.Fatalf("Parse(%q) succeeded", source)
		}
	}
}
