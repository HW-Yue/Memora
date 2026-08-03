package answerbenchmark_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/answerbenchmark"
	"github.com/HW-Yue/Memora/internal/answercorpus"
)

func TestPublishReportsRenamesOneCompletePrivateDirectoryAndNeverOverwrites(t *testing.T) {
	ctx := context.Background()
	executor, closeInstance := cleanDaemonExecutor(t, ctx)
	defer closeInstance()
	bundle, err := answercorpus.Generate()
	if err != nil {
		t.Fatal(err)
	}
	runner, err := answerbenchmark.NewRunner(answerbenchmark.RunnerDependencies{
		MSQL: executor, Provider: &answerProvider{}, Clock: &benchmarkClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	public, private, err := runner.Run(ctx, bundle.Manifest, answerbenchmark.RunConfig{
		RunID: "run-f183-publish", ProviderID: "scripted", Model: "model-test",
		ArmID: answerbenchmark.ArmAtlasLexicalPrefetch, PromptID: "query-agent-v1", CodeRevision: "test-revision",
	})
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "answer-run")
	if err := answerbenchmark.PublishReports(output, public, private); err != nil {
		t.Fatal(err)
	}
	publicBytes, err := os.ReadFile(filepath.Join(output, answerbenchmark.PublicScorecardFile))
	if err != nil {
		t.Fatal(err)
	}
	privatePath := filepath.Join(output, answerbenchmark.PrivateDiagnosticsFile)
	privateBytes, err := os.ReadFile(privatePath)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(privatePath)
	if err != nil || info.Mode().Perm()&0o077 != 0 || len(publicBytes) == 0 || len(privateBytes) == 0 {
		t.Fatalf("published files public=%d private=%d mode=%v err=%v", len(publicBytes), len(privateBytes), info.Mode(), err)
	}
	if err := answerbenchmark.PublishReports(output, public, private); !errors.Is(err, answerbenchmark.ErrOutputExists) {
		t.Fatalf("second PublishReports() = %v", err)
	}
	if err := answerbenchmark.PublishReports("relative-output", public, private); !errors.Is(err, answerbenchmark.ErrInvalidOutput) {
		t.Fatalf("relative PublishReports() = %v", err)
	}
	again, err := os.ReadFile(filepath.Join(output, answerbenchmark.PublicScorecardFile))
	if err != nil || string(again) != string(publicBytes) {
		t.Fatal("existing report changed after rejected overwrite")
	}
}
