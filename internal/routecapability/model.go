package routecapability

import (
	"fmt"

	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/routebenchmarkrunner"
)

const (
	ReportVersion    = "memora.route-capability-report/v1"
	PolicyVersion    = "memora.route-capability-policy/v1"
	PriceCardVersion = "memora.route-price-card/v1"
)

type SourceReport struct {
	Hash string `json:"hash"`
	Runs uint64 `json:"runs"`
}

type BucketKey struct {
	Slice string                   `json:"slice"`
	Value string                   `json:"value"`
	Arm   routebenchmarkrunner.Arm `json:"arm"`
}

type Proportion struct {
	Numerator   uint64  `json:"numerator"`
	Denominator uint64  `json:"denominator"`
	Value       float64 `json:"value"`
	Lower95     float64 `json:"lower_95"`
	Upper95     float64 `json:"upper_95"`
}

type Metrics struct {
	LevelTop1       Proportion `json:"level_top1"`
	ExactPath       Proportion `json:"exact_path"`
	RowIDSuccess    Proportion `json:"rowid_success"`
	PredictorRecall Proportion `json:"predictor_recall_at_k"`
	PrefetchHit     Proportion `json:"prefetch_hit_rate"`
}

type Distribution struct {
	P50 uint64 `json:"p50"`
	P95 uint64 `json:"p95"`
}

type Latency struct {
	QueryEmbedding Distribution `json:"query_embedding_ms"`
	CPUScan        Distribution `json:"cpu_scan_ms"`
	MSQL           Distribution `json:"msql_ms"`
	Model          Distribution `json:"model_ms"`
	EndToEnd       Distribution `json:"end_to_end_ms"`
}

type Delta struct {
	ModelCallsSavedPerRun float64 `json:"model_calls_saved_per_run"`
	TokensSavedPerRun     float64 `json:"tokens_saved_per_run"`
}

type VectorMaximums struct {
	GenerationBytes uint64 `json:"generation_bytes"`
	RebuildMillis   uint64 `json:"rebuild_ms"`
	PeakMemoryBytes uint64 `json:"peak_memory_bytes"`
}

type Cost struct {
	Available      bool   `json:"available"`
	InputMicroUSD  uint64 `json:"input_micro_usd"`
	OutputMicroUSD uint64 `json:"output_micro_usd"`
	TotalMicroUSD  uint64 `json:"total_micro_usd"`
}

type Bucket struct {
	Key          BucketKey                   `json:"key"`
	Counts       routebenchmarkrunner.Counts `json:"counts"`
	FailureCells uint64                      `json:"failure_cells"`
	Metrics      Metrics                     `json:"metrics"`
	Latency      Latency                     `json:"latency"`
	Delta        Delta                       `json:"router_delta"`
	Vector       VectorMaximums              `json:"vector_maximums"`
	Cost         Cost                        `json:"cost"`
}

type ArmDecision struct {
	Arm      routebenchmarkrunner.Arm `json:"arm"`
	Eligible bool                     `json:"eligible"`
	Reasons  []string                 `json:"reasons"`
}

type Decision struct {
	PolicyVersion string                   `json:"policy_version"`
	DefaultArm    routebenchmarkrunner.Arm `json:"default_arm"`
	Arms          []ArmDecision            `json:"arms"`
}

type Report struct {
	Version         string         `json:"version"`
	PolicyVersion   string         `json:"policy_version"`
	CorpusID        string         `json:"corpus_id"`
	CorpusDigest    string         `json:"corpus_digest"`
	ContractDigest  string         `json:"contract_digest"`
	SkillDigest     string         `json:"skill_digest"`
	ProtocolDigest  string         `json:"protocol_digest"`
	SourceReports   []SourceReport `json:"source_reports"`
	PriceCardDigest string         `json:"price_card_digest,omitempty"`
	Buckets         []Bucket       `json:"buckets"`
	Decision        Decision       `json:"decision"`
	Hash            string         `json:"hash"`
}

type Price struct {
	Provider                 string `json:"provider"`
	Model                    string `json:"model"`
	InputMicroUSDPerMillion  uint64 `json:"input_micro_usd_per_million"`
	OutputMicroUSDPerMillion uint64 `json:"output_micro_usd_per_million"`
}

type PriceCard struct {
	Version string  `json:"version"`
	ID      string  `json:"id"`
	Entries []Price `json:"entries"`
	Hash    string  `json:"hash"`
}

type Error struct {
	Code    result.Code
	Message string
}

func (err *Error) Error() string      { return "Route capability report: " + err.Message }
func (err *Error) StableCode() string { return string(err.Code) }

func capabilityError(code result.Code, format string, arguments ...any) error {
	return &Error{Code: code, Message: fmt.Sprintf(format, arguments...)}
}
