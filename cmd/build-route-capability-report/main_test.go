package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunRejectsRelativeArtifactsBeforeReading(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	status := run(context.Background(), []string{
		"--corpus", "benchmarks/route-retrieval-v1.json",
		"--canonical-skill", "skills/memora", "--source", "source.json", "--output", "report.json",
	}, &stdout, &stderr)
	if status != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "absolute normalized") {
		t.Fatalf("status/stdout/stderr = %d/%q/%q", status, stdout.String(), stderr.String())
	}
}
