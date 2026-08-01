package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunRequiresAbsoluteEvidenceArtifacts(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	status := run([]string{"--canonical-skill", "skills/memora", "--codex-runtime", "codex.json",
		"--claude-runtime", "claude.json", "--evidence", "evidence.json", "--output", "report.json"},
		&stdout, &stderr)
	if status != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "absolute and normalized") {
		t.Fatalf("status/stdout/stderr = %d/%q/%q", status, stdout.String(), stderr.String())
	}
}
