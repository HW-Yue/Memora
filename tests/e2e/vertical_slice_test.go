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

	"github.com/HW-Yue/Memora/internal/result"
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
	databaseID, _ := ddl.Results[0].Rows[0]["database_id"].(string)
	if databaseID == "" {
		t.Fatalf("DDL database identity = %#v", ddl.Results[0].Rows)
	}

	rootRoute := e2eEnvelope(t, root, binary, "exec", "--data-dir", dataDir,
		"--input", statementInput(map[string]any{
			"database": "work", "purpose": "Project routing",
		}, map[string]any{"max_affected_rows": 1}),
		"CREATE ROUTE ROOT FOR DATABASE :database PURPOSE :purpose")
	rootID, _ := rootRoute.Results[0].Rows[0]["route_id"].(string)
	leafRoute := e2eEnvelope(t, root, binary, "exec", "--data-dir", dataDir,
		"--input", statementInput(map[string]any{
			"root": rootID, "name": "architecture", "kind": "leaf",
			"purpose": "Architecture decisions",
		}, map[string]any{"max_affected_rows": 1}),
		"CREATE ROUTE UNDER :root NAME :name KIND :kind PURPOSE :purpose")
	leafID, _ := leafRoute.Results[0].Rows[0]["route_id"].(string)
	if rootID == "" || leafID == "" {
		t.Fatalf("routes = root %#v, leaf %#v", rootRoute, leafRoute)
	}

	inserted := e2eEnvelope(t, root, binary, "exec", "--data-dir", dataDir,
		"--input", statementInput(nil, map[string]any{
			"expected_schema_version": 1, "max_affected_rows": 1,
			"index_terms":    []string{"architecture", "manifest"},
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

	matched := e2eEnvelope(t, root, binary, "query", "--data-dir", dataDir,
		"--input", statementInput(map[string]any{
			"query": "architecture", "terms": []string{"architecture"},
		}, nil),
		"MATCH work.notes QUERY :query TERMS :terms LIMIT 10")
	if len(matched.Results[0].Rows) != 1 ||
		matched.Results[0].Rows[0]["row_id"] != rowID {
		t.Fatalf("MATCH envelope = %#v", matched)
	}
	routed := e2eEnvelope(t, root, binary, "query", "--data-dir", dataDir,
		"--input", statementInput(map[string]any{"leaf": leafID}, nil),
		"OPEN ROUTE :leaf LIMIT 10")
	if len(routed.Results[0].Rows) != 1 ||
		routed.Results[0].Rows[0]["row_id"] != rowID {
		t.Fatalf("OPEN ROUTE envelope = %#v", routed)
	}

	updated := e2eEnvelope(t, root, binary, "exec", "--data-dir", dataDir,
		"--input", statementInput(map[string]any{
			"row": rowID, "title": "atomic generation manifest",
		}, map[string]any{
			"expected_schema_version": 1, "expected_revision": 1,
			"max_affected_rows": 1, "index_terms": []string{"atomic", "manifest"},
			"route_leaf_ids": []string{leafID},
			"actor":          "agent:e2e", "source": "e2e:update", "reason": "refine decision",
		}),
		"UPDATE work.notes SET title = :title WHERE row_id = :row")
	if updated.Results[0].Revision == nil || *updated.Results[0].Revision != 2 {
		t.Fatalf("UPDATE envelope = %#v", updated)
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
