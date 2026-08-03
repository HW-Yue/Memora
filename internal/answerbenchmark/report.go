package answerbenchmark

import (
	"errors"

	"github.com/HW-Yue/Memora/internal/agent"
	"github.com/HW-Yue/Memora/internal/answercorpus"
)

const (
	PublicScorecardVersion    = "memora.answer-scorecard/v1"
	PrivateDiagnosticsVersion = "memora.answer-diagnostics/v1"
	QualityNotScored          = "not_scored"
)

var ErrInvalidReport = errors.New("answer benchmark report is invalid")

type PublicCase struct {
	CaseID         string `json:"case_id"`
	Question       string `json:"question"`
	Status         string `json:"status"`
	ErrorCode      string `json:"error_code,omitempty"`
	Answer         string `json:"answer,omitempty"`
	QualityStatus  string `json:"quality_status"`
	DurationMicros uint64 `json:"duration_micros"`
	ProviderCalls  uint64 `json:"provider_calls"`
	MSQLCalls      uint64 `json:"msql_calls"`
	ToolCalls      uint64 `json:"tool_calls"`
	InputTokens    uint64 `json:"input_tokens"`
	CachedTokens   uint64 `json:"cached_input_tokens"`
	OutputTokens   uint64 `json:"output_tokens"`
	TotalTokens    uint64 `json:"total_tokens"`
}

type PublicScorecard struct {
	Version        string       `json:"version"`
	RunID          string       `json:"run_id"`
	CorpusID       string       `json:"corpus_id"`
	CorpusRevision uint64       `json:"corpus_revision"`
	SnapshotID     string       `json:"snapshot_id"`
	SnapshotSHA256 string       `json:"snapshot_sha256"`
	ProviderID     string       `json:"provider_id"`
	Model          string       `json:"model"`
	ArmID          string       `json:"arm_id"`
	PromptID       string       `json:"prompt_id"`
	CodeRevision   string       `json:"code_revision"`
	QualityStatus  string       `json:"quality_status"`
	Cases          []PublicCase `json:"cases"`
	Hash           string       `json:"hash"`
}

func (scorecard PublicScorecard) Validate() error { return ErrInvalidReport }

type PrivateCase struct {
	Task     answercorpus.BlindTask `json:"task"`
	Error    string                 `json:"error,omitempty"`
	Evidence []agent.SelectEvidence `json:"evidence"`
	Trace    agent.TraceEnvelope    `json:"trace"`
}

type PrivateDiagnostics struct {
	Version               string                 `json:"version"`
	RunID                 string                 `json:"run_id"`
	PublicScorecardSHA256 string                 `json:"public_scorecard_sha256"`
	Materialization       MaterializationReceipt `json:"materialization"`
	Cases                 []PrivateCase          `json:"cases"`
	Hash                  string                 `json:"hash"`
}

func (diagnostics PrivateDiagnostics) Validate() error { return ErrInvalidReport }
