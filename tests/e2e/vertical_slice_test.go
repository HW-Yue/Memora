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

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
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

	inserted := e2eEnvelope(t, root, binary, "exec", "--data-dir", dataDir,
		"--input", statementInput(nil, map[string]any{
			"expected_schema_version": 1, "max_affected_rows": 1,
			"actor": "agent:e2e", "source": "e2e:insert", "reason": "capture decision",
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
					IndexTerms: []string{}, RouteLeafIDs: []string{},
					Actor: "agent:e2e", Source: "e2e:update", Reason: "refine decision",
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
	var doctor doctorOutput
	e2eJSON(t, root, &doctor, binary, "doctor", "--data-dir", dataDir)
	if doctor.Status != "healthy" || doctor.Databases != 1 ||
		doctor.Rows != 1 || doctor.History != 2 || doctor.SnapshotHash == "" {
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
