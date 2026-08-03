package answerevaluation

import (
	"context"
	"errors"
	"fmt"

	"github.com/HW-Yue/Memora/internal/answerbenchmark"
	"github.com/HW-Yue/Memora/internal/answercorpus"
)

var ErrEvaluation = errors.New("external answer evaluation failed")

type Evaluator interface {
	Evaluate(context.Context, Input) (Output, error)
}

type Runner struct{ evaluator Evaluator }

func NewRunner(evaluator Evaluator) (*Runner, error) {
	if evaluator == nil {
		return nil, ErrEvaluation
	}
	return &Runner{evaluator: evaluator}, nil
}

func (runner *Runner) Run(
	ctx context.Context,
	bundle answercorpus.Bundle,
	public answerbenchmark.PublicScorecard,
	private answerbenchmark.PrivateDiagnostics,
) (Report, error) {
	if runner == nil || runner.evaluator == nil {
		return Report{}, ErrEvaluation
	}
	input, err := BuildInput(bundle, public, private)
	if err != nil {
		return Report{}, fmt.Errorf("%w: build input", ErrEvaluation)
	}
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	output, err := runner.evaluator.Evaluate(ctx, input)
	if err != nil {
		if ctx.Err() != nil {
			return Report{}, ctx.Err()
		}
		return Report{}, fmt.Errorf("%w: evaluator", ErrEvaluation)
	}
	if output.Validate() != nil || output.InputSHA256 != input.Hash || len(output.Cases) != len(input.Cases) {
		return Report{}, fmt.Errorf("%w: invalid evaluator output", ErrEvaluation)
	}
	report := Report{
		Version: ReportVersion, RunID: input.RunID, CorpusID: input.CorpusID,
		CorpusRevision: input.CorpusRevision, SnapshotSHA256: input.SnapshotSHA256,
		PublicScorecardSHA256:    input.PublicScorecardSHA256,
		PrivateDiagnosticsSHA256: input.PrivateDiagnosticsSHA256,
		GroundTruthSHA256:        input.GroundTruthSHA256, EvaluatorInputSHA256: input.Hash,
		EvaluatorOutputSHA256: output.Hash, AdapterID: output.AdapterID,
		AdapterVersion: output.AdapterVersion, JudgeModel: output.JudgeModel,
		Cases: make([]ReportCase, 0, len(input.Cases)),
	}
	for index, inputCase := range input.Cases {
		outputCase, publicCase := output.Cases[index], public.Cases[index]
		if outputCase.CaseID != inputCase.CaseID || publicCase.CaseID != inputCase.CaseID ||
			!compatibleStatuses(inputCase.RunnerStatus, outputCase.Status) {
			return Report{}, fmt.Errorf("%w: evaluator case alignment", ErrEvaluation)
		}
		report.Cases = append(report.Cases, ReportCase{
			CaseID: inputCase.CaseID, Status: outputCase.Status, ErrorCode: outputCase.ErrorCode,
			DurationMicros: publicCase.DurationMicros, ProviderCalls: publicCase.ProviderCalls,
			MSQLCalls: publicCase.MSQLCalls, ToolCalls: publicCase.ToolCalls,
			InputTokens: publicCase.InputTokens, CachedTokens: publicCase.CachedTokens,
			OutputTokens: publicCase.OutputTokens, TotalTokens: publicCase.TotalTokens,
			Scores: cloneScores(outputCase.Scores),
		})
	}
	counts, performance, metrics, ok := aggregateReportCases(report.Cases)
	if !ok {
		return Report{}, fmt.Errorf("%w: aggregate", ErrEvaluation)
	}
	report.Counts, report.Performance, report.Metrics = counts, performance, metrics
	if err := report.Seal(); err != nil {
		return Report{}, fmt.Errorf("%w: seal report", ErrEvaluation)
	}
	return report, nil
}

func compatibleStatuses(runnerStatus, evaluatorStatus string) bool {
	if runnerStatus == "failed" {
		return evaluatorStatus == "runner_failed"
	}
	return runnerStatus == "succeeded" && (evaluatorStatus == "scored" || evaluatorStatus == "evaluator_failed")
}

func cloneScores(scores MetricScores) MetricScores {
	return MetricScores{
		FactualCorrectness: cloneScore(scores.FactualCorrectness), Faithfulness: cloneScore(scores.Faithfulness),
		ContextPrecision: cloneScore(scores.ContextPrecision), ContextRecall: cloneScore(scores.ContextRecall),
	}
}

func cloneScore(score *float64) *float64 {
	if score == nil {
		return nil
	}
	value := *score
	return &value
}
