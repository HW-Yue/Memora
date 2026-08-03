package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/answerbenchmark"
	"github.com/HW-Yue/Memora/internal/answercorpus"
	"github.com/HW-Yue/Memora/internal/answerevaluation"
)

func TestRunWiresExplicitEvaluatorEnvironmentWithoutPuttingSecretInArguments(t *testing.T) {
	bundle, err := answercorpus.Generate()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	paths := []string{
		filepath.Join(root, "manifest.json"), filepath.Join(root, "truth.json"),
		filepath.Join(root, "scorecard.json"), filepath.Join(root, "diagnostics.json"),
		filepath.Join(root, "evaluator"), filepath.Join(root, "evaluation.json"),
	}
	const secret = "command-secret-must-not-leak"
	marker := &commandEvaluator{}
	var received answerevaluation.ProcessEvaluatorConfig
	published := false
	dependencies := commandDependencies{
		loadBundle: func(manifest, truth string) (answercorpus.Bundle, error) {
			if manifest != paths[0] || truth != paths[1] {
				t.Fatalf("bundle paths = %q, %q", manifest, truth)
			}
			return bundle, nil
		},
		loadReports: func(scorecard, diagnostics string) (answerbenchmark.PublicScorecard, answerbenchmark.PrivateDiagnostics, error) {
			if scorecard != paths[2] || diagnostics != paths[3] {
				t.Fatalf("report paths = %q, %q", scorecard, diagnostics)
			}
			return answerbenchmark.PublicScorecard{}, answerbenchmark.PrivateDiagnostics{}, nil
		},
		newEvaluator: func(config answerevaluation.ProcessEvaluatorConfig) (answerevaluation.Evaluator, error) {
			received = config
			return marker, nil
		},
		runEvaluation: func(ctx context.Context, _ answercorpus.Bundle, _ answerbenchmark.PublicScorecard, _ answerbenchmark.PrivateDiagnostics, evaluator answerevaluation.Evaluator) (answerevaluation.Report, error) {
			if evaluator != marker {
				t.Fatal("run did not receive the configured evaluator")
			}
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) <= 0 {
				t.Fatal("evaluation context has no live timeout")
			}
			return answerevaluation.Report{Hash: "sha256:report"}, nil
		},
		publish: func(path string, report answerevaluation.Report) error {
			if path != paths[5] || report.Hash != "sha256:report" {
				t.Fatalf("publish = %q, %#v", path, report)
			}
			published = true
			return nil
		},
		lookupEnvironment: func(name string) (string, bool) {
			switch name {
			case "MEMORA_TEST_JUDGE_KEY":
				return secret, true
			case "SSL_CERT_FILE":
				return "/tmp/test-certificate.pem", true
			default:
				return "", false
			}
		},
	}
	arguments := []string{
		"--manifest", paths[0], "--ground-truth", paths[1], "--scorecard", paths[2],
		"--diagnostics", paths[3], "--evaluator", paths[4], "--evaluator-arg", "/tmp/evaluate.py",
		"--output", paths[5], "--api-base-url", "https://judge.invalid/v1", "--judge-model", "judge-test",
		"--secret-env", "MEMORA_TEST_JUDGE_KEY", "--pass-env", "SSL_CERT_FILE", "--timeout", "2m",
	}
	var stdout, stderr bytes.Buffer
	if exitCode := run(context.Background(), arguments, &stdout, &stderr, dependencies); exitCode != 0 || !published {
		t.Fatalf("run()=%d published=%v stdout=%q stderr=%q", exitCode, published, stdout.String(), stderr.String())
	}
	if received.Executable != paths[4] || len(received.Arguments) != 1 || received.Arguments[0] != "/tmp/evaluate.py" ||
		!containsEntry(received.Environment, "MEMORA_EVAL_API_BASE_URL=https://judge.invalid/v1") ||
		!containsEntry(received.Environment, "MEMORA_EVAL_MODEL=judge-test") ||
		!containsEntry(received.Environment, "MEMORA_EVAL_SECRET_ENV=MEMORA_TEST_JUDGE_KEY") ||
		!containsEntry(received.Environment, "MEMORA_TEST_JUDGE_KEY="+secret) ||
		!containsEntry(received.Environment, "SSL_CERT_FILE=/tmp/test-certificate.pem") {
		t.Fatalf("evaluator config = %#v", received)
	}
	if strings.Contains(strings.Join(received.Arguments, " ")+stdout.String()+stderr.String(), secret) {
		t.Fatal("command leaked the evaluator secret")
	}
}

func TestRunRejectsInvalidPathsAndMissingSecret(t *testing.T) {
	lookupCalls := 0
	dependencies := commandDependencies{lookupEnvironment: func(string) (string, bool) {
		lookupCalls++
		return "", false
	}}
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"--manifest", "relative"}, &stdout, &stderr, dependencies); code != 2 || lookupCalls != 0 {
		t.Fatalf("invalid path run=%d lookups=%d stderr=%q", code, lookupCalls, stderr.String())
	}

	root := t.TempDir()
	path := func(name string) string { return filepath.Join(root, name) }
	arguments := []string{
		"--manifest", path("manifest"), "--ground-truth", path("truth"), "--scorecard", path("scorecard"),
		"--diagnostics", path("diagnostics"), "--evaluator", path("evaluator"), "--output", path("output"),
		"--api-base-url", "https://judge.invalid/v1", "--judge-model", "judge", "--secret-env", "MISSING_KEY",
	}
	stderr.Reset()
	if code := run(context.Background(), arguments, &stdout, &stderr, dependencies); code != 1 || lookupCalls != 1 {
		t.Fatalf("missing secret run=%d lookups=%d stderr=%q", code, lookupCalls, stderr.String())
	}
}

type commandEvaluator struct{}

func (*commandEvaluator) Evaluate(context.Context, answerevaluation.Input) (answerevaluation.Output, error) {
	return answerevaluation.Output{}, errors.New("not called by command wiring test")
}

func containsEntry(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
