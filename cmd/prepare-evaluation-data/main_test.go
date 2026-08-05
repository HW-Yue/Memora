package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunRejectsRelativeRegistryAndRoot(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"--registry", "registry.json", "--root", "/tmp/evaluation"}, &stdout, &stderr); code != 2 {
		t.Fatalf("run() code = %d, want 2; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "absolute normalized") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunRequiresRegistryAndRoot(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), nil, &stdout, &stderr); code != 2 {
		t.Fatalf("run() code = %d, want 2", code)
	}
}
