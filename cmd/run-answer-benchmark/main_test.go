package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/HW-Yue/Memora/internal/answerbenchmark"
	"github.com/HW-Yue/Memora/internal/answercorpus"
)

func TestRunExecutesCompleteSuiteAgainstOpenAICompatibleProvider(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.Header.Get("Authorization") != "Bearer private-test-secret" {
			t.Error("Provider authorization was not resolved from the configured environment name")
		}
		body, _ := io.ReadAll(request.Body)
		var input struct {
			Model      string `json:"model"`
			ToolChoice string `json:"tool_choice"`
			Messages   []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(body, &input); err != nil {
			t.Error(err)
		}
		writer.Header().Set("Content-Type", "application/json")
		if input.ToolChoice == "none" {
			fmt.Fprintf(writer, `{"model":%q,"choices":[{"index":0,"message":{"role":"assistant","content":"Local fake answer."},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`, input.Model)
			return
		}
		question := ""
		for _, message := range input.Messages {
			if message.Role == "user" {
				question += message.Content
			}
		}
		database, table := "projects", "decisions"
		switch {
		case strings.Contains(question, "time zone"), strings.Contains(question, "编辑器"), strings.Contains(question, "code identifier"):
			database, table = "personal", "preferences"
		case strings.Contains(question, "desert-planet"), strings.Contains(question, "哪本书"), strings.Contains(question, "authors"):
			database, table = "reading", "books"
		}
		arguments, _ := json.Marshal(map[string]any{
			"source":     fmt.Sprintf("SELECT * FROM %s.%s LIMIT 4", database, table),
			"statements": []any{map[string]any{}},
		})
		fmt.Fprintf(writer, `{"model":%q,"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call-%d","type":"function","function":{"name":"execute_msql","arguments":%q}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`, input.Model, calls.Load(), string(arguments))
	}))
	defer server.Close()
	t.Setenv("MEMORA_TEST_PROVIDER_KEY", "private-test-secret")
	bundle, err := answercorpus.Generate()
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := answercorpus.EncodeManifest(bundle.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	manifestPath := filepath.Join(root, "manifest.json")
	if err := os.WriteFile(manifestPath, manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "report")
	var stdout, stderr bytes.Buffer
	exitCode := run(context.Background(), []string{
		"--manifest", manifestPath, "--output-dir", output, "--run-id", "command-test",
		"--provider", "local-fake", "--api-base-url", server.URL + "/v1", "--model", "model-test",
		"--secret-env", "MEMORA_TEST_PROVIDER_KEY", "--code-revision", "test-revision",
	}, &stdout, &stderr)
	if exitCode != 0 || calls.Load() != 24 {
		t.Fatalf("run()=%d calls=%d stdout=%q stderr=%q", exitCode, calls.Load(), stdout.String(), stderr.String())
	}
	for _, name := range []string{answerbenchmark.PublicScorecardFile, answerbenchmark.PrivateDiagnosticsFile} {
		if _, err := os.Stat(filepath.Join(output, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	if strings.Contains(stdout.String()+stderr.String(), "private-test-secret") {
		t.Fatal("command output leaked Provider secret")
	}
}

func TestRunRejectsNonAbsolutePathsBeforeResolvingProvider(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if exitCode := run(context.Background(), []string{"--manifest", "relative", "--output-dir", "relative"}, &stdout, &stderr); exitCode != 2 {
		t.Fatalf("run()=%d stderr=%q", exitCode, stderr.String())
	}
}
