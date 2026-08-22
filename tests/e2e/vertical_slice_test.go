//go:build e2e

package e2e_test

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/feedback"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/semantichealth"
	"github.com/HW-Yue/Memora/internal/skillwrite"
)

func TestLocalDatabaseVerticalSliceThroughCLIAndDaemon(t *testing.T) {
	root := e2eRepositoryRoot(t)
	temporary := t.TempDir()
	binary := filepath.Join(temporary, "memora")
	e2eCommand(t, root, "go", "build", "-o", binary, "./cmd/memora")
	dataDir := filepath.Join(temporary, "instance")
	e2eCommand(t, root, binary, "init", "--data-dir", dataDir)
	e2eCommand(t, root, binary, "daemon", "start", "--data-dir", dataDir)
	defer func() {
		_, _ = e2eRun(root, binary, "daemon", "stop", "--data-dir", dataDir)
	}()

	var initialDoctor doctorOutput
	e2eJSON(t, root, &initialDoctor, binary, "doctor", "--data-dir", dataDir)
	if initialDoctor.Status != "healthy" || initialDoctor.Databases != 0 {
		t.Fatalf("initial doctor = %#v", initialDoctor)
	}

	ddl := e2eEnvelope(t, root, binary, "exec", "--data-dir", dataDir,
		"CREATE DATABASE work PURPOSE 'Project knowledge' SCOPE 'Reviewed projects'; "+
			"CREATE TABLE work.notes PURPOSE 'Durable notes' ROW SEMANTICS 'One reviewed note' "+
			"(title TEXT(2000) NOT NULL PURPOSE 'Note title')")
	if len(ddl.Results) != 2 || len(ddl.Results[0].Rows) != 1 {
		t.Fatalf("DDL envelope = %#v", ddl)
	}
	if databaseID, _ := ddl.Results[0].Rows[0]["database_id"].(string); databaseID == "" {
		t.Fatalf("DDL database identity = %#v", ddl.Results[0].Rows)
	}
	compactTable := e2eEnvelope(t, root, binary, "query", "--data-dir", dataDir,
		"DESCRIBE TABLE work.notes COMPACT")
	summaries, _ := compactTable.Results[0].Rows[0]["column_summaries"].([]any)
	columns, _ := compactTable.Results[0].Rows[0]["columns"].([]any)
	if len(summaries) != 1 || len(columns) != 0 {
		t.Fatalf("compact Table schema = %#v", compactTable)
	}
	rootRoute := e2eEnvelope(t, root, binary, "exec", "--data-dir", dataDir,
		"--input", statementInput(nil, map[string]any{"max_affected_rows": 1}),
		"CREATE ROUTE ROOT FOR TABLE work.notes PURPOSE 'Notes Router'")
	rootID, _ := rootRoute.Results[0].Rows[0]["route_id"].(string)
	branchRoute := e2eEnvelope(t, root, binary, "exec", "--data-dir", dataDir,
		"--input", statementInput(map[string]any{
			"parent": rootID, "name": "architecture", "kind": "branch",
			"purpose":  "Architecture knowledge",
			"synopsis": "Contains current private decisions about storage, manifests, and atomic publication; excludes unrelated project administration.",
		}, map[string]any{"max_affected_rows": 1}),
		"CREATE ROUTE UNDER :parent NAME :name KIND :kind PURPOSE :purpose SYNOPSIS :synopsis")
	branchID, _ := branchRoute.Results[0].Rows[0]["route_id"].(string)
	leafRoute := e2eEnvelope(t, root, binary, "exec", "--data-dir", dataDir,
		"--input", statementInput(map[string]any{"parent": branchID, "name": "storage", "kind": "leaf", "purpose": "Storage decisions"}, map[string]any{"max_affected_rows": 1}),
		"CREATE ROUTE UNDER :parent NAME :name KIND :kind PURPOSE :purpose")
	leafID, _ := leafRoute.Results[0].Rows[0]["route_id"].(string)

	inserted := e2eEnvelope(t, root, binary, "exec", "--data-dir", dataDir,
		"--input", statementInput(nil, map[string]any{
			"expected_schema_version": 1, "max_affected_rows": 1,
			"route_leaf_ids": []string{leafID},
			"actor":          "agent:e2e", "source": "e2e:insert", "reason": "capture decision",
		}),
		"INSERT INTO work.notes (title) VALUES ('generation manifest')")
	if inserted.Results[0].Revision == nil || *inserted.Results[0].Revision != 1 {
		t.Fatalf("INSERT envelope = %#v", inserted)
	}

	selected := e2eEnvelope(t, root, binary, "query", "--data-dir", dataDir,
		"SELECT row_id, title, revision FROM work.notes LIMIT 10")
	if len(selected.Results[0].Rows) != 1 ||
		selected.Results[0].Rows[0]["title"] != "generation manifest" {
		t.Fatalf("SELECT envelope = %#v", selected)
	}
	rowID, _ := selected.Results[0].Rows[0]["row_id"].(string)
	top := e2eEnvelope(t, root, binary, "query", "--data-dir", dataDir,
		"SHOW ROUTES FROM TABLE work.notes AT ROOT LIMIT 12")
	children := e2eEnvelope(t, root, binary, "query", "--data-dir", dataDir,
		"--input", statementInput(map[string]any{"route": branchID}, nil),
		"SHOW ROUTES UNDER :route LIMIT 12")
	opened := e2eEnvelope(t, root, binary, "query", "--data-dir", dataDir,
		"--input", statementInput(map[string]any{"leaf": leafID}, nil),
		"OPEN ROUTE :leaf LIMIT 1")
	if len(top.Results[0].Rows) != 1 || top.Results[0].Rows[0]["route_id"] != branchID ||
		len(children.Results[0].Rows) != 1 || children.Results[0].Rows[0]["route_id"] != leafID ||
		len(opened.Results[0].Rows) != 1 || opened.Results[0].Rows[0]["row_id"] != rowID {
		t.Fatalf("Table Router navigation = top %#v, children %#v, open %#v", top, children, opened)
	}
	for name, statement := range map[string]result.StatementResult{
		"top": top.Results[0], "children": children.Results[0], "open": opened.Results[0],
	} {
		if statement.Page == nil || statement.Page.Version != result.ListPageVersion ||
			statement.Page.Snapshot == "" || statement.Page.Truncated || statement.Page.NextCursor != "" {
			t.Fatalf("%s Route page = %#v", name, statement.Page)
		}
	}
	if _, leaked := top.Results[0].Rows[0]["synopsis"]; leaked {
		t.Fatalf("default Route frame leaked on-demand synopsis = %#v", top)
	}
	describedBranch := e2eEnvelope(t, root, binary, "query", "--data-dir", dataDir,
		"--input", statementInput(map[string]any{"route": branchID}, nil),
		"DESCRIBE ROUTE :route")
	if len(describedBranch.Results[0].Rows) != 1 ||
		describedBranch.Results[0].Rows[0]["synopsis"] != "Contains current private decisions about storage, manifests, and atomic publication; excludes unrelated project administration." {
		t.Fatalf("DESCRIBE ROUTE = %#v", describedBranch)
	}
	updatedSynopsis := e2eEnvelope(t, root, binary, "exec", "--data-dir", dataDir,
		"--input", statementInput(
			map[string]any{
				"route":    branchID,
				"synopsis": "Current private architecture decisions, their accepted boundaries, and storage publication constraints.",
			},
			map[string]any{"expected_revision": 1, "max_affected_rows": 1},
		),
		"ALTER ROUTE :route SET SYNOPSIS :synopsis")
	if updatedSynopsis.Results[0].Revision == nil || *updatedSynopsis.Results[0].Revision != 2 {
		t.Fatalf("ALTER ROUTE synopsis = %#v", updatedSynopsis)
	}

	expectedRow := 1
	mutationPlan := skillwrite.Plan{
		Version: skillwrite.PlanVersion, ID: "e2e-revise", Decision: skillwrite.DecisionRevise,
		Database: "work", Table: "notes", Actor: "agent:e2e",
		SourceEventID: "e2e:update", Reason: "refine decision",
		AuthorizedDatabases: []string{"work"},
		Preflight: []skillwrite.Check{{
			ID: "current", MSQL: "SELECT title, row_id, revision FROM work.notes WHERE row_id = :row LIMIT 1",
			Input:      executor.StatementInput{Parameters: executor.Parameters{Named: map[string]any{"row": rowID}}},
			ExpectRows: &expectedRow, RowContains: map[string]any{"title": "generation manifest"},
		}},
		Steps: []skillwrite.Step{{
			ID: "revise", Kind: "UPDATE", Target: rowID,
			MSQL: "UPDATE work.notes SET title = :title WHERE row_id = :row",
			Input: executor.StatementInput{
				Parameters: executor.Parameters{Named: map[string]any{
					"row": rowID, "title": "atomic generation manifest",
				}},
				Mutation: executor.MutationOptions{
					ExpectedSchemaVersion: 1, ExpectedRevision: 1, MaxAffectedRows: 1,
					RouteLeafIDs: []string{leafID},
					Actor:        "agent:e2e", Source: "e2e:update", Reason: "refine decision",
				},
			},
		}},
		Verify: []skillwrite.Check{{
			ID: "updated", MSQL: "SELECT title, row_id, revision FROM work.notes WHERE row_id = :row LIMIT 1",
			Input:      executor.StatementInput{Parameters: executor.Parameters{Named: map[string]any{"row": rowID}}},
			ExpectRows: &expectedRow, RowContains: map[string]any{"title": "atomic generation manifest"},
		}},
	}
	encodedPlan, err := json.Marshal(mutationPlan)
	if err != nil {
		t.Fatal(err)
	}
	var mutationReceipt skillwrite.Receipt
	e2eJSON(t, root, &mutationReceipt, binary,
		"mutate", "--data-dir", dataDir, "--plan", string(encodedPlan))
	if mutationReceipt.Status != skillwrite.ReceiptCommitted ||
		!mutationReceipt.Verified || len(mutationReceipt.Changes) != 1 ||
		mutationReceipt.Changes[0].Revision == nil || *mutationReceipt.Changes[0].Revision != 2 {
		t.Fatalf("Mutation Receipt = %#v", mutationReceipt)
	}
	history := e2eEnvelope(t, root, binary, "query", "--data-dir", dataDir,
		"--input", statementInput(map[string]any{"row": rowID}, nil),
		"SHOW HISTORY FROM work.notes FOR ROW :row LIMIT 10")
	if len(history.Results[0].Rows) != 2 ||
		history.Results[0].Rows[0]["revision"] != float64(2) ||
		history.Results[0].Rows[0]["source_kind"] != "conversation_assertion" ||
		history.Results[0].Rows[0]["source_receipt_id"] != "" {
		t.Fatalf("SHOW HISTORY envelope = %#v", history)
	}
	openedAfterUpdate := e2eEnvelope(t, root, binary, "query", "--data-dir", dataDir,
		"--input", statementInput(map[string]any{"leaf": leafID}, nil),
		"OPEN ROUTE :leaf LIMIT 1")
	if len(openedAfterUpdate.Results[0].Rows) != 1 ||
		openedAfterUpdate.Results[0].Rows[0]["row_id"] != rowID ||
		openedAfterUpdate.Results[0].Rows[0]["revision"] != float64(2) {
		t.Fatalf("Route after UPDATE = %#v", openedAfterUpdate)
	}

	e2eCommand(t, root, binary, "daemon", "stop", "--data-dir", dataDir)
	e2eCommand(t, root, binary, "daemon", "start", "--data-dir", dataDir)
	reopened := e2eEnvelope(t, root, binary, "query", "--data-dir", dataDir,
		"--input", statementInput(map[string]any{"row": rowID}, nil),
		"SELECT row_id, title, revision FROM work.notes WHERE row_id = :row LIMIT 1")
	if len(reopened.Results[0].Rows) != 1 ||
		reopened.Results[0].Rows[0]["title"] != "atomic generation manifest" ||
		reopened.Results[0].Rows[0]["revision"] != float64(2) {
		t.Fatalf("reopened SELECT = %#v", reopened)
	}
	reopenedRoute := e2eEnvelope(t, root, binary, "query", "--data-dir", dataDir,
		"--input", statementInput(map[string]any{"leaf": leafID}, nil),
		"OPEN ROUTE :leaf LIMIT 1")
	if len(reopenedRoute.Results[0].Rows) != 1 ||
		reopenedRoute.Results[0].Rows[0]["row_id"] != rowID ||
		reopenedRoute.Results[0].Rows[0]["revision"] != float64(2) {
		t.Fatalf("reopened Route = %#v", reopenedRoute)
	}
	// RESTORE rewinds a live Row to an earlier revision. It is not a way back
	// from DELETE — deletion is final, which is proven on its own Row below.
	restored := e2eEnvelope(t, root, binary, "exec", "--data-dir", dataDir,
		"--input", statementInput(
			map[string]any{"row": rowID, "revision": 1},
			map[string]any{
				"expected_schema_version": 1, "expected_revision": 2, "max_affected_rows": 1,
				"route_leaf_ids": []string{leafID},
				"actor":          "agent:e2e", "source": "e2e:restore", "reason": "verify compensation",
			},
		),
		"RESTORE work.notes ROW :row TO REVISION :revision")
	restoredRoute := e2eEnvelope(t, root, binary, "query", "--data-dir", dataDir,
		"--input", statementInput(map[string]any{"leaf": leafID}, nil),
		"OPEN ROUTE :leaf LIMIT 1")
	if restored.Results[0].Revision == nil || *restored.Results[0].Revision != 3 ||
		len(restoredRoute.Results[0].Rows) != 1 ||
		restoredRoute.Results[0].Rows[0]["row_id"] != rowID ||
		restoredRoute.Results[0].Rows[0]["revision"] != float64(3) {
		t.Fatalf("Route after RESTORE = restore %#v, route %#v", restored, restoredRoute)
	}
	verifyDeletionIsFinal(t, root, binary, dataDir, branchID)
	feedbackEvent := feedback.Event{
		Version: feedback.EventVersion, EventID: "e2e-feedback-wrong",
		Kind: feedback.KindWrong, Actor: "user:e2e",
		Reason: "the restored wording is not the confirmed revision",
		Target: feedback.Target{Database: "work", Table: "notes", RowID: rowID, Revision: 3},
	}
	encodedFeedback, err := json.Marshal(feedbackEvent)
	if err != nil {
		t.Fatal(err)
	}
	var feedbackReceipt feedback.Receipt
	e2eJSON(t, root, &feedbackReceipt, binary,
		"feedback", "--data-dir", dataDir, "--event", string(encodedFeedback))
	if feedbackReceipt.Status != "recorded" || feedbackReceipt.Target.Revision != 3 {
		t.Fatalf("Feedback Receipt = %#v", feedbackReceipt)
	}
	confirmation := feedback.Confirmation{
		Version: feedback.ConfirmationVersion, ConfirmationID: "e2e-confirm-undo",
		FeedbackEventID: feedbackEvent.EventID, SourceEventID: "e2e:user-confirmation",
		Actor: "agent:e2e", Instruction: "restore the last confirmed wording",
		Action: feedback.ActionUndo, ExpectedRevision: 3,
		AuthorizedDatabases: []string{"work"},
		Undo: &feedback.Undo{
			TargetRevision: 2, ExpectedSchemaVersion: 1, RouteLeafIDs: []string{leafID},
		},
	}
	encodedConfirmation, err := json.Marshal(confirmation)
	if err != nil {
		t.Fatal(err)
	}
	var confirmationReceipt feedback.ConfirmationReceipt
	e2eJSON(t, root, &confirmationReceipt, binary,
		"feedback", "--data-dir", dataDir, "--confirmation", string(encodedConfirmation))
	if confirmationReceipt.Status != "confirmed" || !confirmationReceipt.Verified ||
		confirmationReceipt.NewRevision == nil || *confirmationReceipt.NewRevision != 4 ||
		confirmationReceipt.RestoredRevision == nil || *confirmationReceipt.RestoredRevision != 2 {
		t.Fatalf("Feedback Confirmation Receipt = %#v", confirmationReceipt)
	}
	openedAfterFeedback := e2eEnvelope(t, root, binary, "query", "--data-dir", dataDir,
		"--input", statementInput(map[string]any{"leaf": leafID}, nil),
		"OPEN ROUTE :leaf LIMIT 1")
	if len(openedAfterFeedback.Results[0].Rows) != 1 ||
		openedAfterFeedback.Results[0].Rows[0]["row_id"] != rowID ||
		openedAfterFeedback.Results[0].Rows[0]["revision"] != float64(4) {
		t.Fatalf("Route after feedback undo = %#v", openedAfterFeedback)
	}
	var health semantichealth.Report
	e2eJSON(t, root, &health, binary, "maintain", "--data-dir", dataDir, "--report")
	if health.Status != "healthy" || health.Hash == "" || health.IssueCount != 0 {
		t.Fatalf("native semantic health = %#v", health)
	}
	anchorLeaf := e2eEnvelope(t, root, binary, "exec", "--data-dir", dataDir,
		"--input", statementInput(
			map[string]any{"parent": branchID, "name": "relation-anchor", "kind": "leaf", "purpose": "Relation anchor Row"},
			map[string]any{"max_affected_rows": 1},
		),
		"CREATE ROUTE UNDER :parent NAME :name KIND :kind PURPOSE :purpose")
	anchorLeafID, _ := anchorLeaf.Results[0].Rows[0]["route_id"].(string)
	secondLeaf := e2eEnvelope(t, root, binary, "exec", "--data-dir", dataDir,
		"--input", statementInput(
			map[string]any{"parent": branchID, "name": "manifest-generation", "kind": "leaf", "purpose": "Manifest generation Row"},
			map[string]any{"max_affected_rows": 1},
		),
		"CREATE ROUTE UNDER :parent NAME :name KIND :kind PURPOSE :purpose")
	secondLeafID, _ := secondLeaf.Results[0].Rows[0]["route_id"].(string)
	anchor := e2eEnvelope(t, root, binary, "exec", "--data-dir", dataDir,
		"--input", statementInput(nil, map[string]any{
			"expected_schema_version": 1, "max_affected_rows": 1,
			"route_leaf_ids": []string{anchorLeafID},
			"actor":          "agent:e2e", "source": "e2e:anchor", "reason": "reshape relation anchor",
		}),
		"INSERT INTO work.notes (title) VALUES ('reshape relation anchor')")
	anchorID, _ := anchor.Results[0].Rows[0]["row_id"].(string)
	related := e2eEnvelope(t, root, binary, "exec", "--data-dir", dataDir,
		"--input", statementInput(
			map[string]any{"source": rowID, "target": anchorID, "type": "supports"},
			map[string]any{"expected_schema_version": 1, "max_affected_rows": 1},
		),
		"RELATE work.notes ROW :source TO work.notes ROW :target TYPE :type")
	relationID, _ := related.Results[0].Rows[0]["relation_id"].(string)
	split := e2eEnvelope(t, root, binary, "exec", "--data-dir", dataDir,
		"--input", statementInput(
			map[string]any{
				"source": rowID, "first": "manifest atomicity", "second": "manifest generation",
			},
			map[string]any{
				"expected_schema_version": 1, "expected_revision": 4, "max_affected_rows": 3,
				"target_route_leaf_ids":    [][]string{{leafID}, {secondLeafID}},
				"relation_target_ordinals": map[string]int{relationID: 1},
				"route_updates": []map[string]any{{
					"route_id": branchID, "expected_revision": 2,
					"purpose":  "Architecture knowledge split into atomic subjects",
					"synopsis": "Two current private architecture subjects with separate semantic boundaries and one explicit relation owner.",
				}},
				"actor": "agent:e2e", "source": "e2e:split", "reason": "separate semantic subjects",
			},
		),
		"SPLIT work.notes ROW :source INTO (title) VALUES (:first), (:second)")
	if split.Results[0].AffectedRows != 3 || len(split.Results[0].Rows) != 2 {
		t.Fatalf("SPLIT envelope = %#v", split)
	}
	firstID, _ := split.Results[0].Rows[0]["row_id"].(string)
	secondID, _ := split.Results[0].Rows[1]["row_id"].(string)
	splitRoute := e2eEnvelope(t, root, binary, "query", "--data-dir", dataDir,
		"--input", statementInput(map[string]any{"leaf": leafID}, nil),
		"OPEN ROUTE :leaf LIMIT 1")
	if !routeContainsRevision(splitRoute, firstID, 1) ||
		routeContainsRevision(splitRoute, secondID, 1) ||
		routeContainsRevision(splitRoute, rowID, 6) {
		t.Fatalf("Route after SPLIT = %#v", splitRoute)
	}
	secondSplitRoute := e2eEnvelope(t, root, binary, "query", "--data-dir", dataDir,
		"--input", statementInput(map[string]any{"leaf": secondLeafID}, nil),
		"OPEN ROUTE :leaf LIMIT 1")
	if !routeContainsRevision(secondSplitRoute, secondID, 1) ||
		routeContainsRevision(secondSplitRoute, firstID, 1) {
		t.Fatalf("second Route after SPLIT = %#v", secondSplitRoute)
	}
	branchAfterSplit := e2eEnvelope(t, root, binary, "query", "--data-dir", dataDir,
		"SHOW ROUTES FROM TABLE work.notes AT ROOT LIMIT 12")
	if len(branchAfterSplit.Results[0].Rows) != 1 ||
		branchAfterSplit.Results[0].Rows[0]["revision"] != float64(3) ||
		branchAfterSplit.Results[0].Rows[0]["purpose"] != "Architecture knowledge split into atomic subjects" {
		t.Fatalf("upper Route after SPLIT = %#v", branchAfterSplit)
	}
	inheritedRelation := e2eEnvelope(t, root, binary, "query", "--data-dir", dataDir,
		"--input", statementInput(map[string]any{"row": firstID}, nil),
		"SHOW RELATIONS FROM work.notes FOR ROW :row DIRECTION OUTGOING LIMIT 10")
	if len(inheritedRelation.Results[0].Rows) != 1 ||
		inheritedRelation.Results[0].Rows[0]["target_row_id"] != anchorID {
		t.Fatalf("relation after SPLIT = %#v", inheritedRelation)
	}
	merge := e2eEnvelope(t, root, binary, "exec", "--data-dir", dataDir,
		"--input", statementInput(
			map[string]any{
				"first": firstID, "second": secondID, "merged": "atomic manifest generation",
			},
			map[string]any{
				"expected_schema_version": 1, "max_affected_rows": 3,
				"source_revisions":      map[string]uint64{firstID: 1, secondID: 1},
				"target_route_leaf_ids": [][]string{{leafID}},
				"route_updates": []map[string]any{{
					"route_id": branchID, "expected_revision": 3,
					"purpose":  "Architecture knowledge with verified semantic boundaries",
					"synopsis": "Current private architecture knowledge after verified recombination; source Rows remain only in History.",
				}},
				"actor": "agent:e2e", "source": "e2e:merge", "reason": "recombine verified boundary",
			},
		),
		"MERGE work.notes ROWS (:first, :second) INTO (title) VALUES (:merged)")
	if merge.Results[0].AffectedRows != 3 || len(merge.Results[0].Rows) != 1 {
		t.Fatalf("MERGE envelope = %#v", merge)
	}
	mergedID, _ := merge.Results[0].Rows[0]["row_id"].(string)
	mergedRoute := e2eEnvelope(t, root, binary, "query", "--data-dir", dataDir,
		"--input", statementInput(map[string]any{"leaf": leafID}, nil),
		"OPEN ROUTE :leaf LIMIT 1")
	if !routeContainsRevision(mergedRoute, mergedID, 1) ||
		routeContainsRevision(mergedRoute, firstID, 2) ||
		routeContainsRevision(mergedRoute, secondID, 2) {
		t.Fatalf("Route after MERGE = %#v", mergedRoute)
	}
	emptySecondRoute := e2eEnvelope(t, root, binary, "query", "--data-dir", dataDir,
		"--input", statementInput(map[string]any{"leaf": secondLeafID}, nil),
		"OPEN ROUTE :leaf LIMIT 1")
	if len(emptySecondRoute.Results[0].Rows) != 0 {
		t.Fatalf("second Route after MERGE = %#v", emptySecondRoute)
	}
	branchAfterMerge := e2eEnvelope(t, root, binary, "query", "--data-dir", dataDir,
		"SHOW ROUTES FROM TABLE work.notes AT ROOT LIMIT 12")
	if len(branchAfterMerge.Results[0].Rows) != 1 ||
		branchAfterMerge.Results[0].Rows[0]["revision"] != float64(4) ||
		branchAfterMerge.Results[0].Rows[0]["purpose"] != "Architecture knowledge with verified semantic boundaries" {
		t.Fatalf("upper Route after MERGE = %#v", branchAfterMerge)
	}
	describedAfterMerge := e2eEnvelope(t, root, binary, "query", "--data-dir", dataDir,
		"--input", statementInput(map[string]any{"route": branchID}, nil),
		"DESCRIBE ROUTE :route")
	if describedAfterMerge.Results[0].Rows[0]["revision"] != float64(4) ||
		describedAfterMerge.Results[0].Rows[0]["synopsis"] != "Current private architecture knowledge after verified recombination; source Rows remain only in History." {
		t.Fatalf("on-demand synopsis after MERGE = %#v", describedAfterMerge)
	}
	mergedRelation := e2eEnvelope(t, root, binary, "query", "--data-dir", dataDir,
		"--input", statementInput(map[string]any{"row": mergedID}, nil),
		"SHOW RELATIONS FROM work.notes FOR ROW :row DIRECTION OUTGOING LIMIT 10")
	if len(mergedRelation.Results[0].Rows) != 1 ||
		mergedRelation.Results[0].Rows[0]["target_row_id"] != anchorID {
		t.Fatalf("relation after MERGE = %#v", mergedRelation)
	}
	initialConfiguration := e2eEnvelope(t, root, binary, "query", "--data-dir", dataDir,
		"SHOW CONFIGURATION")
	if initialConfiguration.Results[0].Rows[0]["revision"] != float64(1) ||
		initialConfiguration.Results[0].Rows[0]["select_rows"] != float64(10) {
		t.Fatalf("initial database configuration = %#v", initialConfiguration)
	}
	tightenedConfiguration := e2eEnvelope(t, root, binary, "exec", "--data-dir", dataDir,
		"--input", statementInput(nil, map[string]any{
			"expected_revision": 1, "actor": "agent:e2e", "reason": "exercise persisted query budgets",
		}),
		"ALTER CONFIGURATION QUERY_BUDGETS SET ROUTE_CHILDREN 1, OPEN_LOCATORS 1, "+
			"SELECT_SCAN 2, SELECT_ROWS 1, ROUTE_FRAME_NODES 2")
	if tightenedConfiguration.Results[0].Revision == nil ||
		*tightenedConfiguration.Results[0].Revision != 2 {
		t.Fatalf("tightened database configuration = %#v", tightenedConfiguration)
	}
	failedOutput, failedErr := e2eRun(root, binary, "query", "--data-dir", dataDir,
		"SELECT row_id FROM work.notes LIMIT 2")
	var budgetFailure result.Envelope
	if failedErr == nil || json.Unmarshal([]byte(failedOutput), &budgetFailure) != nil ||
		budgetFailure.OK || len(budgetFailure.Results) != 1 ||
		budgetFailure.Results[0].Error == nil ||
		budgetFailure.Results[0].Error.Code != result.CodeValidation {
		t.Fatalf("configured SELECT budget was not enforced: err=%v output=%s", failedErr, failedOutput)
	}
	e2eCommand(t, root, binary, "daemon", "stop", "--data-dir", dataDir)
	e2eCommand(t, root, binary, "daemon", "start", "--data-dir", dataDir)
	reopenedConfiguration := e2eEnvelope(t, root, binary, "query", "--data-dir", dataDir,
		"SHOW CONFIGURATION")
	if reopenedConfiguration.Results[0].Rows[0]["revision"] != float64(2) ||
		reopenedConfiguration.Results[0].Rows[0]["select_rows"] != float64(1) {
		t.Fatalf("reopened database configuration = %#v", reopenedConfiguration)
	}
	restoredConfiguration := e2eEnvelope(t, root, binary, "exec", "--data-dir", dataDir,
		"--input", statementInput(map[string]any{"revision": 1}, map[string]any{
			"expected_revision": 2, "actor": "agent:e2e", "reason": "restore accepted defaults",
		}),
		"RESTORE CONFIGURATION QUERY_BUDGETS TO REVISION :revision")
	if restoredConfiguration.Results[0].Revision == nil ||
		*restoredConfiguration.Results[0].Revision != 3 ||
		restoredConfiguration.Results[0].Rows[0]["restored_revision"] != float64(1) {
		t.Fatalf("restored database configuration = %#v", restoredConfiguration)
	}
	e2eEnvelope(t, root, binary, "query", "--data-dir", dataDir,
		"SELECT row_id FROM work.notes LIMIT 10")
	var doctor doctorOutput
	e2eJSON(t, root, &doctor, binary, "doctor", "--data-dir", dataDir)
	// doctor counts the logical snapshot export, and AllRows/AllHistory do not
	// filter by state — a deleted Row and its History still export. So the Row
	// retired by verifyDeletionIsFinal adds one Row and two History entries
	// (insert, delete), while the main Row lost one revision when DELETE left
	// its lifecycle.
	if doctor.Status != "healthy" || doctor.Databases != 1 ||
		doctor.Rows != 6 || doctor.History != 13 || doctor.Relations != 3 ||
		doctor.SnapshotHash == "" {
		t.Fatalf("final doctor = %#v", doctor)
	}
}

