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
			source:     "CREATE ROUTE ROOT FOR TABLE work.notes PURPOSE :purpose",
			kind:       "CREATE_ROUTE",
			parameters: 1,
		},
		{
			source:     "CREATE ROUTE UNDER :parent NAME :name KIND :kind PURPOSE :purpose SYNOPSIS :synopsis",
			kind:       "CREATE_ROUTE",
			parameters: 5,
		},
		{
			source:     "ALTER ROUTE :route RENAME TO :name",
			kind:       "RENAME_ROUTE",
			parameters: 2,
		},
		{
			source:     "ALTER ROUTE :route SET SYNOPSIS :synopsis",
			kind:       "UPDATE_ROUTE",
			parameters: 2,
		},
		{
			source:     "ALTER ROUTE :route SET ALIASES :aliases",
			kind:       "UPDATE_ROUTE",
			parameters: 2,
		},
		{
			source:     "DESCRIBE ROUTE :route",
			kind:       "DESCRIBE_ROUTE",
			parameters: 1,
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
			source:     "SHOW ROUTES FROM TABLE work.notes AT ROOT LIMIT :limit",
			kind:       "SHOW",
			parameters: 1,
		},
		{
			source:     "SHOW ROUTES FROM TABLE work.notes AT ROOT CURSOR :cursor LIMIT :limit",
			kind:       "SHOW",
			parameters: 2,
		},
		{
			source:     "SHOW ROUTES UNDER :parent LIMIT :limit",
			kind:       "SHOW",
			parameters: 2,
		},
		{
			source:     "SHOW ROUTE CANDIDATES FROM ALL TABLES USING LEXICAL :query LIMIT :limit BYTES :bytes",
			kind:       "SHOW",
			parameters: 3,
		},
		{
			source:     "SHOW LEXICAL LOCATIONS FROM ALL TABLES USING :query CURSOR :cursor LIMIT :limit BYTES :bytes",
			kind:       "SHOW",
			parameters: 4,
		},
		{
			source:     "OPEN ROUTE :route LIMIT :limit",
			kind:       "OPEN_ROUTE",
			parameters: 2,
		},
		{
			source:     "OPEN ROUTE :route CURSOR :cursor LIMIT :limit",
			kind:       "OPEN_ROUTE",
			parameters: 3,
		},
		{
			source:     "PLAN ROUTE MUTATION FOR TABLE work.notes USING :proposal",
			kind:       "PLAN_ROUTE_MUTATION",
			parameters: 1,
		},
		{
			source:     "APPLY ROUTE MUTATION PLAN :plan FOR TABLE work.notes",
			kind:       "APPLY_ROUTE_MUTATION",
			parameters: 1,
		},
		{
			source:     "PLAN SCHEMA CHANGE FOR TABLE work.notes USING :proposal",
			kind:       "PLAN_SCHEMA_CHANGE",
			parameters: 1,
		},
		{
			source:     "APPLY SCHEMA CHANGE PLAN :plan FOR TABLE work.notes",
			kind:       "APPLY_SCHEMA_CHANGE",
			parameters: 1,
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
		"CREATE ROUTE ROOT FOR DATABASE :database PURPOSE :purpose",
		"CREATE ROUTE ROOT FOR DATABASE :database",
		"CREATE ROUTE UNDER :parent NAME :name PURPOSE :purpose",
		"ALTER ROUTE :route RENAME",
		"DELETE ROUTE",
		"SHOW ROUTES FROM TABLE work.notes LIMIT 10",
		"SHOW ROUTE CANDIDATES FROM ALL TABLES USING LEXICAL :query LIMIT 8",
		"SHOW ROUTE CANDIDATES FROM ALL TABLES USING VECTOR :query LIMIT 8 BYTES 4096",
		"SHOW ROUTE CANDIDATES FROM ALL TABLES USING UNKNOWN :query LIMIT 8 BYTES 4096",
		"SHOW LEXICAL LOCATIONS FROM ALL TABLES USING :query LIMIT 8",
		"SHOW LEXICAL LOCATIONS FROM TABLE work.notes USING :query LIMIT 8 BYTES 4096",
		"SHOW LEXICAL LOCATIONS FROM ALL TABLES LIMIT 8 BYTES 4096",
		"OPEN ROUTE :route",
		"PLAN ROUTE MUTATION FOR TABLE work.notes",
		"PLAN ROUTE FOR TABLE work.notes USING :proposal",
		"PLAN SCHEMA CHANGE FOR TABLE work.notes",
		"APPLY SCHEMA CHANGE PLAN :plan FOR TABLE",
	} {
		if _, err := Parse(source); err == nil {
			t.Fatalf("Parse(%q) succeeded", source)
		}
	}
}

func TestParseFulltextLexicalLocationsFreezesIndependentProtocol(t *testing.T) {
	t.Parallel()
	document, err := Parse("SHOW LEXICAL LOCATIONS FROM ALL TABLES USING :query CURSOR :cursor LIMIT :limit BYTES :bytes")
	if err != nil {
		t.Fatal(err)
	}
	show := document.Statement.Show
	if show == nil || show.Object != "LEXICAL_LOCATIONS" || show.Query == nil || show.Cursor == nil ||
		show.Limit == nil || show.ByteLimit == nil || show.Predictor != "" {
		t.Fatalf("lexical location AST = %#v", document.Statement)
	}
}

func TestParseVectorRouteCandidatesRequiresSpaceAndAllBudgets(t *testing.T) {
	t.Parallel()
	document, err := Parse("SHOW ROUTE CANDIDATES FROM ALL TABLES USING VECTOR :query SPACE :space LIMIT :limit BYTES :bytes")
	if err != nil {
		t.Fatal(err)
	}
	show := document.Statement.Show
	if show == nil || show.Object != "ROUTE_CANDIDATES" || show.Predictor != "VECTOR" ||
		show.Query == nil || show.Space == nil || show.Limit == nil || show.ByteLimit == nil {
		t.Fatalf("vector Route candidate AST = %#v", document.Statement)
	}
	parameters := document.Parameters()
	if len(parameters) != 4 || parameters[0].Name != "query" || parameters[1].Name != "space" ||
		parameters[2].Name != "limit" || parameters[3].Name != "bytes" {
		t.Fatalf("vector Route candidate parameters = %#v", parameters)
	}
}

func TestParseLexicalRouteCandidatesRequiresAllBudgets(t *testing.T) {
	t.Parallel()
	document, err := Parse("SHOW ROUTE CANDIDATES FROM ALL TABLES USING LEXICAL :query LIMIT :limit BYTES :bytes")
	if err != nil {
		t.Fatal(err)
	}
	show := document.Statement.Show
	if show == nil || show.Object != "ROUTE_CANDIDATES" || show.Predictor != "LEXICAL" ||
		show.Query == nil || show.Limit == nil || show.ByteLimit == nil {
		t.Fatalf("Route candidate AST = %#v", document.Statement)
	}
	parameters := document.Parameters()
	if len(parameters) != 3 || parameters[0].Name != "query" ||
		parameters[1].Name != "limit" || parameters[2].Name != "bytes" {
		t.Fatalf("Route candidate parameters = %#v", parameters)
	}
}
