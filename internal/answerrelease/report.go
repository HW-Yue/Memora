package answerrelease

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/HW-Yue/Memora/internal/answerevaluation"
)

func (report *Report) Seal() error {
	if report == nil || !validReportContent(*report, false) {
		return ErrInvalidReport
	}
	digest, err := reportDigest(*report)
	if err != nil {
		return ErrInvalidReport
	}
	report.Hash = digest
	return nil
}

func (report Report) Validate() error {
	if !validReportContent(report, true) {
		return ErrInvalidReport
	}
	digest, err := reportDigest(report)
	if err != nil || digest != report.Hash {
		return ErrInvalidReport
	}
	return nil
}

func (report Report) ValidateAgainst(evidence []Evidence) error {
	if report.Validate() != nil {
		return ErrInvalidReport
	}
	want, err := Build(evidence)
	if err != nil || !reflect.DeepEqual(report, want) {
		return ErrInvalidReport
	}
	return nil
}

func validReportContent(report Report, sealed bool) bool {
	if report.Version != ReportVersion || report.PolicyVersion != PolicyVersion ||
		report.Policy != FixedPolicy() || !validIdentity(report.Identity) || len(report.Arms) != len(requiredArms) ||
		sealed != validDigest(report.Hash) {
		return false
	}
	seenRuns, seenScorecards, seenEvaluations := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for index, arm := range report.Arms {
		if arm.ArmID != requiredArms[index] || !validText(arm.RunID, 200) ||
			!validDigest(arm.ScorecardSHA256) || !validDigest(arm.EvaluationSHA256) ||
			seenRuns[arm.RunID] || seenScorecards[arm.ScorecardSHA256] || seenEvaluations[arm.EvaluationSHA256] ||
			!validArmSummary(arm) {
			return false
		}
		seenRuns[arm.RunID], seenScorecards[arm.ScorecardSHA256], seenEvaluations[arm.EvaluationSHA256] = true, true, true
	}
	wantArms := cloneArms(report.Arms)
	wantStatus, wantDefault := decide(wantArms)
	return report.Status == wantStatus && report.DefaultArm == wantDefault && reflect.DeepEqual(report.Arms, wantArms)
}

func validIdentity(identity Identity) bool {
	return validText(identity.CorpusID, 200) && identity.CorpusRevision > 0 &&
		validText(identity.SnapshotID, 200) && validDigest(identity.SnapshotSHA256) &&
		validText(identity.ProviderID, 200) && validText(identity.Model, 200) &&
		validText(identity.PromptID, 200) && validText(identity.CodeRevision, 200) &&
		validText(identity.AdapterID, 200) && validText(identity.AdapterVersion, 200) &&
		validText(identity.JudgeModel, 200) && validDigest(identity.GroundTruthSHA256)
}

func validArmSummary(arm ArmResult) bool {
	counts := arm.Counts
	if counts.RunnerSucceeded > math.MaxUint64-counts.RunnerFailed ||
		counts.RunnerSucceeded+counts.RunnerFailed != counts.Cases ||
		counts.Scored > math.MaxUint64-counts.EvaluatorFailed ||
		counts.Scored+counts.EvaluatorFailed != counts.RunnerSucceeded ||
		arm.Performance.InputTokens > math.MaxUint64-arm.Performance.OutputTokens ||
		arm.Performance.TotalTokens != arm.Performance.InputTokens+arm.Performance.OutputTokens ||
		arm.Performance.CachedTokens > arm.Performance.InputTokens || arm.Reasons == nil ||
		!validReasonOrder(arm.Reasons) {
		return false
	}
	metrics := []answerevaluation.MetricAggregate{
		arm.Metrics.FactualCorrectness, arm.Metrics.Faithfulness,
		arm.Metrics.ContextPrecision, arm.Metrics.ContextRecall,
	}
	for _, metric := range metrics {
		if metric.Samples != counts.Scored || !validOptionalScore(metric.Mean, metric.Samples > 0) {
			return false
		}
	}
	minimums := []*float64{
		arm.Minimums.FactualCorrectness, arm.Minimums.Faithfulness,
		arm.Minimums.ContextPrecision, arm.Minimums.ContextRecall,
	}
	for _, minimum := range minimums {
		if !validOptionalScore(minimum, counts.Scored > 0) {
			return false
		}
	}
	wantIncomplete := len(incompleteReasons(arm)) != 0
	return (arm.EvidenceStatus == "incomplete") == wantIncomplete &&
		(arm.EvidenceStatus == "complete" || arm.EvidenceStatus == "incomplete")
}

func validReasonOrder(reasons []string) bool {
	allowed := map[string]bool{
		"case_count_below_minimum": true, "runner_coverage_incomplete": true,
		"evaluator_coverage_incomplete": true, "metric_samples_incomplete": true,
		"matrix_incomplete": true, "factual_correctness_mean_below_threshold": true,
		"factual_correctness_minimum_below_threshold": true,
		"faithfulness_mean_below_threshold":           true, "faithfulness_minimum_below_threshold": true,
		"context_precision_mean_below_threshold": true, "context_recall_mean_below_threshold": true,
		"factual_correctness_delta_above_threshold": true, "faithfulness_delta_above_threshold": true,
	}
	seen := map[string]bool{}
	for _, reason := range reasons {
		if !allowed[reason] || seen[reason] {
			return false
		}
		seen[reason] = true
	}
	return true
}

func validOptionalScore(value *float64, required bool) bool {
	if (value != nil) != required {
		return false
	}
	return value == nil || (!math.IsNaN(*value) && !math.IsInf(*value, 0) && *value >= 0 && *value <= 1)
}

func reportDigest(report Report) (string, error) {
	report.Hash = ""
	encoded, err := json.Marshal(report)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func validDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") {
		return false
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil && len(decoded) == sha256.Size
}

func validText(value string, maximum int) bool {
	return strings.TrimSpace(value) != "" && utf8.ValidString(value) && len(value) <= maximum &&
		!strings.ContainsRune(value, '\x00')
}
