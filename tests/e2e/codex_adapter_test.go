//go:build e2e

package e2e_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/codexadapter"
	"github.com/HW-Yue/Memora/internal/hostharness"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/row"
	nativekvstore "github.com/HW-Yue/Memora/internal/store/nativekv"
	"github.com/HW-Yue/Memora/internal/testkit"
)

func TestCodexAdapterCleanEnvironmentRunsCoreSkillJourney(t *testing.T) {
	t.Parallel()

	root := e2eRepositoryRoot(t)
	bundle, err := codexadapter.Build(filepath.Join(root, "skills", "memora"))
	if err != nil {
		t.Fatal(err)
	}
	if err := bundle.Install(t.TempDir()); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	database, err := nativekvstore.Open(filepath.Join(t.TempDir(), "codex-adapter.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	dictionary := catalog.New(database, catalog.Options{IDs: testkit.NewFakeIDs("db_work", "table_notes", "column_title")})
	if _, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{Name: "work", Purpose: "Work", Scope: "Reviewed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := dictionary.CreateTable(ctx, "work", catalog.TableDefinition{
		Name: "notes", Purpose: "Decisions", RowSemantics: "One durable decision",
		Columns: []catalog.ColumnDefinition{{Name: "title", Type: "TEXT(200)", Purpose: "Decision title"}},
	}); err != nil {
		t.Fatal(err)
	}
	rows := row.New(database, dictionary, row.Options{IDs: testkit.NewFakeIDs("one")})
	session := executor.NewBatchSession(ctx, dictionary, rows)
	defer session.Close()
	tool := hostharness.ToolFunc(func(ctx context.Context, call hostharness.Call) (hostharness.Response, error) {
		envelope := session.Execute(ctx, executor.BatchRequest{RequestID: call.ID, Source: call.MSQL, Statements: []executor.StatementInput{call.Input}})
		return hostharness.Response{Envelope: &envelope}, nil
	})
	zero, one, two := 0, 1, 2
	ok := true
	calls := []hostharness.Call{
		{ID: "discover", Command: "query", MSQL: "SHOW DATABASES"},
		{ID: "schema", Command: "query", MSQL: "DESCRIBE TABLE work.notes COMPACT"},
		{ID: "deduplicate", Command: "query", MSQL: "SELECT row_id FROM work.notes WHERE title = :title LIMIT 1", Input: executor.StatementInput{Parameters: executor.Parameters{Named: map[string]any{"title": "Route results are locators"}}}},
		{ID: "insert", Command: "exec", MSQL: "INSERT INTO work.notes (title) VALUES (:title)", Input: executor.StatementInput{
			Parameters: executor.Parameters{Named: map[string]any{"title": "Route results are locators"}},
			Mutation:   executor.MutationOptions{ExpectedSchemaVersion: 1, MaxAffectedRows: 1, Actor: "agent:codex", Source: "codex-turn-1", Reason: "stable user decision", IndexTerms: []string{"route", "locator"}, RouteLeafIDs: []string{}},
		}},
		{ID: "query", Command: "query", MSQL: "SELECT title, row_id, revision FROM work.notes WHERE row_id = :row LIMIT 1", Input: executor.StatementInput{Parameters: executor.Parameters{Named: map[string]any{"row": "row_one"}}}},
		{ID: "pre-revise", Command: "query", MSQL: "SELECT title, row_id, revision FROM work.notes WHERE row_id = :row LIMIT 1", Input: executor.StatementInput{Parameters: executor.Parameters{Named: map[string]any{"row": "row_one"}}}},
		{ID: "revise", Command: "exec", MSQL: "UPDATE work.notes SET title = :title WHERE row_id = :row", Input: executor.StatementInput{
			Parameters: executor.Parameters{Named: map[string]any{"row": "row_one", "title": "Route results are locators, never evidence"}},
			Mutation:   executor.MutationOptions{ExpectedSchemaVersion: 1, ExpectedRevision: 1, MaxAffectedRows: 1, Actor: "agent:codex", Source: "codex-turn-3", Reason: "user clarified conclusion", IndexTerms: []string{"route", "locator", "evidence"}, RouteLeafIDs: []string{}},
		}},
		{ID: "conflict-current", Command: "query", MSQL: "SELECT title, row_id, revision FROM work.notes WHERE row_id = :row LIMIT 1", Input: executor.StatementInput{Parameters: executor.Parameters{Named: map[string]any{"row": "row_one"}}}},
		{ID: "conflict-history", Command: "query", MSQL: "SHOW HISTORY FROM work.notes FOR ROW :row LIMIT 10", Input: executor.StatementInput{Parameters: executor.Parameters{Named: map[string]any{"row": "row_one"}}}},
	}
	turns := []hostharness.Turn{
		{User: "Remember that Route results are locators.", Actions: []hostharness.Action{
			{Call: calls[0], Expect: hostharness.Expectation{OK: &ok}},
			{Call: calls[1], Expect: hostharness.Expectation{OK: &ok}},
			{Call: calls[2], Expect: hostharness.Expectation{Rows: &zero}},
			{Call: calls[3], Expect: hostharness.Expectation{Revision: uint64Pointer(1)}},
		}, Reply: "Saved row_one revision 1 after discovery and duplicate checking.", ReplyExpect: hostharness.ReplyExpectation{Contains: []string{"row_one", "revision 1"}, MaxCharacters: 2000}},
		{User: "What did we decide about Route results?", Actions: []hostharness.Action{{Call: calls[4], Expect: hostharness.Expectation{Rows: &one}}}, Reply: "work.notes row_one revision 1 says Route results are locators.", ReplyExpect: hostharness.ReplyExpectation{Contains: []string{"work.notes", "row_one", "revision 1"}}},
		{User: "Clarify that they are never evidence.", Actions: []hostharness.Action{
			{Call: calls[5], Expect: hostharness.Expectation{Rows: &one}},
			{Call: calls[6], Expect: hostharness.Expectation{Revision: uint64Pointer(2)}},
		}, Reply: "Revised row_one to revision 2 and verified the clarification.", ReplyExpect: hostharness.ReplyExpectation{Contains: []string{"revision 2", "verified"}}},
		{User: "Another source says Route results are sufficient evidence.", Actions: []hostharness.Action{
			{Call: calls[7], Expect: hostharness.Expectation{Rows: &one}},
			{Call: calls[8], Expect: hostharness.Expectation{Rows: &two}},
		}, Reply: "Conflict: row_one revision 2 says never evidence; the new source says sufficient evidence. Which should I retain or rewrite?", ReplyExpect: hostharness.ReplyExpectation{Contains: []string{"Conflict", "row_one", "revision 2", "retain or rewrite"}}},
	}
	fixture := hostharness.Fixture{Version: hostharness.Version, Name: "codex-core-journey", Turns: turns, ExpectedToolSequence: calls, FinalAssertions: []hostharness.Action{{
		Call:   hostharness.Call{ID: "final", Command: "query", MSQL: "SELECT title, revision FROM work.notes WHERE row_id = :row LIMIT 1", Input: executor.StatementInput{Parameters: executor.Parameters{Named: map[string]any{"row": "row_one"}}}},
		Expect: hostharness.Expectation{Rows: &one, RowContains: map[string]any{"title": "Route results are locators, never evidence", "revision": uint64(2)}},
	}}}
	report, err := hostharness.New(tool).Replay(ctx, fixture)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Calls) != len(calls) || report.FinalAssertions != 1 {
		t.Fatalf("Codex journey report = %#v", report)
	}
}

func uint64Pointer(value uint64) *uint64 { return &value }
