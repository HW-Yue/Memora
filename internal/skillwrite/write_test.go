package skillwrite_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/msql/parser"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/skillwrite"
	nativekvstore "github.com/HW-Yue/Memora/internal/store/nativekv"
	"github.com/HW-Yue/Memora/internal/testkit"
)

func TestEveryMutationDecisionQueriesBeforeWritingAndReturnsCompactReceipt(t *testing.T) {
	t.Parallel()

	for _, decision := range []skillwrite.Decision{
		skillwrite.DecisionIgnore,
		skillwrite.DecisionInsert,
		skillwrite.DecisionRevise,
		skillwrite.DecisionMerge,
		skillwrite.DecisionSplit,
		skillwrite.DecisionMove,
		skillwrite.DecisionRelate,
	} {
		decision := decision
		t.Run(string(decision), func(t *testing.T) {
			t.Parallel()
			plan := mutationPlan(decision)
			tool := &planTool{}
			report, err := skillwrite.New(tool).Run(context.Background(), plan)
			if err != nil {
				t.Fatal(err)
			}
			if len(report.Calls) == 0 || report.Calls[0].Phase != skillwrite.PhasePreflight {
				t.Fatalf("calls = %#v, want preflight first", report.Calls)
			}
			for _, call := range report.Calls {
				for _, input := range call.Request.Statements {
					if input.Authorization.Actor != plan.Actor ||
						len(input.Authorization.AuthorizedDatabases) != 1 ||
						input.Authorization.AuthorizedDatabases[0] != "work" {
						t.Fatalf("call lost plan Authorization: %#v", call)
					}
				}
			}
			if decision == skillwrite.DecisionIgnore {
				if report.Receipt.Status != skillwrite.ReceiptIgnored || report.Receipt.Ignored != 1 {
					t.Fatalf("IGNORE receipt = %#v", report.Receipt)
				}
				if containsPhase(report.Calls, skillwrite.PhaseMutation) {
					t.Fatalf("IGNORE calls = %#v, must not mutate", report.Calls)
				}
			} else {
				if !containsPhase(report.Calls, skillwrite.PhaseMutation) ||
					!containsPhase(report.Calls, skillwrite.PhaseVerify) ||
					!report.Receipt.Verified || report.Receipt.Status != skillwrite.ReceiptCommitted {
					t.Fatalf("%s report = %#v", decision, report)
				}
			}
			encoded, err := json.Marshal(report.Receipt)
			if err != nil {
				t.Fatal(err)
			}
			if len(encoded) > 2_000 {
				t.Fatalf("receipt has %d bytes", len(encoded))
			}
		})
	}
}

func TestMergeAndSplitUseOneExplicitShortTransaction(t *testing.T) {
	t.Parallel()

	for _, decision := range []skillwrite.Decision{skillwrite.DecisionMerge, skillwrite.DecisionSplit} {
		tool := &planTool{}
		if _, err := skillwrite.New(tool).Run(context.Background(), mutationPlan(decision)); err != nil {
			t.Fatal(err)
		}
		mutation := callForPhase(t, tool.calls, skillwrite.PhaseMutation)
		if !strings.HasPrefix(mutation.Request.Source, "BEGIN;") ||
			!strings.HasSuffix(mutation.Request.Source, ";COMMIT") {
			t.Fatalf("%s mutation source = %q", decision, mutation.Request.Source)
		}
		if len(mutation.Request.Statements) != len(mutationPlan(decision).Steps)+2 {
			t.Fatalf("%s statement inputs = %d", decision, len(mutation.Request.Statements))
		}
	}
}

func TestMutationPolicyRejectsMissingPreflightOrSemanticSnapshots(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*skillwrite.Plan)
	}{
		{"missing preflight", func(plan *skillwrite.Plan) { plan.Preflight = nil }},
		{"missing index snapshot", func(plan *skillwrite.Plan) { plan.Steps[0].Input.Mutation.IndexTerms = nil }},
		{"missing route snapshot", func(plan *skillwrite.Plan) { plan.Steps[0].Input.Mutation.RouteLeafIDs = nil }},
		{"missing provenance", func(plan *skillwrite.Plan) { plan.Steps[0].Input.Mutation.Reason = "" }},
		{"too many steps", func(plan *skillwrite.Plan) {
			for len(plan.Steps) <= 8 {
				plan.Steps = append(plan.Steps, plan.Steps[0])
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := mutationPlan(skillwrite.DecisionRevise)
			test.mutate(&plan)
			if _, err := skillwrite.New(&planTool{}).Run(context.Background(), plan); err == nil {
				t.Fatal("Run() error = nil, want Policy rejection")
			}
		})
	}
}

