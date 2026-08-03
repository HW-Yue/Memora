package answerrelease

import (
	"math"
	"reflect"

	"github.com/HW-Yue/Memora/internal/answerbenchmark"
	"github.com/HW-Yue/Memora/internal/answerevaluation"
)

var requiredArms = []string{
	answerbenchmark.ArmAtlasOnly,
	answerbenchmark.ArmAtlasLexical,
	answerbenchmark.ArmAtlasLexicalPrefetch,
}

func Build(evidence []Evidence) (Report, error) {
	ordered, identity, err := validateAndOrderEvidence(evidence)
	if err != nil {
		return Report{}, err
	}
	arms := make([]ArmResult, len(ordered))
	for index, item := range ordered {
		arms[index] = summarizeEvidence(item)
	}
	report := Report{
		Version: ReportVersion, PolicyVersion: PolicyVersion, Policy: FixedPolicy(), Identity: identity,
		Arms: arms,
	}
	applyDecision(&report)
	if err := report.Seal(); err != nil {
		return Report{}, ErrInvalidEvidence
	}
	return report, nil
}

func validateAndOrderEvidence(evidence []Evidence) ([]Evidence, Identity, error) {
	if len(evidence) != len(requiredArms) {
		return nil, Identity{}, ErrInvalidEvidence
	}
	byArm := make(map[string]Evidence, len(evidence))
	seenRuns, seenScorecards, seenEvaluations := map[string]bool{}, map[string]bool{}, map[string]bool{}
	var identity Identity
	for index, item := range evidence {
		scorecard, evaluation := item.Scorecard, item.Evaluation
		if scorecard.Validate() != nil || evaluation.Validate() != nil ||
			evaluation.RunID != scorecard.RunID || evaluation.CorpusID != scorecard.CorpusID ||
			evaluation.CorpusRevision != scorecard.CorpusRevision ||
			evaluation.SnapshotSHA256 != scorecard.SnapshotSHA256 ||
			evaluation.PublicScorecardSHA256 != scorecard.Hash || !validCaseBinding(scorecard, evaluation) ||
			!requiredArm(scorecard.ArmID) || byArm[scorecard.ArmID].Scorecard.Version != "" ||
			seenRuns[scorecard.RunID] || seenScorecards[scorecard.Hash] || seenEvaluations[evaluation.Hash] {
			return nil, Identity{}, ErrInvalidEvidence
		}
		current := evidenceIdentity(scorecard, evaluation)
		if index == 0 {
			identity = current
		} else if !reflect.DeepEqual(identity, current) {
			return nil, Identity{}, ErrInvalidEvidence
		}
		seenRuns[scorecard.RunID], seenScorecards[scorecard.Hash], seenEvaluations[evaluation.Hash] = true, true, true
		byArm[scorecard.ArmID] = item
	}
	ordered := make([]Evidence, len(requiredArms))
	for index, armID := range requiredArms {
		item, found := byArm[armID]
		if !found {
			return nil, Identity{}, ErrInvalidEvidence
		}
		ordered[index] = item
	}
	return ordered, identity, nil
}

func evidenceIdentity(
	scorecard answerbenchmark.PublicScorecard,
	evaluation answerevaluation.Report,
) Identity {
	return Identity{
		CorpusID: scorecard.CorpusID, CorpusRevision: scorecard.CorpusRevision,
		SnapshotID: scorecard.SnapshotID, SnapshotSHA256: scorecard.SnapshotSHA256,
		ProviderID: scorecard.ProviderID, Model: scorecard.Model, PromptID: scorecard.PromptID,
		CodeRevision: scorecard.CodeRevision, AdapterID: evaluation.AdapterID,
		AdapterVersion: evaluation.AdapterVersion, JudgeModel: evaluation.JudgeModel,
		GroundTruthSHA256: evaluation.GroundTruthSHA256,
	}
}

