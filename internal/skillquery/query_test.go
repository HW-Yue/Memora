package skillquery_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/skillquery"
	nativekvstore "github.com/HW-Yue/Memora/internal/store/nativekv"
	"github.com/HW-Yue/Memora/internal/testkit"
)

func TestQueryUsesDiscoveryMatchAndSelectBeforeEvidence(t *testing.T) {
	t.Parallel()

	tool := &queueTool{responses: []result.Envelope{
		succeeded("discover", "SHOW", result.Row{"database_id": "db_work", "name": "work"}),
		succeeded("describe", "DESCRIBE", result.Row{"table_id": "table_notes", "name": "notes"}),
		succeeded("match", "MATCH", result.Row{
			"database_id": "db_work", "table_id": "table_notes",
			"row_id": "row_one", "revision": uint64(2), "score": 1.0,
		}),
		succeeded("select-row_one", "SELECT", result.Row{
			"row_id": "row_one", "revision": uint64(2),
			"title": "Route results are locators only",
		}),
	}}
	report, err := skillquery.New(tool).Run(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := report.Status, skillquery.StatusComplete; got != want {
		t.Fatalf("status = %q, want %q", got, want)
	}
	if len(report.Evidence) != 1 ||
		report.Evidence[0].RowID != "row_one" ||
		report.Evidence[0].Values["title"] != "Route results are locators only" {
		t.Fatalf("evidence = %#v", report.Evidence)
	}
	if got := callCommands(report.Calls); !equalStrings(got, []string{"query", "query", "query", "query"}) {
		t.Fatalf("commands = %#v", got)
	}
	if got := statementKinds(report.Calls); !equalStrings(got, []string{"SHOW", "DESCRIBE", "MATCH", "SELECT"}) {
		t.Fatalf("statement kinds = %#v", got)
	}
	if report.CandidateCount != 1 || !report.AnswerReady() {
		t.Fatalf("report = %#v", report)
	}
}

func TestRouteWithNoLocatorsFallsBackToMatchAndStopsOnNoResults(t *testing.T) {
	t.Parallel()

	tool := &queueTool{responses: []result.Envelope{
		succeeded("discover", "SHOW", result.Row{"database_id": "db_work", "name": "work"}),
		succeeded("describe", "DESCRIBE", result.Row{"table_id": "table_notes", "name": "notes"}),
		succeeded("route", "OPEN_ROUTE"),
		succeeded("match", "MATCH"),
	}}
	query := request()
	query.RoutePath = "/architecture"
	report, err := skillquery.New(tool).Run(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != skillquery.StatusNoResults || report.AnswerReady() {
		t.Fatalf("report = %#v", report)
	}
	if got := statementKinds(report.Calls); !equalStrings(got, []string{"SHOW", "DESCRIBE", "OPEN_ROUTE", "MATCH"}) {
		t.Fatalf("statement kinds = %#v", got)
	}
}

func TestTruncatedCandidatesRemainVisibleAfterSelect(t *testing.T) {
	t.Parallel()

	match := succeeded("match", "MATCH", result.Row{
		"database_id": "db_work", "table_id": "table_notes",
		"row_id": "row_one", "revision": uint64(2),
	})
	match.Results[0].Truncated = true
	match.Truncated = true
	tool := &queueTool{responses: []result.Envelope{
		succeeded("discover", "SHOW", result.Row{"database_id": "db_work", "name": "work"}),
		succeeded("describe", "DESCRIBE", result.Row{"table_id": "table_notes", "name": "notes"}),
		match,
		succeeded("select-row_one", "SELECT", result.Row{
			"row_id": "row_one", "revision": uint64(2), "title": "bounded",
		}),
	}}
	report, err := skillquery.New(tool).Run(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != skillquery.StatusPartial || !report.Truncated || !report.AnswerReady() {
		t.Fatalf("report = %#v", report)
	}
}

func TestPermissionErrorProducesAccessLimitedOutcome(t *testing.T) {
	t.Parallel()

	denied := result.FailedRequest(
		"match",
		result.CodePermissionDenied,
		"table read is outside the host scope",
		false,
	)
	tool := &queueTool{responses: []result.Envelope{
		succeeded("discover", "SHOW", result.Row{"database_id": "db_work", "name": "work"}),
		succeeded("describe", "DESCRIBE", result.Row{"table_id": "table_notes", "name": "notes"}),
		denied,
	}}
	report, err := skillquery.New(tool).Run(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != skillquery.StatusAccessDenied || report.AnswerReady() {
		t.Fatalf("report = %#v", report)
	}
	if len(report.Warnings) != 1 || report.Warnings[0].Code != result.CodePermissionDenied {
		t.Fatalf("warnings = %#v", report.Warnings)
	}
}

func TestRequestRejectsUnboundedOrDuplicateTerms(t *testing.T) {
	t.Parallel()

	for _, mutate := range []func(*skillquery.Request){
		func(request *skillquery.Request) { request.QueryTerms = nil },
		func(request *skillquery.Request) { request.QueryTerms = []string{"route", "Route"} },
		func(request *skillquery.Request) { request.CandidateLimit = 25 },
		func(request *skillquery.Request) { request.SelectLimit = 11 },
	} {
		query := request()
		mutate(&query)
		if _, err := skillquery.New(&queueTool{}).Run(context.Background(), query); err == nil {
			t.Fatalf("Run(%#v) error = nil, want validation error", query)
		}
	}
}

func TestQueryStateMachineRunsAgainstRealMSQLSession(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	database, err := nativekvstore.Open(filepath.Join(t.TempDir(), "skill-query.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	dictionary := catalog.New(database, catalog.Options{
		IDs: testkit.NewFakeIDs("work", "notes", "title"),
	})
	if _, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{
		Name: "work", Purpose: "Work", Scope: "Reviewed decisions",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := dictionary.CreateTable(ctx, "work", catalog.TableDefinition{
		Name: "notes", Purpose: "Notes", RowSemantics: "One decision",
		Columns: []catalog.ColumnDefinition{{
			Name: "title", Type: "TEXT(200)", Purpose: "Decision title",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	rows := row.New(database, dictionary, row.Options{IDs: testkit.NewFakeIDs("one")})
	session := executor.NewBatchSession(ctx, dictionary, rows)
	defer func() { _ = session.Close() }()
	inserted := session.Execute(ctx, executor.BatchRequest{
		RequestID: "setup",
		Source:    "INSERT INTO work.notes (title) VALUES (:title)",
		Statements: []executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{
				"title": "Route results require SELECT",
			}},
			Mutation: executor.MutationOptions{
				ExpectedSchemaVersion: 1, MaxAffectedRows: 1,
				IndexTerms: []string{"routing", "locator", "select"},
				Actor:      "agent:test", Source: "test:F30", Reason: "query fixture",
			},
		}},
	})
	if !inserted.OK {
		t.Fatalf("setup INSERT = %#v", inserted)
	}
	tool := skillquery.ToolFunc(func(ctx context.Context, call skillquery.Call) (result.Envelope, error) {
		return session.Execute(ctx, executor.BatchRequest{
			RequestID: call.ID, Source: call.MSQL,
			Statements: []executor.StatementInput{call.Input},
		}), nil
	})
	report, err := skillquery.New(tool).Run(ctx, request())
	if err != nil {
		t.Fatal(err)
	}
	if !report.AnswerReady() || len(report.Evidence) != 1 ||
		report.Evidence[0].RowID != "row_one" ||
		report.Evidence[0].Values["title"] != "Route results require SELECT" {
		t.Fatalf("real MSQL report = %#v", report)
	}
}

func request() skillquery.Request {
	return skillquery.Request{
		Question:       "What is the routing result boundary?",
		Database:       "work",
		Table:          "notes",
		QueryTerms:     []string{"routing", "locator"},
		Projections:    []string{"title"},
		CandidateLimit: 24,
		SelectLimit:    10,
	}
}

type queueTool struct {
	responses []result.Envelope
	calls     []skillquery.Call
}

func (tool *queueTool) Invoke(_ context.Context, call skillquery.Call) (result.Envelope, error) {
	tool.calls = append(tool.calls, call)
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

func callCommands(calls []skillquery.Call) []string {
	values := make([]string, len(calls))
	for index, call := range calls {
		values[index] = call.Command
	}
	return values
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
