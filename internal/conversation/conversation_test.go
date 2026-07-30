package conversation_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/conversation"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/msql/parser"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/skillwrite"
	sqlitestore "github.com/HW-Yue/Memora/internal/store/sqlite"
)

func TestDeltaCommitsStableConclusionIgnoresChitchatAndReplaysIdempotently(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	database, err := sqlitestore.Open(filepath.Join(t.TempDir(), "conversation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	tool := &mutationTool{}
	processor := conversation.New(conversation.NewJournal(database), tool)
	event := deltaEvent("event-1", "work")

	first, err := processor.Process(ctx, event)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != conversation.StatusProcessed || first.Replayed || first.Ignored != 1 ||
		len(first.Mutations) != 1 || first.Mutations[0].Status != skillwrite.ReceiptCommitted {
		t.Fatalf("first receipt = %#v", first)
	}
	if len(tool.calls) != 3 {
		t.Fatalf("tool calls = %d, want preflight/mutation/verify", len(tool.calls))
	}

	second, err := processor.Process(ctx, event)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Replayed || second.EventID != first.EventID || len(tool.calls) != 3 {
		t.Fatalf("replayed receipt = %#v, calls = %d", second, len(tool.calls))
	}

	changed := event
	changed.Workspace = "different-project"
	_, err = processor.Process(ctx, changed)
	var stable interface{ StableCode() string }
	if !errors.As(err, &stable) || stable.StableCode() != string(result.CodeRevisionConflict) {
		t.Fatalf("changed duplicate error = %v", err)
	}
}

func TestExplicitCheckpointSupportsProjectSwitchAndSessionEnd(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	database, err := sqlitestore.Open(filepath.Join(t.TempDir(), "checkpoint.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	journal := conversation.NewJournal(database)
	processor := conversation.New(journal, &mutationTool{})

	for index, databaseName := range []string{"work", "home"} {
		event := conversation.Event{
			Version: conversation.EventVersion, EventID: "checkpoint-" + databaseName,
			SessionID: "session-1", Kind: conversation.KindCheckpoint,
			Workspace: "workspace-" + databaseName, AuthorizedDatabases: []string{databaseName},
			Checkpoint: &conversation.Checkpoint{
				ActiveDatabase: databaseName, RoutePath: "/current", LastEventID: "delta-" + databaseName,
			},
		}
		receipt, err := processor.Process(ctx, event)
		if err != nil || receipt.Status != conversation.StatusCheckpointed {
			t.Fatalf("checkpoint %d receipt = %#v, %v", index, receipt, err)
		}
	}
	checkpoint, found, err := journal.LoadCheckpoint(ctx, "session-1")
	if err != nil || !found || checkpoint.ActiveDatabase != "home" || checkpoint.Workspace != "workspace-home" {
		t.Fatalf("switched checkpoint = %#v, found=%t, err=%v", checkpoint, found, err)
	}

	ended, err := processor.Process(ctx, conversation.Event{
		Version: conversation.EventVersion, EventID: "end-1", SessionID: "session-1",
		Kind: conversation.KindSessionEnd, Workspace: "workspace-home",
		AuthorizedDatabases: []string{"home"},
	})
	if err != nil || ended.Status != conversation.StatusEnded {
		t.Fatalf("end receipt = %#v, %v", ended, err)
	}
	if _, found, err := journal.LoadCheckpoint(ctx, "session-1"); err != nil || found {
		t.Fatalf("checkpoint after end found=%t, err=%v", found, err)
	}
}

func TestMissingDeltaContextReturnsActionableReceiptBeforeMutation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	database, err := sqlitestore.Open(filepath.Join(t.TempDir(), "missing.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	tool := &mutationTool{}
	processor := conversation.New(conversation.NewJournal(database), tool)
	for _, field := range []string{"plan", "database"} {
		event := deltaEvent("event-missing-"+field, "work")
		if field == "plan" {
			event.Deltas[1].Plan = nil
		} else {
			event.Deltas[1].Database = ""
		}
		receipt, err := processor.Process(ctx, event)
		if err != nil {
			t.Fatalf("missing %s: %v", field, err)
		}
		if receipt.Status != conversation.StatusNeedsContext || len(receipt.Missing) == 0 || len(tool.calls) != 0 {
			t.Fatalf("missing %s receipt = %#v, calls = %d", field, receipt, len(tool.calls))
		}
	}
}

func deltaEvent(id, database string) conversation.Event {
	plan := mutationPlan(id, database)
	return conversation.Event{
		Version: conversation.EventVersion, EventID: id, SessionID: "session-1",
		Kind: conversation.KindDelta, Workspace: "workspace-work",
		AuthorizedDatabases: []string{database},
		Deltas: []conversation.Delta{
			{ID: "hello", Disposition: conversation.DispositionIgnore, Reason: "social exchange"},
			{ID: "decision", Disposition: conversation.DispositionPersist, Database: database, Plan: &plan},
		},
	}
}

func mutationPlan(eventID, database string) skillwrite.Plan {
	zero, one := 0, 1
	return skillwrite.Plan{
		Version: skillwrite.PlanVersion, ID: "plan-" + eventID, Decision: skillwrite.DecisionInsert,
		Database: database, Table: "notes", Actor: "agent:test", SourceEventID: eventID,
		Reason: "capture stable conversation conclusion", AuthorizedDatabases: []string{database},
		Preflight: []skillwrite.Check{{
			ID: "duplicate-check", MSQL: "SELECT row_id FROM " + database + ".notes LIMIT 1", ExpectRows: &zero,
		}},
		Steps: []skillwrite.Step{{
			ID: "insert", Kind: "INSERT", Target: "new-decision",
			MSQL: "INSERT INTO " + database + ".notes (title) VALUES (:title)",
			Input: executor.StatementInput{
				Parameters: executor.Parameters{Named: map[string]any{"title": "stable decision"}},
				Mutation: executor.MutationOptions{
					ExpectedSchemaVersion: 1, MaxAffectedRows: 1, Actor: "agent:test",
					Source: eventID, Reason: "capture stable conversation conclusion",
					IndexTerms: []string{"decision"}, RouteLeafIDs: []string{},
				},
			},
		}},
		Verify: []skillwrite.Check{{
			ID: "verify", MSQL: "SELECT row_id FROM " + database + ".notes LIMIT 1", ExpectRows: &one,
		}},
	}
}

type mutationTool struct{ calls []skillwrite.Call }

func (tool *mutationTool) Invoke(_ context.Context, call skillwrite.Call) (result.Envelope, error) {
	tool.calls = append(tool.calls, call)
	batch, err := parser.ParseBatch(call.Request.Source)
	if err != nil {
		return result.Envelope{}, err
	}
	results := make([]result.StatementResult, len(batch.Statements))
	for index, statement := range batch.Statements {
		item := result.NewStatement(index, statement.Kind, statement.Kind)
		switch call.Phase {
		case skillwrite.PhasePreflight:
			item.Rows = []result.Row{}
		case skillwrite.PhaseVerify:
			item.Rows = []result.Row{{"row_id": "row_one"}}
		case skillwrite.PhaseMutation:
			item.AffectedRows = 1
			revision, sequence := uint64(1), uint64(1)
			item.Revision, item.CommitSequence = &revision, &sequence
		}
		results[index] = item
	}
	return result.NewEnvelope(call.Request.RequestID, results...), nil
}