func validCaseBinding(
	scorecard answerbenchmark.PublicScorecard,
	evaluation answerevaluation.Report,
) bool {
	if len(scorecard.Cases) != len(evaluation.Cases) {
		return false
	}
	for index, scorecardCase := range scorecard.Cases {
		evaluationCase := evaluation.Cases[index]
		if scorecardCase.CaseID != evaluationCase.CaseID ||
			math.MaxUint64-scorecardCase.InputTokens < scorecardCase.OutputTokens ||
			scorecardCase.DurationMicros != evaluationCase.DurationMicros ||
			scorecardCase.ProviderCalls != evaluationCase.ProviderCalls ||
			scorecardCase.MSQLCalls != evaluationCase.MSQLCalls ||
			scorecardCase.ToolCalls != evaluationCase.ToolCalls ||
			scorecardCase.InputTokens != evaluationCase.InputTokens ||
			scorecardCase.CachedTokens != evaluationCase.CachedTokens ||
			scorecardCase.OutputTokens != evaluationCase.OutputTokens ||
			scorecardCase.TotalTokens != evaluationCase.TotalTokens {
			return false
		}
		switch scorecardCase.Status {
		case "succeeded":
			if evaluationCase.Status != "scored" && evaluationCase.Status != "evaluator_failed" {
				return false
			}
		case "failed":
			if evaluationCase.Status != "runner_failed" {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func summarizeEvidence(evidence Evidence) ArmResult {
	evaluation := evidence.Evaluation
	arm := ArmResult{
		ArmID: evidence.Scorecard.ArmID, RunID: evidence.Scorecard.RunID,
		ScorecardSHA256: evidence.Scorecard.Hash, EvaluationSHA256: evaluation.Hash,
		Counts: evaluation.Counts, Performance: evaluation.Performance,
		Metrics: cloneMetrics(evaluation.Metrics), Minimums: minimumScores(evaluation.Cases),
		Reasons: []string{},
	}
	arm.Reasons = incompleteReasons(arm)
	if len(arm.Reasons) == 0 {
		arm.EvidenceStatus = "complete"
	} else {
		arm.EvidenceStatus = "incomplete"
	}
	return arm
}

func decide(arms []ArmResult) (string, string) {
	incomplete := false
	for index := range arms {
		arms[index].Reasons = incompleteReasons(arms[index])
		if len(arms[index].Reasons) != 0 {
			arms[index].EvidenceStatus = "incomplete"
			incomplete = true
		} else {
			arms[index].EvidenceStatus = "complete"
		}
		arms[index].Eligible = false
	}
	if incomplete {
		for index := range arms {
			arms[index].Reasons = appendReason(arms[index].Reasons, "matrix_incomplete")
		}
		return StatusIncomplete, ""
	}
	bestFactual, bestFaithfulness := 0.0, 0.0
	for _, arm := range arms {
		bestFactual = math.Max(bestFactual, *arm.Metrics.FactualCorrectness.Mean)
		bestFaithfulness = math.Max(bestFaithfulness, *arm.Metrics.Faithfulness.Mean)
	}
	policy := FixedPolicy()
	for index := range arms {
		arm := &arms[index]
		arm.Reasons = qualityReasons(*arm, bestFactual, bestFaithfulness, policy)
		arm.Eligible = len(arm.Reasons) == 0
	}
	defaultArm := ""
	for _, arm := range arms {
		if arm.Eligible && (defaultArm == "" || cheaper(arm, armByID(arms, defaultArm))) {
			defaultArm = arm.ArmID
		}
	}
	if defaultArm == "" {
		return StatusFailed, ""
	}
	return StatusPassed, defaultArm
}

func applyDecision(report *Report) {
	arms := cloneArms(report.Arms)
	status, defaultArm := decide(arms)
	report.Arms, report.Status, report.DefaultArm = arms, status, defaultArm
}

func incompleteReasons(arm ArmResult) []string {
	reasons := []string{}
	policy := FixedPolicy()
	if arm.Counts.Cases < policy.MinimumCases {
		reasons = append(reasons, "case_count_below_minimum")
	}
	if arm.Counts.RunnerFailed != 0 || arm.Counts.RunnerSucceeded != arm.Counts.Cases {
		reasons = append(reasons, "runner_coverage_incomplete")
	}
	if arm.Counts.EvaluatorFailed != 0 || arm.Counts.Scored != arm.Counts.Cases {
		reasons = append(reasons, "evaluator_coverage_incomplete")
	}
	metrics := []answerevaluation.MetricAggregate{
		arm.Metrics.FactualCorrectness, arm.Metrics.Faithfulness,
		arm.Metrics.ContextPrecision, arm.Metrics.ContextRecall,
	}
	for _, metric := range metrics {
		if metric.Samples != arm.Counts.Cases || metric.Mean == nil {
			reasons = append(reasons, "metric_samples_incomplete")
			break
		}
	}
	if arm.Minimums.FactualCorrectness == nil || arm.Minimums.Faithfulness == nil ||
		arm.Minimums.ContextPrecision == nil || arm.Minimums.ContextRecall == nil {
		reasons = appendReason(reasons, "metric_samples_incomplete")
	}
	return reasons
}

func qualityReasons(arm ArmResult, bestFactual, bestFaithfulness float64, policy Policy) []string {
	reasons := []string{}
	if *arm.Metrics.FactualCorrectness.Mean < policy.FactualCorrectnessMean {
		reasons = append(reasons, "factual_correctness_mean_below_threshold")
	}
	if *arm.Minimums.FactualCorrectness < policy.FactualCorrectnessMinimum {
		reasons = append(reasons, "factual_correctness_minimum_below_threshold")
	}
	if *arm.Metrics.Faithfulness.Mean < policy.FaithfulnessMean {
		reasons = append(reasons, "faithfulness_mean_below_threshold")
	}
	if *arm.Minimums.Faithfulness < policy.FaithfulnessMinimum {
		reasons = append(reasons, "faithfulness_minimum_below_threshold")
	}
	if *arm.Metrics.ContextPrecision.Mean < policy.ContextPrecisionMean {
		reasons = append(reasons, "context_precision_mean_below_threshold")
	}
	if *arm.Metrics.ContextRecall.Mean < policy.ContextRecallMean {
		reasons = append(reasons, "context_recall_mean_below_threshold")
	}
	if bestFactual-*arm.Metrics.FactualCorrectness.Mean > policy.MaximumQualityDelta {
		reasons = append(reasons, "factual_correctness_delta_above_threshold")
	}
	if bestFaithfulness-*arm.Metrics.Faithfulness.Mean > policy.MaximumQualityDelta {
		reasons = append(reasons, "faithfulness_delta_above_threshold")
	}
	return reasons
}

func cheaper(left, right ArmResult) bool {
	if left.Performance.InputTokens != right.Performance.InputTokens {
		return left.Performance.InputTokens < right.Performance.InputTokens
	}
	if left.Performance.ProviderCalls != right.Performance.ProviderCalls {
		return left.Performance.ProviderCalls < right.Performance.ProviderCalls
	}
	if left.Performance.LatencyP95Micros != right.Performance.LatencyP95Micros {
		return left.Performance.LatencyP95Micros < right.Performance.LatencyP95Micros
	}
	return armRank(left.ArmID) < armRank(right.ArmID)
}

func armByID(arms []ArmResult, armID string) ArmResult {
	for _, arm := range arms {
		if arm.ArmID == armID {
			return arm
		}
	}
	return ArmResult{}
}

func armRank(armID string) int {
	for index, current := range requiredArms {
		if armID == current {
			return index
		}
	}
	return len(requiredArms)
}

func requiredArm(armID string) bool { return armRank(armID) < len(requiredArms) }

func minimumScores(cases []answerevaluation.ReportCase) answerevaluation.MetricScores {
	values := answerevaluation.MetricScores{}
	for _, testCase := range cases {
		if testCase.Status != "scored" {
			continue
		}
		minimumPointer(&values.FactualCorrectness, *testCase.Scores.FactualCorrectness)
		minimumPointer(&values.Faithfulness, *testCase.Scores.Faithfulness)
		minimumPointer(&values.ContextPrecision, *testCase.Scores.ContextPrecision)
		minimumPointer(&values.ContextRecall, *testCase.Scores.ContextRecall)
	}
	return values
}

func minimumPointer(target **float64, value float64) {
	if *target == nil || value < **target {
		copy := value
		*target = &copy
	}
}

func cloneMetrics(metrics answerevaluation.ReportMetrics) answerevaluation.ReportMetrics {
	metrics.FactualCorrectness.Mean = cloneFloat(metrics.FactualCorrectness.Mean)
	metrics.Faithfulness.Mean = cloneFloat(metrics.Faithfulness.Mean)
	metrics.ContextPrecision.Mean = cloneFloat(metrics.ContextPrecision.Mean)
	metrics.ContextRecall.Mean = cloneFloat(metrics.ContextRecall.Mean)
	return metrics
}

func cloneFloat(value *float64) *float64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneArms(arms []ArmResult) []ArmResult {
	cloned := make([]ArmResult, len(arms))
	for index, arm := range arms {
		cloned[index] = arm
		cloned[index].Metrics = cloneMetrics(arm.Metrics)
		cloned[index].Minimums = answerevaluation.MetricScores{
			FactualCorrectness: cloneFloat(arm.Minimums.FactualCorrectness),
			Faithfulness:       cloneFloat(arm.Minimums.Faithfulness),
			ContextPrecision:   cloneFloat(arm.Minimums.ContextPrecision),
			ContextRecall:      cloneFloat(arm.Minimums.ContextRecall),
		}
		cloned[index].Reasons = append([]string(nil), arm.Reasons...)
	}
	return cloned
}

func appendReason(reasons []string, reason string) []string {
	for _, current := range reasons {
		if current == reason {
			return reasons
		}
	}
	return append(reasons, reason)
}
