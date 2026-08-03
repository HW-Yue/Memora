package answerevaluation

import "errors"

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

func (report *Report) Seal() error    { return ErrInvalidReport }
func (report Report) Validate() error { return ErrInvalidReport }
