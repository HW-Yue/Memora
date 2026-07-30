package feedback_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/feedback"
	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/skillwrite"
	nativekvstore "github.com/HW-Yue/Memora/internal/store/nativekv"
)

func TestFiveFeedbackKindsRecordWithoutChangingFactsAndReplay(t *testing.T) {
	t.Parallel()

	ctx, processor, rows, current, closeFixture := feedbackFixture(t)
	defer closeFixture()
	for index, kind := range []feedback.Kind{
		feedback.KindUseful, feedback.KindIrrelevant, feedback.KindStale, feedback.KindWrong, feedback.KindIncomplete,
	} {
		event := feedbackEvent("feedback-"+string(rune('a'+index)), kind, current)
		receipt, err := processor.Record(ctx, event)
		if err != nil || receipt.Status != "recorded" || receipt.Replayed {
			t.Fatalf("feedback %q receipt = %#v, %v", kind, receipt, err)
		}
		replayed, err := processor.Record(ctx, event)
		if err != nil || !replayed.Replayed {
			t.Fatalf("feedback %q replay = %#v, %v", kind, replayed, err)
		}
	}
	after, err := rows.Get(ctx, "work", "notes", current.ID)
	if err != nil || after.Revision != current.Revision || after.Values["title"] != current.Values["title"] {
		t.Fatalf("feedback changed current Row = %#v, %v", after, err)
	}
	records, err := rows.History(ctx, "work", "notes", current.ID)
	if err != nil || len(records) != 2 {
		t.Fatalf("feedback changed History = %#v, %v", records, err)
	}
}

func TestConfirmedUndoCreatesAuditableCompensationRevisionAndCannotBlindlyReplay(t *testing.T) {
	t.Parallel()

	ctx, processor, rows, current, closeFixture := feedbackFixture(t)
	defer closeFixture()
	event := feedbackEvent("feedback-wrong", feedback.KindWrong, current)
	if _, err := processor.Record(ctx, event); err != nil {
		t.Fatal(err)
	}
	confirmation := feedback.Confirmation{
		Version: feedback.ConfirmationVersion, ConfirmationID: "confirm-undo",
		FeedbackEventID: event.EventID, SourceEventID: "user-confirm-undo", Actor: "agent:test",
		Instruction: "restore the prior verified title", Action: feedback.ActionUndo,
		ExpectedRevision: current.Revision, AuthorizedDatabases: []string{"work"},
		Undo: &feedback.Undo{TargetRevision: 1, ExpectedSchemaVersion: 1, IndexTerms: []string{"first"}, RouteLeafIDs: []string{}},
	}
	receipt, err := processor.Confirm(ctx, confirmation)
	if err != nil || receipt.Status != "confirmed" || receipt.NewRevision == nil || *receipt.NewRevision != 3 ||
		receipt.RestoredRevision == nil || *receipt.RestoredRevision != 1 || !receipt.Verified {
		t.Fatalf("undo receipt = %#v, %v", receipt, err)
	}
	restored, err := rows.Get(ctx, "work", "notes", current.ID)
	if err != nil || restored.Values["title"] != "first" || restored.Revision != 3 {
		t.Fatalf("restored Row = %#v, %v", restored, err)
	}
	records, err := rows.History(ctx, "work", "notes", current.ID)
	if err != nil || len(records) != 3 || records[2].Operation != history.OperationCompensate ||
		records[2].Source != confirmation.SourceEventID || !strings.Contains(records[2].Reason, event.EventID) {
		t.Fatalf("compensation History = %#v, %v", records, err)
	}
	replayed, err := processor.Confirm(ctx, confirmation)
	if err != nil || !replayed.Replayed || replayed.NewRevision == nil || *replayed.NewRevision != 3 {
		t.Fatalf("undo replay = %#v, %v", replayed, err)
	}
	records, _ = rows.History(ctx, "work", "notes", current.ID)
	if len(records) != 3 {
		t.Fatalf("undo replay appended History: %#v", records)
	}
}

func TestFeedbackAndConfirmationRejectStaleOrUnconfirmedMutationBeforeWrites(t *testing.T) {
	t.Parallel()

	ctx, processor, rows, current, closeFixture := feedbackFixture(t)
	defer closeFixture()
	stale := feedbackEvent("feedback-stale-revision", feedback.KindWrong, current)
	stale.Target.Revision--
	_, err := processor.Record(ctx, stale)
	assertCode(t, err, result.CodeRevisionConflict)

	useful := feedbackEvent("feedback-useful", feedback.KindUseful, current)
	if _, err := processor.Record(ctx, useful); err != nil {
		t.Fatal(err)
	}
	confirmation := feedback.Confirmation{
		Version: feedback.ConfirmationVersion, ConfirmationID: "confirm-useful-undo",
		FeedbackEventID: useful.EventID, SourceEventID: "user-confirm", Actor: "agent:test",
		Instruction: "undo anyway", Action: feedback.ActionUndo, ExpectedRevision: current.Revision,
		AuthorizedDatabases: []string{"work"},
		Undo:                &feedback.Undo{TargetRevision: 1, ExpectedSchemaVersion: 1, IndexTerms: []string{}, RouteLeafIDs: []string{}},
	}
	_, err = processor.Confirm(ctx, confirmation)
	assertCode(t, err, result.CodePermissionDenied)
	after, _ := rows.Get(ctx, "work", "notes", current.ID)
	if after.Revision != current.Revision {
		t.Fatalf("rejected confirmation changed revision to %d", after.Revision)
	}
}

