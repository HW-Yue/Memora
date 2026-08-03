package answerevaluation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"reflect"
	"sort"
)

const ReportVersion = "memora.answer-evaluation-report/v1"

var ErrInvalidReport = errors.New("external answer evaluation report is invalid")

type MetricAggregate struct {
	Samples uint64   `json:"samples"`
	Mean    *float64 `json:"mean,omitempty"`
}

type ReportCounts struct {
	Cases           uint64 `json:"cases"`
	RunnerSucceeded uint64 `json:"runner_succeeded"`
	RunnerFailed    uint64 `json:"runner_failed"`
	Scored          uint64 `json:"scored"`
	EvaluatorFailed uint64 `json:"evaluator_failed"`
}

type ReportPerformance struct {
	LatencyP50Micros uint64 `json:"latency_p50_micros"`
	LatencyP95Micros uint64 `json:"latency_p95_micros"`
	ProviderCalls    uint64 `json:"provider_calls"`
	MSQLCalls        uint64 `json:"msql_calls"`
	ToolCalls        uint64 `json:"tool_calls"`
	InputTokens      uint64 `json:"input_tokens"`
	CachedTokens     uint64 `json:"cached_input_tokens"`
	OutputTokens     uint64 `json:"output_tokens"`
	TotalTokens      uint64 `json:"total_tokens"`
}

type ReportMetrics struct {
	FactualCorrectness MetricAggregate `json:"factual_correctness"`
	Faithfulness       MetricAggregate `json:"faithfulness"`
	ContextPrecision   MetricAggregate `json:"context_precision"`
	ContextRecall      MetricAggregate `json:"context_recall"`
}

type ReportCase struct {
	CaseID         string       `json:"case_id"`
	Status         string       `json:"status"`
	ErrorCode      string       `json:"error_code,omitempty"`
	DurationMicros uint64       `json:"duration_micros"`
	ProviderCalls  uint64       `json:"provider_calls"`
	MSQLCalls      uint64       `json:"msql_calls"`
	ToolCalls      uint64       `json:"tool_calls"`
	InputTokens    uint64       `json:"input_tokens"`
	CachedTokens   uint64       `json:"cached_input_tokens"`
	OutputTokens   uint64       `json:"output_tokens"`
	TotalTokens    uint64       `json:"total_tokens"`
	Scores         MetricScores `json:"scores"`
}

type Report struct {
	Version                  string            `json:"version"`
	RunID                    string            `json:"run_id"`
	CorpusID                 string            `json:"corpus_id"`
	CorpusRevision           uint64            `json:"corpus_revision"`
	SnapshotSHA256           string            `json:"snapshot_sha256"`
	PublicScorecardSHA256    string            `json:"public_scorecard_sha256"`
	PrivateDiagnosticsSHA256 string            `json:"private_diagnostics_sha256"`
	GroundTruthSHA256        string            `json:"ground_truth_sha256"`
	EvaluatorInputSHA256     string            `json:"evaluator_input_sha256"`
	EvaluatorOutputSHA256    string            `json:"evaluator_output_sha256"`
	AdapterID                string            `json:"adapter_id"`
	AdapterVersion           string            `json:"adapter_version"`
	JudgeModel               string            `json:"judge_model"`
	Counts                   ReportCounts      `json:"counts"`
	Performance              ReportPerformance `json:"performance"`
	Metrics                  ReportMetrics     `json:"metrics"`
	Cases                    []ReportCase      `json:"cases"`
	Hash                     string            `json:"hash"`
}

func (report *Report) Seal() error {
	if report == nil || !validReport(*report, false) {
		return ErrInvalidReport
	}
	digest, err := evaluationReportDigest(*report)
	if err != nil {
		return ErrInvalidReport
	}
	report.Hash = digest
	return nil
}

func (report Report) Validate() error {
	if !validReport(report, true) {
		return ErrInvalidReport
	}
	digest, err := evaluationReportDigest(report)
	if err != nil || report.Hash != digest {
		return ErrInvalidReport
	}
	return nil
}

func validReport(report Report, sealed bool) bool {
	if report.Version != ReportVersion || !validText(report.RunID, 200) ||
		!validText(report.CorpusID, 200) || report.CorpusRevision == 0 ||
		!validDigest(report.SnapshotSHA256) || !validDigest(report.PublicScorecardSHA256) ||
		!validDigest(report.PrivateDiagnosticsSHA256) || !validDigest(report.GroundTruthSHA256) ||
		!validDigest(report.EvaluatorInputSHA256) || !validDigest(report.EvaluatorOutputSHA256) ||
		!validText(report.AdapterID, 200) || !validText(report.AdapterVersion, 200) ||
		!validText(report.JudgeModel, 200) || report.Cases == nil || len(report.Cases) == 0 ||
		sealed != validDigest(report.Hash) {
		return false
	}
	counts, performance, metrics, ok := aggregateReportCases(report.Cases)
	return ok && reflect.DeepEqual(report.Counts, counts) &&
		reflect.DeepEqual(report.Performance, performance) && reflect.DeepEqual(report.Metrics, metrics)
}

