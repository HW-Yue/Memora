//go:build e2e

package e2e_test

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"runtime"
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
	rootRoute := e2eEnvelope(t, root, binary, "exec", "--data-dir", dataDir,
		"--input", statementInput(nil, map[string]any{"max_affected_rows": 1}),
		"CREATE ROUTE ROOT FOR TABLE work.notes PURPOSE 'Notes Router'")
	rootID, _ := rootRoute.Results[0].Rows[0]["route_id"].(string)
	branchRoute := e2eEnvelope(t, root, binary, "exec", "--data-dir", dataDir,
		"--input", statementInput(map[string]any{"parent": rootID, "name": "architecture", "kind": "branch", "purpose": "Architecture knowledge"}, map[string]any{"max_affected_rows": 1}),
		"CREATE ROUTE UNDER :parent NAME :name KIND :kind PURPOSE :purpose")
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
		"OPEN ROUTE :leaf LIMIT 20")
	if len(top.Results[0].Rows) != 1 || top.Results[0].Rows[0]["route_id"] != branchID ||
		len(children.Results[0].Rows) != 1 || children.Results[0].Rows[0]["route_id"] != leafID ||
		len(opened.Results[0].Rows) != 1 || opened.Results[0].Rows[0]["row_id"] != rowID {
		t.Fatalf("Table Router navigation = top %#v, children %#v, open %#v", top, children, opened)
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
		history.Results[0].Rows[0]["revision"] != float64(2) {
		t.Fatalf("SHOW HISTORY envelope = %#v", history)
	}
	openedAfterUpdate := e2eEnvelope(t, root, binary, "query", "--data-dir", dataDir,
		"--input", statementInput(map[string]any{"leaf": leafID}, nil),
		"OPEN ROUTE :leaf LIMIT 20")
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
		"OPEN ROUTE :leaf LIMIT 20")
	if len(reopenedRoute.Results[0].Rows) != 1 ||
		reopenedRoute.Results[0].Rows[0]["row_id"] != rowID ||
		reopenedRoute.Results[0].Rows[0]["revision"] != float64(2) {
		t.Fatalf("reopened Route = %#v", reopenedRoute)
	}
	e2eEnvelope(t, root, binary, "exec", "--data-dir", dataDir,
		"--input", statementInput(
			map[string]any{"row": rowID},
			map[string]any{
				"expected_schema_version": 1, "expected_revision": 2, "max_affected_rows": 1,
				"actor": "agent:e2e", "source": "e2e:delete", "reason": "verify compensation",
			},
		),
		"DELETE FROM work.notes WHERE row_id = :row")
	deletedRoute := e2eEnvelope(t, root, binary, "query", "--data-dir", dataDir,
		"--input", statementInput(map[string]any{"leaf": leafID}, nil),
		"OPEN ROUTE :leaf LIMIT 20")
	if len(deletedRoute.Results[0].Rows) != 0 {
		t.Fatalf("Route after DELETE = %#v", deletedRoute)
	}
	restored := e2eEnvelope(t, root, binary, "exec", "--data-dir", dataDir,
		"--input", statementInput(
			map[string]any{"row": rowID, "revision": 2},
			map[string]any{
				"expected_schema_version": 1, "expected_revision": 3, "max_affected_rows": 1,
				"route_leaf_ids": []string{leafID},
				"actor":          "agent:e2e", "source": "e2e:restore", "reason": "verify compensation",
			},
		),
		"RESTORE work.notes ROW :row TO REVISION :revision")
	restoredRoute := e2eEnvelope(t, root, binary, "query", "--data-dir", dataDir,
		"--input", statementInput(map[string]any{"leaf": leafID}, nil),
		"OPEN ROUTE :leaf LIMIT 20")
	if restored.Results[0].Revision == nil || *restored.Results[0].Revision != 4 ||
		len(restoredRoute.Results[0].Rows) != 1 ||
		restoredRoute.Results[0].Rows[0]["row_id"] != rowID ||
		restoredRoute.Results[0].Rows[0]["revision"] != float64(4) {
		t.Fatalf("Route after RESTORE = restore %#v, route %#v", restored, restoredRoute)
	}
	feedbackEvent := feedback.Event{
		Version: feedback.EventVersion, EventID: "e2e-feedback-wrong",
		Kind: feedback.KindWrong, Actor: "user:e2e",
		Reason: "the restored wording is not the confirmed revision",
		Target: feedback.Target{Database: "work", Table: "notes", RowID: rowID, Revision: 4},
	}
	encodedFeedback, err := json.Marshal(feedbackEvent)
	if err != nil {
		t.Fatal(err)
	}
	var feedbackReceipt feedback.Receipt
	e2eJSON(t, root, &feedbackReceipt, binary,
		"feedback", "--data-dir", dataDir, "--event", string(encodedFeedback))
	if feedbackReceipt.Status != "recorded" || feedbackReceipt.Target.Revision != 4 {
		t.Fatalf("Feedback Receipt = %#v", feedbackReceipt)
	}
	confirmation := feedback.Confirmation{
		Version: feedback.ConfirmationVersion, ConfirmationID: "e2e-confirm-undo",
		FeedbackEventID: feedbackEvent.EventID, SourceEventID: "e2e:user-confirmation",
		Actor: "agent:e2e", Instruction: "restore the last confirmed wording",
		Action: feedback.ActionUndo, ExpectedRevision: 4,
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
		confirmationReceipt.NewRevision == nil || *confirmationReceipt.NewRevision != 5 ||
		confirmationReceipt.RestoredRevision == nil || *confirmationReceipt.RestoredRevision != 2 {
		t.Fatalf("Feedback Confirmation Receipt = %#v", confirmationReceipt)
	}
	openedAfterFeedback := e2eEnvelope(t, root, binary, "query", "--data-dir", dataDir,
		"--input", statementInput(map[string]any{"leaf": leafID}, nil),
		"OPEN ROUTE :leaf LIMIT 20")
	if len(openedAfterFeedback.Results[0].Rows) != 1 ||
		openedAfterFeedback.Results[0].Rows[0]["row_id"] != rowID ||
		openedAfterFeedback.Results[0].Rows[0]["revision"] != float64(5) {
		t.Fatalf("Route after feedback undo = %#v", openedAfterFeedback)
	}
	var health semantichealth.Report
	e2eJSON(t, root, &health, binary, "maintain", "--data-dir", dataDir, "--report")
	if health.Status != "healthy" || health.Hash == "" || health.IssueCount != 0 {
		t.Fatalf("native semantic health = %#v", health)
	}
	var doctor doctorOutput
	e2eJSON(t, root, &doctor, binary, "doctor", "--data-dir", dataDir)
	if doctor.Status != "healthy" || doctor.Databases != 1 ||
		doctor.Rows != 1 || doctor.History != 5 || doctor.SnapshotHash == "" {
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
