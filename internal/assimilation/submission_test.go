package assimilation_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/assimilation"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/msql/parser"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/skillwrite"
	nativekvstore "github.com/HW-Yue/Memora/internal/store/nativekv"
)

func TestReviewedLongFormSubmissionWritesModulesRelationsAndCompactSourceReceipt(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	database, processor, revision := completedCoverage(t, ctx)
	defer database.Close()
	tool := &submissionTool{}
	submission := reviewedSubmission(revision)

	receipt, err := processor.Submit(ctx, submission, tool)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Version != assimilation.SourceReceiptVersion ||
		receipt.Status != assimilation.SubmissionCommitted || receipt.Replayed ||
		receipt.Source.ID != "book" || receipt.Source.ContentHash != digest("b") ||
		len(receipt.Impacts) != 3 || len(receipt.KeyFacts) != 1 {
		t.Fatalf("source receipt = %#v", receipt)
	}
	if receipt.Impacts[0].Changes[0].ObjectID != "row-created-1" ||
		receipt.Impacts[2].Changes[0].ObjectID != "relation-supports" {
		t.Fatalf("receipt object IDs = %#v", receipt.Impacts)
	}
	if tool.relationSource != "row-created-1" || tool.relationTarget != "row-created-2" {
		t.Fatalf("resolved relationship endpoints = %q -> %q", tool.relationSource, tool.relationTarget)
	}
	if got := receipt.KeyFacts[0]; got.ModuleID != "module-retention" || got.Field != "retention_days" ||
		got.Anchor.UnitID != "chapter" || got.Anchor.Start != 1 || got.Anchor.End != 2 {
		t.Fatalf("key fact receipt = %#v", got)
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"Semantic module one", "INSERT INTO", "raw source"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("Source Receipt persisted semantic/raw body %q: %s", forbidden, encoded)
		}
	}
	firstCalls := len(tool.calls)
	if firstCalls != 9 {
		t.Fatalf("tool calls = %d, want 9", firstCalls)
	}

	replayed, err := processor.Submit(ctx, submission, tool)
	if err != nil || !replayed.Replayed || len(tool.calls) != firstCalls {
		t.Fatalf("replayed receipt = %#v, calls = %d, err = %v", replayed, len(tool.calls), err)
	}
	loaded, err := processor.SourceReceipt(ctx, submission.SubmissionID)
	if err != nil || loaded.SubmissionID != submission.SubmissionID {
		t.Fatalf("loaded Source Receipt = %#v, %v", loaded, err)
	}

	_, err = processor.Process(ctx, assimilation.Event{
		Version: assimilation.EventVersion, EventID: "clear-after-submit", TaskID: submission.TaskID,
		Workspace: submission.Workspace, Kind: assimilation.KindClear, ExpectedRevision: revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := processor.SourceReceipt(ctx, submission.SubmissionID); err != nil {
		t.Fatalf("Source Receipt disappeared after temporary task clear: %v", err)
	}
}

func TestSubmissionGatesCoverageReviewAnchorsPlansAndConflictsBeforeWrites(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	database, processor, revision := completedCoverage(t, ctx)
	defer database.Close()

	tests := []struct {
		name       string
		change     func(*assimilation.Submission)
		wantStatus assimilation.SubmissionStatus
		wantCode   result.Code
	}{
		{
			name: "review context is not isolated",
			change: func(value *assimilation.Submission) {
				value.Review.ContextID = value.DraftContextID
			},
			wantCode: result.CodeValidation,
		},
		{
			name: "review omits a semantic module",
			change: func(value *assimilation.Submission) {
				value.Review.CheckedModuleIDs = value.Review.CheckedModuleIDs[:1]
			},
			wantCode: result.CodeValidation,
		},
		{
			name: "source anchor exceeds inventory extent",
			change: func(value *assimilation.Submission) {
				value.Modules[0].Anchors[0].End = 3
			},
			wantCode: result.CodeValidation,
		},
		{
			name: "plan provenance differs from submission",
			change: func(value *assimilation.Submission) {
				value.Modules[0].Plan.SourceEventID = "different-event"
				value.Modules[0].Plan.Steps[0].Input.Mutation.Source = "different-event"
			},
			wantCode: result.CodeValidation,
		},
		{
			name: "unresolved semantic conflict asks user",
			change: func(value *assimilation.Submission) {
				value.UnresolvedConflictIDs = []string{"conflict-retention"}
			},
			wantStatus: assimilation.SubmissionNeedsUser,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := &submissionTool{}
			value := reviewedSubmission(revision)
			value.SubmissionID += "-" + strings.ReplaceAll(tt.name, " ", "-")
			for index := range value.Modules {
				rebindPlan(&value.Modules[index].Plan, value.SubmissionID)
			}
			for index := range value.Relationships {
				rebindPlan(&value.Relationships[index].Plan, value.SubmissionID)
			}
			tt.change(&value)
			receipt, err := processor.Submit(ctx, value, tool)
			if tt.wantCode != "" {
				assertCode(t, err, tt.wantCode)
			} else if err != nil || receipt.Status != tt.wantStatus {
				t.Fatalf("receipt = %#v, err = %v", receipt, err)
			}
			if len(tool.calls) != 0 {
				t.Fatalf("gate made %d Tool calls", len(tool.calls))
			}
			if tt.wantStatus == assimilation.SubmissionNeedsUser {
				loaded, loadErr := processor.SourceReceipt(ctx, value.SubmissionID)
				if loadErr != nil || loaded.Status != assimilation.SubmissionNeedsUser ||
					len(loaded.UnresolvedConflictIDs) != 1 {
					t.Fatalf("stored needs-user receipt = %#v, %v", loaded, loadErr)
				}
			}
		})
	}
}