func TestReviseCommitsRowAndAgentIndexSnapshotBeforeVerification(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	database, err := nativekvstore.Open(filepath.Join(t.TempDir(), "skill-write.db"))
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
		RequestID: "setup", Source: "INSERT INTO work.notes (title) VALUES (:title)",
		Statements: []executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"title": "initial"}},
			Mutation: executor.MutationOptions{
				ExpectedSchemaVersion: 1, MaxAffectedRows: 1,
				Actor: "agent:setup", Source: "test:setup", Reason: "fixture",
				IndexTerms: []string{"old-term"}, RouteLeafIDs: []string{},
			},
		}},
	})
	if !inserted.OK {
		t.Fatalf("setup INSERT = %#v", inserted)
	}

	plan := mutationPlan(skillwrite.DecisionRevise)
	plan.Preflight = []skillwrite.Check{{
		ID:         "read-current",
		MSQL:       "SELECT title, row_id, revision FROM work.notes WHERE row_id = :row LIMIT 1",
		Input:      executor.StatementInput{Parameters: executor.Parameters{Named: map[string]any{"row": "row_one"}}},
		ExpectRows: intPointer(1), RowContains: map[string]any{
			"title": "initial", "row_id": "row_one", "revision": uint64(1),
		},
	}}
	plan.Steps[0].Input.Parameters.Named["title"] = "revised"
	plan.Steps[0].Input.Mutation.IndexTerms = []string{"new-term"}
	plan.Verify = []skillwrite.Check{{
		ID:         "verify-current",
		MSQL:       "SELECT title, row_id, revision FROM work.notes WHERE row_id = :row LIMIT 1",
		Input:      executor.StatementInput{Parameters: executor.Parameters{Named: map[string]any{"row": "row_one"}}},
		ExpectRows: intPointer(1), RowContains: map[string]any{
			"title": "revised", "row_id": "row_one", "revision": uint64(2),
		},
	}}
	tool := skillwrite.ToolFunc(func(ctx context.Context, call skillwrite.Call) (result.Envelope, error) {
		return session.Execute(ctx, call.Request), nil
	})
	report, err := skillwrite.New(tool).Run(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if report.Receipt.Status != skillwrite.ReceiptCommitted || !report.Receipt.Verified ||
		len(report.Receipt.Changes) != 1 || report.Receipt.Changes[0].Revision == nil ||
		*report.Receipt.Changes[0].Revision != 2 {
		t.Fatalf("receipt = %#v", report.Receipt)
	}
	current, err := rows.Get(ctx, "work", "notes", "row_one")
	if err != nil || current.Revision != 2 || current.Values["title"] != "revised" {
		t.Fatalf("current Row = %#v, %v", current, err)
	}
	newPostings, err := rows.LookupAgentTerm(ctx, current.DatabaseID, "new-term", 10)
	if err != nil || len(newPostings) != 1 || newPostings[0].Revision != 2 {
		t.Fatalf("new index snapshot = %#v, %v", newPostings, err)
	}
	oldPostings, err := rows.LookupAgentTerm(ctx, current.DatabaseID, "old-term", 10)
	if err != nil || len(oldPostings) != 0 {
		t.Fatalf("old index snapshot = %#v, %v", oldPostings, err)
	}
}

