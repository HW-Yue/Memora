package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/agent"
)

func TestRunBuildsJSONAndHTMLFromHookSnapshots(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "hook.json")
	jsonOutput := filepath.Join(directory, "report.json")
	htmlOutput := filepath.Join(directory, "report.html")
	envelope := agent.ExternalAgentHookEnvelope{Version: agent.ExternalAgentHookVersion,
		Context: agent.ExternalAgentHookContext{HostSessionID: "host", Host: "host", Model: "model", SkillVersion: "skill", ProtocolVersion: "msql"}, Events: []agent.ExternalAgentHookEvent{}}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	status := run(context.Background(), []string{"--input", input, "--json", jsonOutput, "--html", htmlOutput}, &stdout, &stderr)
	if status != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "Agent metrics report") {
		t.Fatalf("status/stdout/stderr=%d/%q/%q", status, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(jsonOutput); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(htmlOutput); err != nil {
		t.Fatal(err)
	}
}

func TestRunRejectsRelativePaths(t *testing.T) {
	var stdout, stderr bytes.Buffer
	status := run(context.Background(), []string{"--input", "hook.json", "--json", "report.json"}, &stdout, &stderr)
	if status != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "absolute normalized") {
		t.Fatalf("status/stdout/stderr=%d/%q/%q", status, stdout.String(), stderr.String())
	}
}
