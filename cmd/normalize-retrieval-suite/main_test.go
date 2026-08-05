package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunRejectsRelativeRootAndOutput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"--kind", "miracl-zh", "--root", "raw", "--output", "/tmp/suite.json"}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "absolute normalized") {
		t.Fatalf("code/stderr = %d/%q", code, stderr.String())
	}
}

func TestRunRefusesExistingOutput(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "suite.json")
	if err := os.WriteFile(output, []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"--kind", "miracl-zh", "--root", root, "--output", output}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "already exists") {
		t.Fatalf("code/stderr = %d/%q", code, stderr.String())
	}
}
