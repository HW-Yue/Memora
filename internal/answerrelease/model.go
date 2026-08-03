// Package answerrelease builds the public release decision over F183/F184 evidence.
package answerrelease

import (
	"errors"

	"github.com/HW-Yue/Memora/internal/answerbenchmark"
	"github.com/HW-Yue/Memora/internal/answerevaluation"
)

const (
	ReportVersion = "memora.query-release-gate/v1"
	PolicyVersion = "memora.query-release-policy/v1"

	StatusPassed     = "passed"
	StatusFailed     = "failed"
	StatusIncomplete = "incomplete"
)

var (
	ErrInvalidEvidence = errors.New("query release evidence is invalid")
	ErrInvalidReport   = errors.New("query release report is invalid")
)

type Policy struct {
	Version                   string  `json:"version"`
	MinimumCases              uint64  `json:"minimum_cases"`
	FactualCorrectnessMean    float64 `json:"factual_correctness_mean"`
	FactualCorrectnessMinimum float64 `json:"factual_correctness_minimum"`
	FaithfulnessMean          float64 `json:"faithfulness_mean"`
	FaithfulnessMinimum       float64 `json:"faithfulness_minimum"`
	ContextPrecisionMean      float64 `json:"context_precision_mean"`
	ContextRecallMean         float64 `json:"context_recall_mean"`
	MaximumQualityDelta       float64 `json:"maximum_quality_delta"`
}

type Identity struct {
	CorpusID          string `json:"corpus_id"`
	CorpusRevision    uint64 `json:"corpus_revision"`
	SnapshotID        string `json:"snapshot_id"`
	SnapshotSHA256    string `json:"snapshot_sha256"`
	ProviderID        string `json:"provider_id"`
	Model             string `json:"model"`
	PromptID          string `json:"prompt_id"`
	CodeRevision      string `json:"code_revision"`
	AdapterID         string `json:"adapter_id"`
	AdapterVersion    string `json:"adapter_version"`
	JudgeModel        string `json:"judge_model"`
	GroundTruthSHA256 string `json:"ground_truth_sha256"`
}

type Evidence struct {
	Scorecard  answerbenchmark.PublicScorecard
	Evaluation answerevaluation.Report
}

type ArmResult struct {
	ArmID            string                             `json:"arm_id"`
	RunID            string                             `json:"run_id"`
	ScorecardSHA256  string                             `json:"scorecard_sha256"`
	EvaluationSHA256 string                             `json:"evaluation_sha256"`
	EvidenceStatus   string                             `json:"evidence_status"`
	Counts           answerevaluation.ReportCounts      `json:"counts"`
	Performance      answerevaluation.ReportPerformance `json:"performance"`
	Metrics          answerevaluation.ReportMetrics     `json:"metrics"`
	Minimums         answerevaluation.MetricScores      `json:"minimums"`
	Eligible         bool                               `json:"eligible"`
	Reasons          []string                           `json:"reasons"`
}

type Report struct {
	Version       string      `json:"version"`
	PolicyVersion string      `json:"policy_version"`
	Policy        Policy      `json:"policy"`
	Identity      Identity    `json:"identity"`
	Status        string      `json:"status"`
	DefaultArm    string      `json:"default_arm,omitempty"`
	Arms          []ArmResult `json:"arms"`
	Hash          string      `json:"hash"`
}

func FixedPolicy() Policy {
	return Policy{
		Version: PolicyVersion, MinimumCases: 12,
		FactualCorrectnessMean: 0.85, FactualCorrectnessMinimum: 0.50,
		FaithfulnessMean: 0.90, FaithfulnessMinimum: 0.80,
		ContextPrecisionMean: 0.80, ContextRecallMean: 0.85,
		MaximumQualityDelta: 0.03,
	}
}