type doctorOutput struct {
	Status       string `json:"status"`
	Databases    int    `json:"databases"`
	Rows         int    `json:"rows"`
	History      int    `json:"history"`
	Relations    int    `json:"relations"`
	SnapshotHash string `json:"snapshot_hash"`
}

func statementInput(parameters, mutation map[string]any) string {
	input := map[string]any{}
	if parameters != nil {
		input["parameters"] = map[string]any{"named": parameters}
	}
	if mutation != nil {
		input["mutation"] = mutation
	}
	encoded, _ := json.Marshal(input)
	return string(encoded)
}

func routeContainsRevision(envelope result.Envelope, rowID string, revision float64) bool {
	if len(envelope.Results) != 1 {
		return false
	}
	for _, value := range envelope.Results[0].Rows {
		if value["row_id"] == rowID && value["revision"] == revision {
			return true
		}
	}
	return false
}

// verifyDeletionIsFinal proves the DELETE contract end to end: the Route Leaf
// empties out, and RESTORE refuses to bring the Row back.
//
// It uses a Row created for this and nothing else. The slice's main Row has to
// stay live for SPLIT/MERGE and the post-restart reads, and deletion is final,
// so the two cannot be the same Row.
func verifyDeletionIsFinal(t *testing.T, root, binary, dataDir, branchID string) {
	t.Helper()
	leaf := e2eEnvelope(t, root, binary, "exec", "--data-dir", dataDir,
		"--input", statementInput(
			map[string]any{
				"parent": branchID, "name": "retired-note", "kind": "leaf",
				"purpose": "Row retired to prove deletion is final",
			},
			map[string]any{"max_affected_rows": 1},
		),
		"CREATE ROUTE UNDER :parent NAME :name KIND :kind PURPOSE :purpose")
	leafID, _ := leaf.Results[0].Rows[0]["route_id"].(string)
	inserted := e2eEnvelope(t, root, binary, "exec", "--data-dir", dataDir,
		"--input", statementInput(nil, map[string]any{
			"expected_schema_version": 1, "max_affected_rows": 1,
			"route_leaf_ids": []string{leafID},
			"actor":          "agent:e2e", "source": "e2e:retire", "reason": "delete subject",
		}),
		"INSERT INTO work.notes (title) VALUES ('retired manifest')")
	doomedID, _ := inserted.Results[0].Rows[0]["row_id"].(string)

	e2eEnvelope(t, root, binary, "exec", "--data-dir", dataDir,
		"--input", statementInput(
			map[string]any{"row": doomedID},
			map[string]any{
				"expected_schema_version": 1, "expected_revision": 1, "max_affected_rows": 1,
				"actor": "agent:e2e", "source": "e2e:delete", "reason": "verify deletion is final",
			},
		),
		"DELETE FROM work.notes WHERE row_id = :row")
	emptied := e2eEnvelope(t, root, binary, "query", "--data-dir", dataDir,
		"--input", statementInput(map[string]any{"leaf": leafID}, nil),
		"OPEN ROUTE :leaf LIMIT 1")
	if len(emptied.Results[0].Rows) != 0 {
		t.Fatalf("Route after DELETE = %#v", emptied)
	}

	output, err := e2eRun(root, binary, "exec", "--data-dir", dataDir,
		"--input", statementInput(
			map[string]any{"row": doomedID, "revision": 1},
			map[string]any{
				"expected_schema_version": 1, "expected_revision": 2, "max_affected_rows": 1,
				"actor": "agent:e2e", "source": "e2e:resurrect", "reason": "deletion must be final",
			},
		),
		"RESTORE work.notes ROW :row TO REVISION :revision")
	if err == nil {
		t.Fatalf("RESTORE must not resurrect a deleted Row, got: %s", output)
	}
	if !strings.Contains(output, "deletion is final") {
		t.Fatalf("the refusal should say deletion is final, got: %s", output)
	}
}

func e2eEnvelope(
	t *testing.T,
	directory, binary, command string,
	args ...string,
) result.Envelope {
	t.Helper()
	var envelope result.Envelope
	e2eJSON(t, directory, &envelope, binary, append([]string{command}, args...)...)
	if !envelope.OK {
		t.Fatalf("%s envelope failed: %#v", command, envelope)
	}
	return envelope
}

func e2eJSON(
	t *testing.T,
	directory string,
	target any,
	name string,
	args ...string,
) {
	t.Helper()
	output := e2eCommand(t, directory, name, args...)
	if err := json.Unmarshal([]byte(output), target); err != nil {
		t.Fatalf("decode %s %v: %v\n%s", name, args, err, output)
	}
}

func e2eCommand(t *testing.T, directory, name string, args ...string) string {
	t.Helper()
	output, err := e2eRun(directory, name, args...)
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, output)
	}
	return output
}

func e2eRun(directory, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	return string(output), err
}

func e2eRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve E2E test file")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
}
