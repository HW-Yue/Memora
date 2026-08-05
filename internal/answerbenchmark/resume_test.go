package answerbenchmark_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/answerbenchmark"
	"github.com/HW-Yue/Memora/internal/answercorpus"
)

func TestRunnerResumesOnlyThePreviouslyFailedCase(t *testing.T) {
	ctx := context.Background()
	bundle, err := answercorpus.Generate()
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := filepath.Join(t.TempDir(), "answer-checkpoint.json")
	config := answerbenchmark.RunConfig{RunID: "run-resume", ProviderID: "scripted", Model: "model-test",
		ArmID: answerbenchmark.ArmAtlasLexicalPrefetch, PromptID: "query-agent-v1", CodeRevision: "test-revision", CheckpointPath: checkpoint}
	executor, closeInstance := cleanDaemonExecutor(t, ctx)
	provider := &answerProvider{failSubstring: "loopback"}
	runner, err := answerbenchmark.NewRunner(answerbenchmark.RunnerDependencies{MSQL: executor, Provider: provider, Clock: &benchmarkClock{}})
	if err != nil {
		t.Fatal(err)
	}
	firstPublic, _, err := runner.Run(ctx, bundle.Manifest, config)
	closeInstance()
	if err != nil || firstPublic.Cases[2].Status != "failed" {
		t.Fatalf("first run public/error = %#v/%v", firstPublic, err)
	}
	secondExecutor, secondClose := cleanDaemonExecutor(t, ctx)
	defer secondClose()
	secondProvider := &answerProvider{}
	secondRunner, err := answerbenchmark.NewRunner(answerbenchmark.RunnerDependencies{MSQL: secondExecutor, Provider: secondProvider, Clock: &benchmarkClock{}})
	if err != nil {
		t.Fatal(err)
	}
	secondPublic, secondPrivate, err := secondRunner.Run(ctx, bundle.Manifest, config)
	if err != nil || secondPublic.Validate() != nil || secondPrivate.Validate() != nil || len(secondPublic.Cases) != 12 {
		t.Fatalf("resume public/private/error = %#v/%#v/%v", secondPublic, secondPrivate, err)
	}
	if secondProvider.callCount() != 2 {
		t.Fatalf("resume Provider calls = %d, want only the failed case", secondProvider.callCount())
	}
}
