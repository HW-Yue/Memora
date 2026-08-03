package answerevaluation_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/answercorpus"
	"github.com/HW-Yue/Memora/internal/answerevaluation"
)

func TestRunnerAggregatesExternalScoresAndRawSystemMetrics(t *testing.T) {
	bundle, err := answercorpus.Generate()
	if err != nil {
		t.Fatal(err)
	}
	public, private := evaluationArtifacts(t, bundle)
	evaluator := &scriptedEvaluator{}
	runner, err := answerevaluation.NewRunner(evaluator)
	if err != nil {
		t.Fatal(err)
	}
	report, err := runner.Run(context.Background(), bundle, public, private)
	if err != nil {
		t.Fatal(err)
	}
	if err := report.Validate(); err != nil {
		t.Fatal(err)
	}
	if evaluator.calls != 1 || report.Counts != (answerevaluation.ReportCounts{
		Cases: 12, RunnerSucceeded: 11, RunnerFailed: 1, Scored: 10, EvaluatorFailed: 1,
	}) || report.Performance.LatencyP50Micros != 5 || report.Performance.LatencyP95Micros != 11 ||
		report.Performance.ProviderCalls != 22 || report.Performance.TotalTokens != 132 ||
		report.Metrics.FactualCorrectness.Samples != 10 || report.Metrics.FactualCorrectness.Mean == nil ||
		*report.Metrics.FactualCorrectness.Mean != 0.8 || report.Cases[10].Status != "evaluator_failed" ||
		report.Cases[11].Status != "runner_failed" {
		t.Fatalf("evaluation report = %#v", report)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, privateValue := range []string{
		bundle.GroundTruth.Cases[0].ReferenceAnswer,
		"Use a Memora-owned thin Go loop", "row_actual", "SELECT row_id",
	} {
		if strings.Contains(string(encoded), privateValue) {
			t.Fatalf("public evaluation report leaked %q", privateValue)
		}
	}
	tampered := report
	tampered.Counts.Scored++
	if tampered.Validate() == nil {
		t.Fatal("tampered evaluation report validated")
	}
}

type scriptedEvaluator struct{ calls int }

func (evaluator *scriptedEvaluator) Evaluate(_ context.Context, input answerevaluation.Input) (answerevaluation.Output, error) {
	evaluator.calls++
	output := answerevaluation.Output{
		Version: answerevaluation.OutputVersion, InputSHA256: input.Hash,
		AdapterID: "scripted-ragas", AdapterVersion: "0.4.3", JudgeModel: "judge-test",
		Cases: make([]answerevaluation.OutputCase, 0, len(input.Cases)),
	}
	for index, testCase := range input.Cases {
		value := answerevaluation.OutputCase{CaseID: testCase.CaseID, Status: "scored"}
		score := 0.8
		value.Scores = answerevaluation.MetricScores{
			FactualCorrectness: &score, Faithfulness: &score, ContextPrecision: &score, ContextRecall: &score,
		}
		if testCase.RunnerStatus == "failed" {
			value.Status, value.Scores = "runner_failed", answerevaluation.MetricScores{}
		} else if index == 10 {
			value.Status, value.ErrorCode, value.Scores = "evaluator_failed", "judge_failed", answerevaluation.MetricScores{}
		}
		output.Cases = append(output.Cases, value)
	}
	if err := output.Seal(); err != nil {
		return answerevaluation.Output{}, err
	}
	return output, nil
}