func mutationPlan(decision skillwrite.Decision) skillwrite.Plan {
	plan := skillwrite.Plan{
		Version:             skillwrite.PlanVersion,
		ID:                  "plan-1",
		Decision:            decision,
		Database:            "work",
		Table:               "notes",
		Actor:               "agent:test",
		SourceEventID:       "conversation:event-1",
		Reason:              "capture verified decision",
		AuthorizedDatabases: []string{"work"},
		Preflight: []skillwrite.Check{{
			ID: "read-before-write", MSQL: "SELECT row_id, revision FROM work.notes LIMIT 10",
			ExpectRows: intPointer(1),
		}},
		Verify: []skillwrite.Check{{
			ID: "verify-after-write", MSQL: "SELECT row_id, revision FROM work.notes LIMIT 10",
			ExpectRows: intPointer(1),
		}},
	}
	update := mutationStep("revise", "UPDATE", "row_one",
		"UPDATE work.notes SET title = :title WHERE row_id = :row",
		map[string]any{"title": "revised", "row": "row_one"}, 1)
	insert := mutationStep("insert", "INSERT", "new-row",
		"INSERT INTO work.notes (title) VALUES (:title)",
		map[string]any{"title": "new module"}, 0)
	deleteStep := mutationStep("delete", "DELETE", "row_two",
		"DELETE FROM work.notes WHERE row_id = :row",
		map[string]any{"row": "row_two"}, 1)
	deleteStep.Input.Mutation.IndexTerms = nil
	deleteStep.Input.Mutation.RouteLeafIDs = nil
	relate := mutationStep("relate", "RELATE", "row_one->row_two",
		"RELATE work.notes ROW :source TO work.notes ROW :target TYPE :type",
		map[string]any{"source": "row_one", "target": "row_two", "type": "supports"}, 0)
	relate.Input.Mutation.ExpectedSchemaVersion = 0
	relate.Input.Mutation.IndexTerms = nil
	relate.Input.Mutation.RouteLeafIDs = nil

	switch decision {
	case skillwrite.DecisionIgnore:
		plan.Verify = nil
	case skillwrite.DecisionInsert:
		plan.Steps = []skillwrite.Step{insert}
	case skillwrite.DecisionRevise, skillwrite.DecisionMove:
		plan.Steps = []skillwrite.Step{update}
	case skillwrite.DecisionMerge:
		plan.Steps = []skillwrite.Step{update, deleteStep}
	case skillwrite.DecisionSplit:
		plan.Steps = []skillwrite.Step{update, insert}
	case skillwrite.DecisionRelate:
		plan.Steps = []skillwrite.Step{relate}
	}
	return plan
}

func mutationStep(id, kind, target, source string, named map[string]any, revision uint64) skillwrite.Step {
	return skillwrite.Step{
		ID: id, Kind: kind, Target: target, MSQL: source,
		Input: executor.StatementInput{
			Parameters: executor.Parameters{Named: named},
			Mutation: executor.MutationOptions{
				ExpectedSchemaVersion: 1, ExpectedRevision: revision, MaxAffectedRows: 1,
				Actor: "agent:test", Source: "conversation:event-1", Reason: "capture verified decision",
				IndexTerms: []string{"decision", "routing"}, RouteLeafIDs: []string{},
			},
		},
	}
}

type planTool struct {
	calls []skillwrite.Call
}

func (tool *planTool) Invoke(_ context.Context, call skillwrite.Call) (result.Envelope, error) {
	tool.calls = append(tool.calls, call)
	items, err := parser.ParseBatch(call.Request.Source)
	if err != nil {
		return result.Envelope{}, err
	}
	results := make([]result.StatementResult, len(items.Statements))
	for index, statement := range items.Statements {
		value := result.NewStatement(index, statement.Kind, statement.Kind)
		if call.Phase != skillwrite.PhaseMutation {
			value.Rows = []result.Row{{"row_id": "row_one", "revision": uint64(1)}}
		} else if statement.Kind != "BEGIN" && statement.Kind != "COMMIT" {
			revision := uint64(2)
			value.Revision = &revision
			sequence := uint64(7)
			value.CommitSequence = &sequence
			value.AffectedRows = 1
		}
		results[index] = value
	}
	return result.NewEnvelope(call.Request.RequestID, results...), nil
}

func containsPhase(calls []skillwrite.Call, phase skillwrite.Phase) bool {
	for _, call := range calls {
		if call.Phase == phase {
			return true
		}
	}
	return false
}

func callForPhase(t *testing.T, calls []skillwrite.Call, phase skillwrite.Phase) skillwrite.Call {
	t.Helper()
	for _, call := range calls {
		if call.Phase == phase {
			return call
		}
	}
	t.Fatalf("phase %q not found in %#v", phase, calls)
	return skillwrite.Call{}
}

func intPointer(value int) *int { return &value }
