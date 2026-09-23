package parser

import (
	"strings"
	"testing"
)

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
			// A purpose is an amendable description, not identity frozen at
			// creation: the engine refuses a new Route that only repeats its
			// name, so it must also offer the statement that repairs an old one.
			source:     "ALTER ROUTE :route SET PURPOSE :purpose",
			kind:       "UPDATE_ROUTE",
			parameters: 2,
		},
		{
			source:     "DESCRIBE ROUTE :route",
			kind:       "DESCRIBE_ROUTE",
			parameters: 1,
		},
		{
			// A layer is answered whole, so the only parameter a route listing
			// can carry is the node it is under.
			source:     "SHOW ROUTES UNDER :parent",
			kind:       "SHOW",
			parameters: 1,
		},
		{
			source:     "SHOW ROUTES FROM TABLE work.notes AT ROOT",
			kind:       "SHOW",
			parameters: 0,
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
		"ALTER ROUTE :route SET ALIASES",
		"ALTER ROUTE :route SET PURPOSE",
		"ALTER ROUTE :route SET UNKNOWN :value",
		"ARCHIVE ROUTE :route REASON :reason",
		"UNARCHIVE ROUTE :route",
		"ARCHIVE ROW work.notes :row REASON :reason",
		"UNARCHIVE RELATION :relation",
		"ARCHIVE ROUTE",
		"UNARCHIVE ROUTE",
		"UNARCHIVE DATABASE",
		"SHOW ROUTES FROM TABLE work.notes",
		"SHOW ROUTE CANDIDATES FROM ALL TABLES USING LEXICAL :query LIMIT :limit BYTES :bytes",
		"SHOW ROUTE CANDIDATES FROM ALL TABLES USING LEXICAL :query LIMIT 8",
		"SHOW ROUTE CANDIDATES FROM ALL TABLES USING UNKNOWN :query LIMIT 8 BYTES 4096",
		"SHOW LEXICAL LOCATIONS FROM ALL TABLES USING :query CURSOR :cursor LIMIT :limit BYTES :bytes",
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

func TestParseRouteAliasReplacementPreservesArrayParameter(t *testing.T) {
	t.Parallel()
	document, err := Parse("ALTER ROUTE :route SET ALIASES :aliases")
	if err != nil {
		t.Fatal(err)
	}
	if document.Statement.UpdateRoute == nil || document.Statement.UpdateRoute.Route == nil ||
		document.Statement.UpdateRoute.Aliases == nil || document.Statement.UpdateRoute.Synopsis != nil {
		t.Fatalf("Route alias AST = %#v", document.Statement)
	}
}

func TestParseRoutePurposeAmendmentIsItsOwnClause(t *testing.T) {
	t.Parallel()
	document, err := Parse("ALTER ROUTE :route SET PURPOSE :purpose")
	if err != nil {
		t.Fatal(err)
	}
	update := document.Statement.UpdateRoute
	if update == nil || update.Route == nil || update.Purpose == nil ||
		update.Synopsis != nil || update.Aliases != nil {
		t.Fatalf("Route purpose AST = %#v", document.Statement)
	}
}

// A refusal that lists two of the three clauses sends the writer looking for a
// statement that exists.
func TestParseAlterRouteSetNamesEveryClauseItAccepts(t *testing.T) {
	t.Parallel()
	_, err := Parse("ALTER ROUTE :route SET UNKNOWN :value")
	if err == nil {
		t.Fatal("ALTER ROUTE SET UNKNOWN succeeded")
	}
	for _, clause := range []string{"SYNOPSIS", "ALIASES", "PURPOSE"} {
		if !strings.Contains(err.Error(), clause) {
			t.Fatalf("the refusal must name %s: %v", clause, err)
		}
	}
}
