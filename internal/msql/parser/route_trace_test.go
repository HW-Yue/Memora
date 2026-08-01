package parser

import "testing"

func TestParseRouteTraceReads(t *testing.T) {
	t.Parallel()
	timeline, err := Parse("SHOW ROUTE TRACES IN DATABASE work AFTER TRACE_SEQUENCE :after CURSOR :cursor LIMIT :limit")
	if err != nil {
		t.Fatalf("Parse(timeline) error = %v", err)
	}
	show := timeline.Statement.Show
	if show == nil || show.Object != "ROUTE_TRACES" || show.Database == nil || show.After == nil ||
		show.Cursor == nil || show.Limit == nil {
		t.Fatalf("timeline AST = %#v", show)
	}
	detail, err := Parse("SHOW ROUTE TRACE :trace IN DATABASE work CURSOR :cursor LIMIT 32")
	if err != nil {
		t.Fatalf("Parse(detail) error = %v", err)
	}
	show = detail.Statement.Show
	if show == nil || show.Object != "ROUTE_TRACE" || show.Trace == nil || show.Database == nil ||
		show.Cursor == nil || show.Limit == nil {
		t.Fatalf("detail AST = %#v", show)
	}
}