func aggregateReportCases(cases []ReportCase) (ReportCounts, ReportPerformance, ReportMetrics, bool) {
	counts := ReportCounts{Cases: uint64(len(cases))}
	performance := ReportPerformance{}
	latencies := make([]uint64, 0, len(cases))
	factual, faithfulness := []*float64{}, []*float64{}
	precision, recall := []*float64{}, []*float64{}
	previous := ""
	for _, testCase := range cases {
		if !validText(testCase.CaseID, 200) || testCase.CaseID <= previous ||
			testCase.TotalTokens != testCase.InputTokens+testCase.OutputTokens ||
			testCase.CachedTokens > testCase.InputTokens {
			return ReportCounts{}, ReportPerformance{}, ReportMetrics{}, false
		}
		switch testCase.Status {
		case "scored":
			if testCase.ErrorCode != "" || !validScores(testCase.Scores, true) {
				return ReportCounts{}, ReportPerformance{}, ReportMetrics{}, false
			}
			counts.RunnerSucceeded++
			counts.Scored++
			factual = append(factual, testCase.Scores.FactualCorrectness)
			faithfulness = append(faithfulness, testCase.Scores.Faithfulness)
			precision = append(precision, testCase.Scores.ContextPrecision)
			recall = append(recall, testCase.Scores.ContextRecall)
		case "evaluator_failed":
			if !validErrorCode(testCase.ErrorCode) || !validScores(testCase.Scores, false) {
				return ReportCounts{}, ReportPerformance{}, ReportMetrics{}, false
			}
			counts.RunnerSucceeded++
			counts.EvaluatorFailed++
		case "runner_failed":
			if testCase.ErrorCode != "" || !validScores(testCase.Scores, false) {
				return ReportCounts{}, ReportPerformance{}, ReportMetrics{}, false
			}
			counts.RunnerFailed++
		default:
			return ReportCounts{}, ReportPerformance{}, ReportMetrics{}, false
		}
		latencies = append(latencies, testCase.DurationMicros)
		if !addPerformance(&performance, testCase) {
			return ReportCounts{}, ReportPerformance{}, ReportMetrics{}, false
		}
		previous = testCase.CaseID
	}
	sort.Slice(latencies, func(left, right int) bool { return latencies[left] < latencies[right] })
	performance.LatencyP50Micros = nearestRank(latencies, 50)
	performance.LatencyP95Micros = nearestRank(latencies, 95)
	metrics := ReportMetrics{
		FactualCorrectness: metricAggregate(factual), Faithfulness: metricAggregate(faithfulness),
		ContextPrecision: metricAggregate(precision), ContextRecall: metricAggregate(recall),
	}
	return counts, performance, metrics, true
}

func addPerformance(performance *ReportPerformance, testCase ReportCase) bool {
	values := []struct {
		target *uint64
		value  uint64
	}{
		{&performance.ProviderCalls, testCase.ProviderCalls}, {&performance.MSQLCalls, testCase.MSQLCalls},
		{&performance.ToolCalls, testCase.ToolCalls}, {&performance.InputTokens, testCase.InputTokens},
		{&performance.CachedTokens, testCase.CachedTokens}, {&performance.OutputTokens, testCase.OutputTokens},
		{&performance.TotalTokens, testCase.TotalTokens},
	}
	for _, item := range values {
		if math.MaxUint64-*item.target < item.value {
			return false
		}
		*item.target += item.value
	}
	return true
}

func nearestRank(sorted []uint64, percentile uint64) uint64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := (percentile*uint64(len(sorted)) + 99) / 100
	return sorted[rank-1]
}

func metricAggregate(values []*float64) MetricAggregate {
	result := MetricAggregate{Samples: uint64(len(values))}
	if len(values) == 0 {
		return result
	}
	total := new(big.Rat)
	for _, value := range values {
		total.Add(total, new(big.Rat).SetFloat64(*value))
	}
	total.Quo(total, new(big.Rat).SetInt64(int64(len(values))))
	mean, _ := total.Float64()
	result.Mean = &mean
	return result
}

func evaluationReportDigest(report Report) (string, error) {
	report.Hash = ""
	encoded, err := json.Marshal(report)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}
