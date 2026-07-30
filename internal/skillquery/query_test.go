package skillquery_test

import (
	"context"
	"testing"

	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/skillquery"
)

func TestQueryNavigatesTableRouterOneLayerAtATimeBeforeSelect(t *testing.T) {
	t.Parallel()
	tool := &queueTool{responses: []result.Envelope{
		discoverySucceeded(),
		succeeded("tables", "SHOW", result.Row{"table_id": "table_notes", "name": "notes"}),
		succeeded("describe", "DESCRIBE", result.Row{"table_id": "table_notes", "name": "notes"}),
		succeeded("root", "SHOW_ROUTES", result.Row{"route_id": "route_arch", "kind": "branch"}),
		succeeded("under", "SHOW_ROUTES", result.Row{"route_id": "route_storage", "kind": "leaf"}),
		succeeded("open", "OPEN_ROUTE", result.Row{
			"database_id": "db_work", "table_id": "table_notes", "row_id": "row_one", "revision": uint64(2),
		}),
		succeeded("select", "SELECT", result.Row{"row_id": "row_one", "revision": uint64(2), "title": "Native authority"}),
	}}
	report, err := skillquery.New(tool).Run(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != skillquery.StatusComplete || !report.AnswerReady() || len(report.Evidence) != 1 {
		t.Fatalf("report = %#v", report)
	}
	want := []string{"SHOW_CONFIGURATION_DATABASES", "SHOW_TABLES", "DESCRIBE", "SHOW_ROUTES", "SHOW_ROUTES", "OPEN_ROUTE", "SELECT"}
	if got := statementKinds(report.Calls); !equalStrings(got, want) {
		t.Fatalf("statement kinds = %#v, want %#v", got, want)
	}
	for _, call := range report.Calls {
		if call.Statement == "MATCH" {
			t.Fatalf("hidden retrieval fallback: %#v", report.Calls)
		}
	}
}

func TestQueryStopsWhenChosenRouteIsNotInCurrentFrame(t *testing.T) {
	t.Parallel()
	tool := &queueTool{responses: []result.Envelope{
		discoverySucceeded(),
		succeeded("tables", "SHOW", result.Row{"table_id": "table_notes", "name": "notes"}),
		succeeded("describe", "DESCRIBE", result.Row{"table_id": "table_notes", "name": "notes"}),
		succeeded("root", "SHOW_ROUTES", result.Row{"route_id": "route_other", "kind": "leaf"}),
	}}
	report, err := skillquery.New(tool).Run(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != skillquery.StatusNoResults || report.AnswerReady() || len(report.Calls) != 4 {
		t.Fatalf("report = %#v", report)
	}
}

func TestTruncatedRouteFrameRemainsVisibleAfterSelect(t *testing.T) {
	t.Parallel()
	root := succeeded("root", "SHOW_ROUTES", result.Row{"route_id": "route_arch", "kind": "branch"})
	root.Truncated = true
	root.Results[0].Truncated = true
	tool := &queueTool{responses: []result.Envelope{
		discoverySucceeded(),
		succeeded("tables", "SHOW", result.Row{"table_id": "table_notes", "name": "notes"}),
		succeeded("describe", "DESCRIBE", result.Row{"table_id": "table_notes", "name": "notes"}),
		root,
		succeeded("under", "SHOW_ROUTES", result.Row{"route_id": "route_storage", "kind": "leaf"}),
		succeeded("open", "OPEN_ROUTE", result.Row{"database_id": "db_work", "table_id": "table_notes", "row_id": "row_one", "revision": uint64(2)}),
		succeeded("select", "SELECT", result.Row{"row_id": "row_one", "revision": uint64(2), "title": "bounded"}),
	}}
	report, err := skillquery.New(tool).Run(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != skillquery.StatusPartial || !report.Truncated || !report.AnswerReady() {
		t.Fatalf("report = %#v", report)
	}
}

func TestPermissionErrorDoesNotBroadenRouteLookup(t *testing.T) {
	t.Parallel()
	denied := result.FailedRequest("root", result.CodePermissionDenied, "table is outside scope", false)
	tool := &queueTool{responses: []result.Envelope{
		discoverySucceeded(),
		succeeded("tables", "SHOW", result.Row{"table_id": "table_notes", "name": "notes"}),
		succeeded("describe", "DESCRIBE", result.Row{"table_id": "table_notes", "name": "notes"}),
		denied,
	}}
	report, err := skillquery.New(tool).Run(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != skillquery.StatusAccessDenied || report.AnswerReady() || len(report.Calls) != 4 {
		t.Fatalf("report = %#v", report)
	}
}

func TestQueryUsesDatabaseConfigurationWithoutAddingAToolRoundTrip(t *testing.T) {
	t.Parallel()
	tool := &queueTool{responses: []result.Envelope{
		discoveryWithBudgets(1, 1, 1, 2),
		succeeded("tables", "SHOW", result.Row{"table_id": "table_notes", "name": "notes"}),
		succeeded("describe", "DESCRIBE", result.Row{"table_id": "table_notes", "name": "notes"}),
		succeeded("root", "SHOW_ROUTES", result.Row{"route_id": "route_arch", "kind": "branch"}),
		succeeded("under", "SHOW_ROUTES", result.Row{"route_id": "route_storage", "kind": "leaf"}),
		succeeded("open", "OPEN_ROUTE", result.Row{
			"database_id": "db_work", "table_id": "table_notes", "row_id": "row_one", "revision": uint64(2),
		}),
		succeeded("select", "SELECT", result.Row{"row_id": "row_one", "revision": uint64(2), "title": "bounded"}),
	}}
	report, err := skillquery.New(tool).Run(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Calls) != 7 || report.Calls[0].MSQL != "SHOW CONFIGURATION; SHOW DATABASES" {
		t.Fatalf("configuration discovery calls = %#v", report.Calls)
	}
	for _, call := range report.Calls {
		if call.Statement != "SHOW_ROUTES" && call.Statement != "OPEN_ROUTE" {
			continue
		}
		if got := call.Input.Parameters.Named["limit"]; got != int64(1) {
			t.Fatalf("%s configured limit = %#v", call.Statement, got)
		}
	}
}

func TestRequestRejectsInvalidRouteFramesOrBudgets(t *testing.T) {
	t.Parallel()
	for _, mutate := range []func(*skillquery.Request){
		func(request *skillquery.Request) { request.SelectedRoutes = nil },
		func(request *skillquery.Request) { request.SelectedRoutes = []string{"route_arch", "route_arch"} },
		func(request *skillquery.Request) { request.CandidateLimit = 25 },
		func(request *skillquery.Request) { request.SelectLimit = 11 },
	} {
		query := request()
		mutate(&query)
		if _, err := skillquery.New(&queueTool{}).Run(context.Background(), query); err == nil {
			t.Fatalf("Run(%#v) error = nil", query)
		}
	}
}

func request() skillquery.Request {
	return skillquery.Request{
		Database: "work", Table: "notes",
		SelectedRoutes: []string{"route_arch", "route_storage"},
		Projections:    []string{"title"}, CandidateLimit: 24, SelectLimit: 10,
	}
}

type queueTool struct{ responses []result.Envelope }

func (tool *queueTool) Invoke(_ context.Context, _ skillquery.Call) (result.Envelope, error) {
	if len(tool.responses) == 0 {
		panic("unexpected query tool call")
	}
	response := tool.responses[0]
	tool.responses = tool.responses[1:]
	return response, nil
}

func succeeded(requestID, kind string, rows ...result.Row) result.Envelope {
	statement := result.NewStatement(0, kind, kind)
	statement.Rows = rows
	return result.NewEnvelope(requestID, statement)
}

func discoverySucceeded() result.Envelope {
	return discoveryWithBudgets(12, 24, 10, 12)
}

func discoveryWithBudgets(routes, locators, rows, frame int) result.Envelope {
	configuration := result.NewStatement(0, "SHOW", "SHOW CONFIGURATION")
	configuration.Rows = []result.Row{{
		"route_children": routes, "open_locators": locators, "select_rows": rows, "route_frame_nodes": frame,
	}}
	databases := result.NewStatement(1, "SHOW", "SHOW DATABASES")
	databases.Rows = []result.Row{{"database_id": "db_work", "name": "work"}}
	return result.NewEnvelope("discovery", configuration, databases)
}

func statementKinds(calls []skillquery.Call) []string {
	values := make([]string, len(calls))
	for index, call := range calls {
		values[index] = call.Statement
	}
	return values
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
