package parser

import (
	"strings"
	"testing"
)

// A route layer is returned whole: its size is governed by
// `route_policy.branch_fanout`, not by a read-side page. `LIMIT` and `CURSOR`
// were the paging parameters, and a host that kept sending them would be reading
// a page while believing it held the layer — which is exactly the ambiguity the
// whole-layer answer removes. They are refused by name rather than by "unexpected
// token", because an agent that copies an older example deserves to be told what
// happened to it.
func TestRouteListingsRefuseTheRetiredPagingParameters(t *testing.T) {
	t.Parallel()
	for _, source := range []string{
		"SHOW ROUTES FROM TABLE work.notes AT ROOT LIMIT 5",
		"SHOW ROUTES FROM TABLE work.notes AT ROOT CURSOR :cursor LIMIT 5",
		"SHOW ROUTES UNDER :parent LIMIT 5",
		"SHOW ROUTES UNDER :parent CURSOR :cursor LIMIT 5",
	} {
		_, err := Parse(source)
		if err == nil {
			t.Fatalf("%s must be refused", source)
		}
		if !strings.Contains(err.Error(), "LIMIT") && !strings.Contains(err.Error(), "CURSOR") {
			t.Fatalf("%s: the refusal must name the retired parameter: %v", source, err)
		}
		if !strings.Contains(err.Error(), "whole layer") {
			t.Fatalf("%s: the refusal must say the layer comes whole: %v", source, err)
		}
	}
}

// Without them, both spellings of a layer are complete statements.
func TestRouteListingsTakeNoPagingParameters(t *testing.T) {
	t.Parallel()
	for _, source := range []string{
		"SHOW ROUTES FROM TABLE work.notes AT ROOT",
		"SHOW ROUTES UNDER :parent",
	} {
		document, err := Parse(source)
		if err != nil {
			t.Fatalf("%s: %v", source, err)
		}
		statement := document.Statement
		if statement.Kind != "SHOW" || statement.Show == nil || statement.Show.Object != "ROUTES" {
			t.Fatalf("%s parsed as %+v", source, statement)
		}
		if statement.Show.Limit != nil || statement.Show.Cursor != nil {
			t.Fatalf("%s must carry neither LIMIT nor CURSOR", source)
		}
	}
}
