package daemon

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/assimilation"
	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/ipc"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/skillwrite"
	nativekvstore "github.com/HW-Yue/Memora/internal/store/nativekv"
	protocolmsql "github.com/HW-Yue/Memora/protocol/msql"
)

func TestFormalAssimilationMSQLSurfaceCommitsThroughSharedServiceWithoutLegacyCoverage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	databaseStore, err := nativekvstore.Open(filepath.Join(t.TempDir(), "formal-assimilation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	dictionary := catalog.New(databaseStore, catalog.Options{})
	if _, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{Name: "work", Purpose: "Work", Scope: "Knowledge"}); err != nil {
		t.Fatal(err)
	}
	if _, err := dictionary.CreateTable(ctx, "work", catalog.TableDefinition{
		Name: "notes", Purpose: "Notes", RowSemantics: "One semantic module",
		Columns: []catalog.ColumnDefinition{{Name: "title", Type: "TEXT(200)", Purpose: "Title"}},
	}); err != nil {
		t.Fatal(err)
	}
	rows := row.New(databaseStore, dictionary, row.Options{})
	handler := newDatabaseHandler(ctx, dictionary, rows, databaseStore)
	defer handler.Close()
	session, err := handler.msql.OpenSession("agent:formal-assimilation")
	if err != nil {
		t.Fatal(err)
	}

	proposal := protocolmsql.AssimilationProposal{
		Version: protocolmsql.AssimilationProposalVersion, ProposalID: "formal-plan-1",
		Database: "work", Author: "agent:author", SourceID: "book-1",
		SourceLocator: "library/book.epub", SourceSHA256: daemonDigest("a"),
		DocumentSHA256: daemonDigest("b"), CoverageRevision: 7, CoveredNodes: 12, TotalNodes: 12,
		Evidence: &protocolmsql.AssimilationEvidence{
			Version: protocolmsql.AssimilationEvidenceVersion, Author: "agent:author",
			Reviewers: []string{"agent:reviewer"}, ReviewArtifactSHA256s: []string{daemonDigest("c")},
			CoverageRevision: 7, CoveredNodes: 12, TotalNodes: 12,
		},
		Statements: []protocolmsql.AssimilationStatement{{
			MSQL:       "INSERT INTO work.notes (title) VALUES (:title)",
			Parameters: protocolmsql.Parameters{Named: map[string]any{"title": "A complete semantic module"}},
			Mutation: protocolmsql.MutationOptions{
				ExpectedSchemaVersion: 1, MaxAffectedRows: 1, Actor: "agent:author",
				Source: "book-1", Reason: "assimilate covered semantic module",
				SourceKind: protocolmsql.SourceDocumentAnchor, SourceLocator: "library/book.epub",
				SourceContentHash: daemonDigest("a"),
			},
		}},
	}
	readAuthorization := protocolmsql.Authorization{
		Version: protocolmsql.AuthorizationVersion, Actor: "agent:author",
		AuthorizedDatabases: []string{"work"}, DefaultLevel: protocolmsql.LevelRead,
	}
	reviewed, err := session.ExecuteMSQL(ctx, protocolmsql.Request{
		Version: protocolmsql.RequestVersion,
		Source:  "REVIEW ASSIMILATION FOR DATABASE work USING :proposal",
		Statements: []protocolmsql.StatementInput{{
			Parameters:    protocolmsql.Parameters{Named: map[string]any{"proposal": proposal}},
			Authorization: readAuthorization,
		}},
	})
	if err != nil || !reviewed.OK || len(reviewed.Results) != 1 || len(reviewed.Results[0].Rows) != 1 {
		t.Fatalf("review envelope = %#v, %v", reviewed, err)
	}
	plan, ok := reviewed.Results[0].Rows[0]["assimilation_plan"].(protocolmsql.AssimilationPlan)
	if !ok || plan.Validate() != nil {
		t.Fatalf("review plan = %#v", reviewed.Results[0].Rows[0]["assimilation_plan"])
	}
	writeAuthorization := readAuthorization
	writeAuthorization.DefaultLevel = protocolmsql.LevelWrite
	writeAuthorization.Approval = &protocolmsql.Approval{
		Version: protocolmsql.ApprovalVersion, Action: protocolmsql.AssimilationSubmitAction,
		SubjectSHA256: strings.TrimPrefix(plan.SHA256, "sha256:"), Confirmed: true,
	}
	submitted, err := session.ExecuteMSQL(ctx, protocolmsql.Request{
		Version: protocolmsql.RequestVersion,
		Source:  "SUBMIT ASSIMILATION PLAN :plan FOR DATABASE work",
		Statements: []protocolmsql.StatementInput{{
			Parameters:    protocolmsql.Parameters{Named: map[string]any{"plan": plan}},
			Authorization: writeAuthorization,
		}},
	})
	if err != nil || !submitted.OK || submitted.Results[0].AffectedRows != 1 ||
		submitted.Results[0].Rows[0]["status"] != protocolmsql.AssimilationCommitted {
		t.Fatalf("submit envelope = %#v, %v", submitted, err)
	}
	stored, _, err := rows.ListPage(ctx, "work", "notes", 10)
	if err != nil || len(stored) != 1 || stored[0].Values["title"] != "A complete semantic module" {
		t.Fatalf("stored rows = %#v, %v", stored, err)
	}
	submitReceipt, ok := submitted.Results[0].Rows[0]["assimilation_receipt"].(protocolmsql.AssimilationReceipt)
	if !ok || submitReceipt.Evidence == nil || len(submitReceipt.Statements) != 1 ||
		len(submitReceipt.Statements[0].ObjectIDs) != 1 || submitReceipt.Statements[0].ObjectIDs[0] != stored[0].ID ||
		submitReceipt.Statements[0].Revision == nil || submitReceipt.Statements[0].CommitSequence == nil {
		t.Fatalf("submit source receipt = %#v", submitted.Results[0].Rows[0]["assimilation_receipt"])
	}
	shown, err := session.ExecuteMSQL(ctx, protocolmsql.Request{
		Version: protocolmsql.RequestVersion,
		Source:  "SHOW ASSIMILATION RECEIPT :receipt IN DATABASE work",
		Statements: []protocolmsql.StatementInput{{
			Parameters:    protocolmsql.Parameters{Named: map[string]any{"receipt": plan.PlanID}},
			Authorization: readAuthorization,
		}},
	})
	if err != nil || !shown.OK || shown.Results[0].Rows[0]["status"] != protocolmsql.AssimilationCommitted {
		t.Fatalf("shown receipt = %#v, %v", shown, err)
	}
	showReceipt, ok := shown.Results[0].Rows[0]["assimilation_receipt"].(protocolmsql.AssimilationReceipt)
	if !ok || !reflect.DeepEqual(submitReceipt, showReceipt) {
		t.Fatalf("receipt reconciliation = %#v / %#v", submitReceipt, showReceipt)
	}
}

