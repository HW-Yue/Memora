package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/retrievalscore"
)

func TestRunBuildsReportAndRefusesOverwrite(t *testing.T) {
	root := t.TempDir()
	suite := retrievalscore.Suite{Version: retrievalscore.SuiteVersion, SuiteID: "suite", DatasetID: "fixture", Split: "dev",
		Queries: []retrievalscore.Query{{QueryID: "q1", Group: "all"}}, Qrels: []retrievalscore.Qrel{{QueryID: "q1", DocumentID: "d1", Relevance: 1}}}
	if err := suite.Seal(); err != nil {
		t.Fatal(err)
	}
	retrievalRun := retrievalscore.Run{Version: retrievalscore.RunVersion, RunID: "baseline", Arm: "lexical", SuiteHash: suite.Hash,
		Results: []retrievalscore.Result{{QueryID: "q1", Status: "completed", CandidateDocumentIDs: []string{"d1"}}}}
	if err := retrievalRun.Seal(); err != nil {
		t.Fatal(err)
	}
	suitePath, runPath, outputPath := filepath.Join(root, "suite.json"), filepath.Join(root, "run.json"), filepath.Join(root, "report.json")
	writeValue(t, suitePath, suite)
	writeValue(t, runPath, retrievalRun)
	var stdout, stderr bytes.Buffer
	args := []string{"--suite", suitePath, "--run", runPath, "--baseline-run", "baseline", "--output", outputPath}
	if code := run(context.Background(), args, &stdout, &stderr); code != 0 {
		t.Fatalf("run() code = %d stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(outputPath); err != nil {
		t.Fatalf("report missing: %v", err)
	}
	if code := run(context.Background(), args, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "already exists") {
		t.Fatalf("overwrite code/stderr = %d/%q", code, stderr.String())
	}
}

func TestRunRejectsRelativePaths(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"--suite", "suite.json", "--run", "/tmp/run.json", "--baseline-run", "baseline", "--output", "/tmp/report.json"}, &stdout, &stderr); code != 2 {
		t.Fatalf("run() code = %d", code)
	}
}

func writeValue(t *testing.T, path string, value any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		t.Fatal(err)
	}
}
