package answerevaluation_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/answercorpus"
	"github.com/HW-Yue/Memora/internal/answerevaluation"
)

func TestProcessEvaluatorUsesPrivateFilesAndStrictOutput(t *testing.T) {
	input := processInput(t)
	countFile := filepath.Join(t.TempDir(), "count")
	evaluator := processEvaluator(t, countFile, "success")
	output, err := evaluator.Evaluate(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if err := output.Validate(); err != nil {
		t.Fatal(err)
	}
	count, err := os.ReadFile(countFile)
	if err != nil || string(count) != "1" || output.InputSHA256 != input.Hash || len(output.Cases) != len(input.Cases) {
		t.Fatalf("process output = %#v; count = %q; err = %v", output, count, err)
	}

	for _, mode := range []string{"unknown_field", "wrong_input"} {
		evaluator = processEvaluator(t, filepath.Join(t.TempDir(), "count"), mode)
		if _, err := evaluator.Evaluate(context.Background(), input); err == nil {
			t.Fatalf("mode %q accepted invalid evaluator output", mode)
		}
	}
}

func TestProcessEvaluatorCancelsAndDoesNotExposeStderr(t *testing.T) {
	input := processInput(t)
	evaluator := processEvaluator(t, filepath.Join(t.TempDir(), "count"), "wait")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := evaluator.Evaluate(ctx, input); err != context.Canceled {
		t.Fatalf("cancel error = %v", err)
	}

	const secret = "memora-secret-must-not-leak"
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	evaluator, err = answerevaluation.NewProcessEvaluator(answerevaluation.ProcessEvaluatorConfig{
		Executable: executable,
		Arguments:  []string{"-test.run=^TestProcessEvaluatorHelperProcess$", "--"},
		Environment: []string{
			"GO_WANT_MEMORA_EVALUATOR_HELPER=1", "MEMORA_HELPER_MODE=stderr",
			"MEMORA_HELPER_COUNT=" + filepath.Join(t.TempDir(), "count"), "MEMORA_TEST_SECRET=" + secret,
		},
		TempRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := evaluator.Evaluate(context.Background(), input); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("stderr failure = %v", err)
	}
}

func processEvaluator(t *testing.T, countFile, mode string) *answerevaluation.ProcessEvaluator {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	evaluator, err := answerevaluation.NewProcessEvaluator(answerevaluation.ProcessEvaluatorConfig{
		Executable: executable,
		Arguments:  []string{"-test.run=^TestProcessEvaluatorHelperProcess$", "--"},
		Environment: []string{
			"GO_WANT_MEMORA_EVALUATOR_HELPER=1", "MEMORA_HELPER_MODE=" + mode,
			"MEMORA_HELPER_COUNT=" + countFile,
		},
		TempRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return evaluator
}

func processInput(t *testing.T) answerevaluation.Input {
	t.Helper()
	bundle, err := answercorpus.Generate()
	if err != nil {
		t.Fatal(err)
	}
	public, private := evaluationArtifacts(t, bundle)
	input, err := answerevaluation.BuildInput(bundle, public, private)
	if err != nil {
		t.Fatal(err)
	}
	return input
}

func TestProcessEvaluatorHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_MEMORA_EVALUATOR_HELPER") != "1" {
		return
	}
	if err := os.WriteFile(os.Getenv("MEMORA_HELPER_COUNT"), []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	mode := os.Getenv("MEMORA_HELPER_MODE")
	if mode == "stderr" {
		fmt.Fprintln(os.Stderr, os.Getenv("MEMORA_TEST_SECRET"))
		os.Exit(17)
	}
	if mode == "wait" {
		time.Sleep(time.Minute)
	}
	inputPath, outputPath := helperArgument("--input"), helperArgument("--output")
	inputInfo, err := os.Stat(inputPath)
	if err != nil || inputInfo.Mode().Perm() != 0o600 {
		t.Fatalf("input mode = %v; err = %v", inputInfo, err)
	}
	if info, err := os.Stat(filepath.Dir(inputPath)); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode = %v; err = %v", info, err)
	}
	if info, err := os.Stat(outputPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("output mode = %v; err = %v", info, err)
	}
	encoded, err := os.ReadFile(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	var input answerevaluation.Input
	if err := json.Unmarshal(encoded, &input); err != nil {
		t.Fatal(err)
	}
	output := answerevaluation.Output{
		Version: answerevaluation.OutputVersion, InputSHA256: input.Hash,
		AdapterID: "helper", AdapterVersion: "1", JudgeModel: "judge-test",
		Cases: make([]answerevaluation.OutputCase, 0, len(input.Cases)),
	}
	for _, testCase := range input.Cases {
		value := answerevaluation.OutputCase{CaseID: testCase.CaseID, Status: "runner_failed", Scores: answerevaluation.MetricScores{}}
		if testCase.RunnerStatus == "succeeded" {
			score := 0.75
			value.Status = "scored"
			value.Scores = answerevaluation.MetricScores{
				FactualCorrectness: &score, Faithfulness: &score,
				ContextPrecision: &score, ContextRecall: &score,
			}
		}
		output.Cases = append(output.Cases, value)
	}
	if mode == "wrong_input" {
		output.InputSHA256 = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	}
	if err := output.Seal(); err != nil {
		t.Fatal(err)
	}
	encoded, err = json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	if mode == "unknown_field" {
		encoded = append(encoded[:len(encoded)-1], []byte(`,"unknown":true}`)...)
	}
	if err := os.WriteFile(outputPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}

func helperArgument(name string) string {
	for index := range os.Args {
		if os.Args[index] == name && index+1 < len(os.Args) {
			return os.Args[index+1]
		}
	}
	return ""
}