func TestSubmissionRequiresCoverageCompleteAndDoesNotBlindlyReplayInDoubtWrites(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "submission.db")
	database, err := nativekvstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	processor := assimilation.New(database)
	created, err := processor.Process(ctx, simpleInventoryEvent("submission-inventory"))
	if err != nil {
		t.Fatal(err)
	}
	tool := &submissionTool{}
	_, err = processor.Submit(ctx, reviewedSubmission(created.Revision), tool)
	assertCode(t, err, result.CodeRevisionConflict)
	if len(tool.calls) != 0 {
		t.Fatalf("incomplete coverage made %d Tool calls", len(tool.calls))
	}

	covered, err := processor.Process(ctx, windowEvent("submission-window", created.Revision, assimilation.Window{
		UnitID: "chapter", Start: 0, End: 4, Fingerprint: digest("c"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	finished, err := processor.Process(ctx, assimilation.Event{
		Version: assimilation.EventVersion, EventID: "submission-finish", TaskID: "book-task",
		Workspace: "project-memora", Kind: assimilation.KindFinish, ExpectedRevision: covered.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	submission := reviewedSubmission(finished.Revision)
	tool.failMutation = 2
	receipt, err := processor.Submit(ctx, submission, tool)
	if err != nil || receipt.Status != assimilation.SubmissionInDoubt {
		t.Fatalf("in-doubt receipt = %#v, %v", receipt, err)
	}
	calls := len(tool.calls)
	replayed, err := processor.Submit(ctx, submission, tool)
	if err != nil || replayed.Status != assimilation.SubmissionInDoubt || !replayed.Replayed || len(tool.calls) != calls {
		t.Fatalf("in-doubt replay = %#v, calls = %d, err = %v", replayed, len(tool.calls), err)
	}
}

func completedCoverage(t *testing.T, ctx context.Context) (interface{ Close() error }, *assimilation.Processor, uint64) {
	t.Helper()
	database, err := nativekvstore.Open(filepath.Join(t.TempDir(), "review.db"))
	if err != nil {
		t.Fatal(err)
	}
	processor := assimilation.New(database)
	created, err := processor.Process(ctx, assimilation.Event{
		Version: assimilation.EventVersion, EventID: "review-inventory", TaskID: "book-task",
		Workspace: "project-memora", Kind: assimilation.KindInventory,
		Inventory: &assimilation.Inventory{
			Source: assimilation.Source{ID: "book", Title: "Fixture Book", Locator: "fixture/book.md", ContentHash: digest("b")},
			Units: []assimilation.Unit{
				{ID: "source", Kind: assimilation.UnitSource, Label: "Fixture Book"},
				{ID: "chapter", ParentID: "source", Kind: assimilation.UnitChapter, Label: "Chapter", Anchor: "chapter-1", Extent: 2},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	covered, err := processor.Process(ctx, windowEvent("review-window", created.Revision, assimilation.Window{
		UnitID: "chapter", Start: 0, End: 2, Fingerprint: digest("d"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	finished, err := processor.Process(ctx, assimilation.Event{
		Version: assimilation.EventVersion, EventID: "review-finish", TaskID: "book-task",
		Workspace: "project-memora", Kind: assimilation.KindFinish, ExpectedRevision: covered.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	return database, processor, finished.Revision
}

func reviewedSubmission(revision uint64) assimilation.Submission {
	submissionID := "book-submit-1"
	modules := []assimilation.SemanticModule{
		{ID: "module-routing", Anchors: []assimilation.SourceAnchor{{UnitID: "chapter", Start: 0, End: 1}}, Plan: submissionPlan(submissionID, "plan-routing", skillwrite.DecisionInsert, "row-routing")},
		{ID: "module-retention", Anchors: []assimilation.SourceAnchor{{UnitID: "chapter", Start: 1, End: 2}}, Plan: submissionPlan(submissionID, "plan-retention", skillwrite.DecisionInsert, "row-retention")},
	}
	relationships := []assimilation.SemanticRelationship{{
		ID: "relationship-supports", SourceModuleID: "module-routing", TargetModuleID: "module-retention",
		Anchors: []assimilation.SourceAnchor{{UnitID: "chapter", Start: 0, End: 2}},
		Plan:    submissionPlan(submissionID, "plan-supports", skillwrite.DecisionRelate, "row-routing->row-retention"),
	}}
	return assimilation.Submission{
		Version: assimilation.SubmissionVersion, SubmissionID: submissionID,
		TaskID: "book-task", Workspace: "project-memora", CoverageRevision: revision,
		Author: "agent:host", DraftContextID: "draft-context", DraftDigest: digest("e"),
		Modules: modules, Relationships: relationships,
		KeyFacts: []assimilation.KeyFact{{
			ID: "fact-retention", ModuleID: "module-retention", Field: "retention_days",
			ValueDigest: digest("f"), Anchor: assimilation.SourceAnchor{UnitID: "chapter", Start: 1, End: 2},
		}},
		Review: assimilation.Review{
			Version: assimilation.ReviewVersion, Reviewer: "agent:reviewer", ContextID: "review-context",
			DraftDigest: digest("e"), CoverageRevision: revision, Verdict: assimilation.ReviewAccepted,
			CheckedModuleIDs:       []string{"module-routing", "module-retention"},
			CheckedRelationshipIDs: []string{"relationship-supports"}, CheckedKeyFactIDs: []string{"fact-retention"},
			AnchorsVerified: true, KeyFactsVerified: true, ConflictsChecked: true, RawContentAbsent: true,
		},
	}
}

func submissionPlan(sourceEventID, id string, decision skillwrite.Decision, target string) skillwrite.Plan {
	zero, one := 0, 1
	plan := skillwrite.Plan{
		Version: skillwrite.PlanVersion, ID: id, Decision: decision, Database: "work", Table: "notes",
		Actor: "agent:host", SourceEventID: sourceEventID, Reason: "absorb reviewed fixture book",
		AuthorizedDatabases: []string{"work"},
		Preflight:           []skillwrite.Check{{ID: "before", MSQL: "SELECT row_id FROM work.notes LIMIT 10", ExpectRows: &zero}},
		Verify:              []skillwrite.Check{{ID: "after", MSQL: "SELECT row_id FROM work.notes LIMIT 10", ExpectRows: &one}},
	}
	mutation := executor.MutationOptions{
		ExpectedSchemaVersion: 1, MaxAffectedRows: 1, Actor: plan.Actor,
		Source: sourceEventID, Reason: plan.Reason, RouteLeafIDs: []string{},
	}
	if decision == skillwrite.DecisionRelate {
		mutation.ExpectedSchemaVersion = 0
		mutation.RouteLeafIDs = nil
		plan.Steps = []skillwrite.Step{{
			ID: "relate", Kind: "RELATE", Target: target,
			MSQL: "RELATE work.notes ROW :source TO work.notes ROW :target TYPE :type",
			Input: executor.StatementInput{Parameters: executor.Parameters{Named: map[string]any{
				"source": "module-routing", "target": "module-retention", "type": "supports",
			}}, Mutation: mutation},
		}}
	} else {
		plan.Steps = []skillwrite.Step{{
			ID: "insert", Kind: "INSERT", Target: target,
			MSQL: "INSERT INTO work.notes (title) VALUES (:title)",
			Input: executor.StatementInput{Parameters: executor.Parameters{Named: map[string]any{
				"title": "Semantic module one",
			}}, Mutation: mutation},
		}}
	}
	return plan
}

func rebindPlan(plan *skillwrite.Plan, eventID string) {
	plan.SourceEventID = eventID
	for index := range plan.Steps {
		plan.Steps[index].Input.Mutation.Source = eventID
	}
}

type submissionTool struct {
	calls          []skillwrite.Call
	mutations      int
	failMutation   int
	relationSource string
	relationTarget string
}

func (tool *submissionTool) Invoke(_ context.Context, call skillwrite.Call) (result.Envelope, error) {
	tool.calls = append(tool.calls, call)
	if call.Phase == skillwrite.PhaseMutation {
		tool.mutations++
		if tool.failMutation == tool.mutations {
			return result.Envelope{}, errors.New("connection lost after dispatch")
		}
	}
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
			item.Rows = []result.Row{{"row_id": "verified"}}
		case skillwrite.PhaseMutation:
			if statement.Kind != "BEGIN" && statement.Kind != "COMMIT" {
				revision, sequence := uint64(1), uint64(tool.mutations)
				item.AffectedRows, item.Revision, item.CommitSequence = 1, &revision, &sequence
				if statement.Kind == "RELATE" {
					tool.relationSource, _ = call.Request.Statements[0].Parameters.Named["source"].(string)
					tool.relationTarget, _ = call.Request.Statements[0].Parameters.Named["target"].(string)
					item.Rows = []result.Row{{"relation_id": "relation-supports"}}
				} else {
					item.Rows = []result.Row{{"row_id": fmt.Sprintf("row-created-%d", tool.mutations)}}
				}
			}
		}
		results[index] = item
	}
	return result.NewEnvelope(call.Request.RequestID, results...), nil
}