func TestConfirmedRevisionPlanCannotTargetAnotherRow(t *testing.T) {
	t.Parallel()

	ctx, processor, rows, current, closeFixture := feedbackFixture(t)
	defer closeFixture()
	event := feedbackEvent("feedback-cross-row", feedback.KindIncomplete, current)
	if _, err := processor.Record(ctx, event); err != nil {
		t.Fatal(err)
	}
	zero, one := 0, 1
	plan := skillwrite.Plan{
		Version: skillwrite.PlanVersion, ID: "cross-row-plan", Decision: skillwrite.DecisionRevise,
		Database: "work", Table: "notes", Actor: "agent:test", SourceEventID: "user-cross-row",
		Reason: "feedback feedback-cross-row: revise", AuthorizedDatabases: []string{"work"},
		Preflight: []skillwrite.Check{{ID: "preflight", MSQL: "SELECT row_id FROM work.notes WHERE row_id = :row LIMIT 1", Input: executor.StatementInput{Parameters: executor.Parameters{Named: map[string]any{"row": "another-row"}}}, ExpectRows: &zero}},
		Steps: []skillwrite.Step{{
			ID: "revise", Kind: "UPDATE", Target: "another-row", MSQL: "UPDATE work.notes SET title = :title WHERE row_id = :row",
			Input: executor.StatementInput{
				Parameters: executor.Parameters{Named: map[string]any{"row": "another-row", "title": "unsafe"}},
				Mutation:   executor.MutationOptions{ExpectedSchemaVersion: 1, ExpectedRevision: current.Revision, MaxAffectedRows: 1, Actor: "agent:test", Source: "user-cross-row", Reason: "feedback feedback-cross-row: revise", IndexTerms: []string{}, RouteLeafIDs: []string{}},
			},
		}},
		Verify: []skillwrite.Check{{ID: "verify", MSQL: "SELECT row_id FROM work.notes WHERE row_id = :row LIMIT 1", Input: executor.StatementInput{Parameters: executor.Parameters{Named: map[string]any{"row": "another-row"}}}, ExpectRows: &one}},
	}
	_, err := processor.Confirm(ctx, feedback.Confirmation{
		Version: feedback.ConfirmationVersion, ConfirmationID: "confirm-cross-row", FeedbackEventID: event.EventID,
		SourceEventID: "user-cross-row", Actor: "agent:test", Instruction: "revise another row",
		Action: feedback.ActionRevise, ExpectedRevision: current.Revision, AuthorizedDatabases: []string{"work"}, Plan: &plan,
	})
	assertCode(t, err, result.CodePermissionDenied)
	after, _ := rows.Get(ctx, "work", "notes", current.ID)
	if after.Revision != current.Revision {
		t.Fatalf("cross-Row confirmation changed revision to %d", after.Revision)
	}
}

func feedbackFixture(t *testing.T) (context.Context, *feedback.Processor, *row.Service, row.Row, func()) {
	t.Helper()
	ctx := context.Background()
	database, err := nativekvstore.Open(filepath.Join(t.TempDir(), "feedback.db"))
	if err != nil {
		t.Fatal(err)
	}
	dictionary := catalog.New(database, catalog.Options{})
	if _, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{Name: "work", Purpose: "Work", Scope: "Reviewed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := dictionary.CreateTable(ctx, "work", catalog.TableDefinition{
		Name: "notes", Purpose: "Notes", RowSemantics: "One note",
		Columns: []catalog.ColumnDefinition{{Name: "title", Type: "TEXT(100)", Purpose: "Title"}},
	}); err != nil {
		t.Fatal(err)
	}
	rows := row.New(database, dictionary, row.Options{})
	first, err := rows.Insert(ctx, "work", "notes", map[string]any{"title": "first"}, row.WriteOptions{
		ExpectedSchemaVersion: 1, Metadata: row.WriteMetadata{Actor: "agent:test", Source: "seed-1", Reason: "first"},
		IndexTerms: []string{"first"}, RouteLeafIDs: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	current, err := rows.Update(ctx, "work", "notes", first.ID, map[string]any{"title": "wrong"}, row.WriteOptions{
		ExpectedSchemaVersion: 1, ExpectedRevision: 1,
		Metadata:   row.WriteMetadata{Actor: "agent:test", Source: "seed-2", Reason: "wrong update"},
		IndexTerms: []string{"wrong"}, RouteLeafIDs: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	batch := executor.NewBatchSession(ctx, dictionary, rows)
	tool := skillwrite.ToolFunc(func(callCtx context.Context, call skillwrite.Call) (result.Envelope, error) {
		return batch.Execute(callCtx, call.Request), nil
	})
	return ctx, feedback.New(database, rows, tool), rows, current, func() {
		_ = batch.Close()
		_ = database.Close()
	}
}

func feedbackEvent(id string, kind feedback.Kind, current row.Row) feedback.Event {
	return feedback.Event{
		Version: feedback.EventVersion, EventID: id, Kind: kind, Actor: "agent:test", Reason: "quality feedback",
		Target: feedback.Target{Database: "work", Table: "notes", RowID: current.ID, Revision: current.Revision},
	}
}

func assertCode(t *testing.T, err error, want result.Code) {
	t.Helper()
	stable, ok := err.(interface{ StableCode() string })
	if !ok || result.Code(stable.StableCode()) != want {
		t.Fatalf("error = %v, want code %q", err, want)
	}
}