func TestAssimilationHandlerPersistsTemporaryCoverageAcrossSessions(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, err := nativekvstore.Open(filepath.Join(t.TempDir(), "assimilation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	dictionary := catalog.New(databaseStore, catalog.Options{})
	handler := newDatabaseHandler(ctx, dictionary, row.New(databaseStore, dictionary, row.Options{}), databaseStore)
	defer handler.Close()

	inventory := assimilation.Event{
		Version: assimilation.EventVersion, EventID: "inventory-daemon", TaskID: "book-task",
		Workspace: "repo", Kind: assimilation.KindInventory,
		Inventory: &assimilation.Inventory{
			Source: assimilation.Source{
				ID: "book", Title: "Book", Locator: "book.pdf",
				ContentHash: daemonDigest("a"), Kind: "document_anchor",
			},
			Units: []assimilation.Unit{
				{ID: "source", Kind: assimilation.UnitSource, Label: "Book"},
				{ID: "chapter", ParentID: "source", Kind: assimilation.UnitChapter, Label: "Chapter", Extent: 2},
			},
		},
	}
	created := invokeAssimilation(t, ctx, handler, "ipc-one", inventory)
	if created.Revision != 1 || created.UnreadCount != 1 {
		t.Fatalf("created receipt = %#v", created)
	}
	status := invokeAssimilation(t, ctx, handler, "ipc-two", assimilation.Event{
		Version: assimilation.EventVersion, EventID: "status-daemon", TaskID: "book-task",
		Workspace: "repo", Kind: assimilation.KindStatus,
	})
	if status.Revision != 1 || status.UnreadCount != 1 || status.Unread[0].UnitID != "chapter" {
		t.Fatalf("cross-session status = %#v", status)
	}
}

func TestAssimilationSubmissionCommitsReviewedModulesRelationshipAndReloadableReceipt(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, err := nativekvstore.Open(filepath.Join(t.TempDir(), "assimilation-submit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	dictionary := catalog.New(databaseStore, catalog.Options{})
	if _, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{Name: "work", Purpose: "Work", Scope: "Reviewed knowledge"}); err != nil {
		t.Fatal(err)
	}
	if _, err := dictionary.CreateTable(ctx, "work", catalog.TableDefinition{
		Name: "notes", Purpose: "Notes", RowSemantics: "One reviewed semantic module",
		Columns: []catalog.ColumnDefinition{{Name: "title", Type: "TEXT(200)", Purpose: "Module title"}},
	}); err != nil {
		t.Fatal(err)
	}
	rows := row.New(databaseStore, dictionary, row.Options{})
	handler := newDatabaseHandler(ctx, dictionary, rows, databaseStore)
	defer handler.Close()

	created := invokeAssimilation(t, ctx, handler, "coverage", assimilation.Event{
		Version: assimilation.EventVersion, EventID: "daemon-review-inventory", TaskID: "daemon-book",
		Workspace: "repo", Kind: assimilation.KindInventory,
		Inventory: &assimilation.Inventory{
			Source: assimilation.Source{
				ID: "fixture-book", Title: "Fixture Book", Locator: "fixture/book.md",
				ContentHash: daemonDigest("b"), Kind: "document_anchor",
			},
			Units: []assimilation.Unit{
				{ID: "source", Kind: assimilation.UnitSource, Label: "Fixture Book"},
				{ID: "chapter", ParentID: "source", Kind: assimilation.UnitChapter, Label: "Chapter", Extent: 2},
			},
		},
	})
	covered := invokeAssimilation(t, ctx, handler, "coverage", assimilation.Event{
		Version: assimilation.EventVersion, EventID: "daemon-review-window", TaskID: "daemon-book",
		Workspace: "repo", Kind: assimilation.KindWindow, ExpectedRevision: created.Revision,
		Window: &assimilation.Window{UnitID: "chapter", Start: 0, End: 2, Fingerprint: daemonDigest("c")},
	})
	finished := invokeAssimilation(t, ctx, handler, "coverage", assimilation.Event{
		Version: assimilation.EventVersion, EventID: "daemon-review-finish", TaskID: "daemon-book",
		Workspace: "repo", Kind: assimilation.KindFinish, ExpectedRevision: covered.Revision,
	})

	submissionID := "daemon-book-submit"
	modules := []assimilation.SemanticModule{
		{ID: "module-one", Anchors: []assimilation.SourceAnchor{{UnitID: "chapter", Start: 0, End: 1}}, Plan: daemonInsertPlan(submissionID, "plan-one", "module-one", "reviewed one")},
		{ID: "module-two", Anchors: []assimilation.SourceAnchor{{UnitID: "chapter", Start: 1, End: 2}}, Plan: daemonInsertPlan(submissionID, "plan-two", "module-two", "reviewed two")},
	}
	relationPlan := daemonRelationPlan(submissionID)
	submission := assimilation.Submission{
		Version: assimilation.SubmissionVersion, SubmissionID: submissionID, TaskID: "daemon-book",
		Workspace: "repo", CoverageRevision: finished.Revision, Author: "agent:host",
		DraftContextID: "draft-context", DraftDigest: daemonDigest("d"), Modules: modules,
		Relationships: []assimilation.SemanticRelationship{{
			ID: "supports", SourceModuleID: "module-one", TargetModuleID: "module-two",
			Anchors: []assimilation.SourceAnchor{{UnitID: "chapter", Start: 0, End: 2}}, Plan: relationPlan,
		}},
		KeyFacts: []assimilation.KeyFact{{
			ID: "fact-two", ModuleID: "module-two", Field: "title", ValueDigest: daemonDigest("e"),
			Anchor: assimilation.SourceAnchor{UnitID: "chapter", Start: 1, End: 2},
		}},
		Review: assimilation.Review{
			Version: assimilation.ReviewVersion, Reviewer: "agent:review", ContextID: "review-context",
			DraftDigest: daemonDigest("d"), CoverageRevision: finished.Revision, Verdict: assimilation.ReviewAccepted,
			CheckedModuleIDs: []string{"module-one", "module-two"}, CheckedRelationshipIDs: []string{"supports"},
			CheckedKeyFactIDs: []string{"fact-two"}, AnchorsVerified: true, KeyFactsVerified: true,
			ConflictsChecked: true, RawContentAbsent: true,
			Challenge: finished.ReviewChallenge, FindingsDigest: daemonDigest("f"),
		},
	}
	submission.Review.ArtifactDigest = assimilation.ReviewArtifactDigest(submission.Review)
	receipt := invokeSubmission(t, ctx, handler, "submit-session", submission)
	if receipt.Status != assimilation.SubmissionCommitted || len(receipt.Impacts) != 3 {
		t.Fatalf("Source Receipt = %#v", receipt)
	}
	firstID := receipt.Impacts[0].Changes[0].ObjectID
	secondID := receipt.Impacts[1].Changes[0].ObjectID
	if firstID == "" || secondID == "" || receipt.Impacts[2].Changes[0].ObjectID == "" {
		t.Fatalf("Source Receipt logical objects = %#v", receipt.Impacts)
	}
	for rowID, title := range map[string]string{firstID: "reviewed one", secondID: "reviewed two"} {
		current, err := rows.Get(ctx, "work", "notes", rowID)
		if err != nil || current.Revision != 1 || current.Values["title"] != title {
			t.Fatalf("reviewed Row %q = %#v, %v", rowID, current, err)
		}
		records, _, historyErr := rows.HistoryPage(ctx, "work", "notes", rowID, "", 10)
		if historyErr != nil || len(records) != 1 ||
			records[0].SourceKind != "reviewed_source" ||
			records[0].SourceReceiptID != submissionID ||
			records[0].SourceLocator != "fixture/book.md" {
			t.Fatalf("reviewed Row History %q = %#v, %v", rowID, records, historyErr)
		}
	}
	batch, _ := handler.session("submit-session")
	relations := batch.ExecuteBatch(ctx, executor.BatchRequest{
		RequestID: "verify-relation", Source: "SHOW RELATIONS FROM work.notes FOR ROW :row DIRECTION OUTGOING LIMIT 10",
		Statements: []executor.StatementInput{{Parameters: executor.Parameters{Named: map[string]any{"row": firstID}}}},
	})
	if !relations.OK || len(relations.Results) != 1 || len(relations.Results[0].Rows) != 1 {
		t.Fatalf("relations = %#v", relations)
	}

	payload, _ := json.Marshal(struct {
		SubmissionID string `json:"submission_id"`
	}{SubmissionID: submissionID})
	encoded, err := handler.Handle(ctx, ipc.Session{ID: "receipt-session"}, ipc.Request{
		Version: ipc.Version, RequestID: "load-receipt", Method: "assimilation.receipt", Payload: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	var loaded assimilation.SourceReceipt
	if err := json.Unmarshal(encoded, &loaded); err != nil || loaded.SubmissionID != submissionID {
		t.Fatalf("loaded Source Receipt = %#v, %v", loaded, err)
	}
}

func TestAssimilationHandlerRejectsRawContentField(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, err := nativekvstore.Open(filepath.Join(t.TempDir(), "raw.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	dictionary := catalog.New(databaseStore, catalog.Options{})
	handler := newDatabaseHandler(ctx, dictionary, row.New(databaseStore, dictionary, row.Options{}), databaseStore)
	defer handler.Close()
	payload := json.RawMessage(`{
		"version":"memora.assimilation-event/v1",
		"event_id":"raw","task_id":"task","workspace":"repo","kind":"inventory",
		"content":"raw source text"
	}`)
	if _, err := handler.Handle(ctx, ipc.Session{ID: "ipc"}, ipc.Request{
		Version: ipc.Version, RequestID: "raw-request", Method: "assimilation.record", Payload: payload,
	}); err == nil || !strings.Contains(err.Error(), "payload is invalid") {
		t.Fatalf("raw content error = %v", err)
	}
	submissionPayload := json.RawMessage(`{
		"version":"memora.assimilation-submission/v1",
		"submission_id":"raw","task_id":"task","workspace":"repo",
		"content":"raw source text"
	}`)
	if _, err := handler.Handle(ctx, ipc.Session{ID: "ipc"}, ipc.Request{
		Version: ipc.Version, RequestID: "raw-submission", Method: "assimilation.submit", Payload: submissionPayload,
	}); err == nil || !strings.Contains(err.Error(), "payload is invalid") {
		t.Fatalf("raw submission error = %v", err)
	}
}

func daemonInsertPlan(eventID, planID, moduleID, title string) skillwrite.Plan {
	zero, one := 0, 1
	reason := "absorb reviewed fixture book"
	return skillwrite.Plan{
		Version: skillwrite.PlanVersion, ID: planID, Decision: skillwrite.DecisionInsert,
		Database: "work", Table: "notes", Actor: "agent:host", SourceEventID: eventID,
		Reason: reason, AuthorizedDatabases: []string{"work"},
		Preflight: []skillwrite.Check{{
			ID: "before", MSQL: "SELECT row_id FROM work.notes WHERE title = :title LIMIT 1",
			Input: executor.StatementInput{Parameters: executor.Parameters{Named: map[string]any{"title": title}}}, ExpectRows: &zero,
		}},
		Steps: []skillwrite.Step{{
			ID: "insert", Kind: "INSERT", Target: moduleID,
			MSQL: "INSERT INTO work.notes (title) VALUES (:title)",
			Input: executor.StatementInput{
				Parameters: executor.Parameters{Named: map[string]any{"title": title}},
				Mutation: executor.MutationOptions{
					ExpectedSchemaVersion: 1, MaxAffectedRows: 1,
					Actor: "agent:host", Source: eventID, Reason: reason,
					RouteLeafIDs: []string{},
				},
			},
		}},
		Verify: []skillwrite.Check{{
			ID: "after", MSQL: "SELECT title, row_id, revision FROM work.notes WHERE title = :title LIMIT 1",
			Input:      executor.StatementInput{Parameters: executor.Parameters{Named: map[string]any{"title": title}}},
			ExpectRows: &one, RowContains: map[string]any{"title": title},
		}},
	}
}

func daemonRelationPlan(eventID string) skillwrite.Plan {
	one := 1
	reason := "absorb reviewed fixture book"
	return skillwrite.Plan{
		Version: skillwrite.PlanVersion, ID: "plan-supports", Decision: skillwrite.DecisionRelate,
		Database: "work", Table: "notes", Actor: "agent:host", SourceEventID: eventID,
		Reason: reason, AuthorizedDatabases: []string{"work"},
		Preflight: []skillwrite.Check{{
			ID: "before", MSQL: "SELECT row_id FROM work.notes WHERE row_id = :row LIMIT 1",
			Input: executor.StatementInput{Parameters: executor.Parameters{Named: map[string]any{"row": "module-one"}}}, ExpectRows: &one,
		}},
		Steps: []skillwrite.Step{{
			ID: "relate", Kind: "RELATE", Target: "module-one->module-two",
			MSQL: "RELATE work.notes ROW :source TO work.notes ROW :target TYPE :type",
			Input: executor.StatementInput{
				Parameters: executor.Parameters{Named: map[string]any{"source": "module-one", "target": "module-two", "type": "supports"}},
				Mutation:   executor.MutationOptions{MaxAffectedRows: 1, Actor: "agent:host", Source: eventID, Reason: reason},
			},
		}},
		Verify: []skillwrite.Check{{
			ID: "after", MSQL: "SHOW RELATIONS FROM work.notes FOR ROW :row DIRECTION OUTGOING LIMIT 10",
			Input: executor.StatementInput{Parameters: executor.Parameters{Named: map[string]any{"row": "module-one"}}}, ExpectRows: &one,
		}},
	}
}

func invokeSubmission(
	t *testing.T,
	ctx context.Context,
	handler *databaseHandler,
	sessionID string,
	submission assimilation.Submission,
) assimilation.SourceReceipt {
	t.Helper()
	payload, err := json.Marshal(submission)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := handler.Handle(ctx, ipc.Session{ID: sessionID}, ipc.Request{
		Version: ipc.Version, RequestID: submission.SubmissionID, Method: "assimilation.submit", Payload: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	var receipt assimilation.SourceReceipt
	if err := json.Unmarshal(encoded, &receipt); err != nil {
		t.Fatal(err)
	}
	return receipt
}

func invokeAssimilation(
	t *testing.T,
	ctx context.Context,
	handler *databaseHandler,
	sessionID string,
	event assimilation.Event,
) assimilation.Receipt {
	t.Helper()
	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := handler.Handle(ctx, ipc.Session{ID: sessionID}, ipc.Request{
		Version: ipc.Version, RequestID: event.EventID, Method: "assimilation.record", Payload: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	var receipt assimilation.Receipt
	if err := json.Unmarshal(encoded, &receipt); err != nil {
		t.Fatal(err)
	}
	return receipt
}

func daemonDigest(character string) string {
	return "sha256:" + strings.Repeat(character, 64)
}
