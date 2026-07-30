package benchmark

import (
	"context"
	"fmt"

	"github.com/HW-Yue/Memora/internal/result"
)

const (
	SuiteVersion  = "memora.ai-benchmark/v1"
	ReportVersion = "memora.ai-benchmark-report/v1"
)

type Kind string

const (
	KindMultiProject Kind = "multi_project"
	KindRevision     Kind = "revision"
	KindAssimilation Kind = "assimilation"
	KindColdStart    Kind = "cold_start"
	KindHostSwitch   Kind = "host_switch"
)

type Suite struct {
	Version   string     `json:"version"`
	ID        string     `json:"id"`
	Scenarios []Scenario `json:"scenarios"`
}

type Scenario struct {
	ID    string   `json:"id"`
	Kind  Kind     `json:"kind"`
	Hosts []string `json:"hosts"`
	Turns []Turn   `json:"turns"`
}

type Turn struct {
	ID       string `json:"id"`
	Project  string `json:"project,omitempty"`
	Intent   string `json:"intent"`
	Expected string `json:"expected"`
}

type Counts struct {
	WritesCorrect             uint64 `json:"writes_correct"`
	WritesAttempted           uint64 `json:"writes_attempted"`
	WritesExpected            uint64 `json:"writes_expected"`
	SourceUnitsRead           uint64 `json:"source_units_read"`
	SourceUnitsRequired       uint64 `json:"source_units_required"`
	ClaimsCorrect             uint64 `json:"claims_correct"`
	ClaimsChecked             uint64 `json:"claims_checked"`
	AnchorsTraceable          uint64 `json:"anchors_traceable"`
	AnchorsChecked            uint64 `json:"anchors_checked"`
	SchemaDuplicates          uint64 `json:"schema_duplicates"`
	SchemaObjects             uint64 `json:"schema_objects"`
	RetrievalHitsAt5          uint64 `json:"retrieval_hits_at_5"`
	RetrievalQueries          uint64 `json:"retrieval_queries"`
	ReciprocalRankMilli       uint64 `json:"reciprocal_rank_milli"`
	NDCGMilli                 uint64 `json:"ndcg_milli"`
	ContextCharacters         uint64 `json:"context_characters"`
	ToolCalls                 uint64 `json:"tool_calls"`
	AnsweredTurns             uint64 `json:"answered_turns"`
	IrrelevantRows            uint64 `json:"irrelevant_rows"`
	SelectedRows              uint64 `json:"selected_rows"`
	UnintendedRows            uint64 `json:"unintended_rows"`
	MutatedRows               uint64 `json:"mutated_rows"`
	RevisionConflictsCaptured uint64 `json:"revision_conflicts_captured"`
	RevisionConflicts         uint64 `json:"revision_conflicts"`
	TakeoversSucceeded        uint64 `json:"takeovers_succeeded"`
	Takeovers                 uint64 `json:"takeovers"`
	RecoveriesSucceeded       uint64 `json:"recoveries_succeeded"`
	Recoveries                uint64 `json:"recoveries"`
	IndexChecksPassed         uint64 `json:"index_checks_passed"`
	IndexChecks               uint64 `json:"index_checks"`
	ExportHashesMatched       uint64 `json:"export_hashes_matched"`
	ExportChecks              uint64 `json:"export_checks"`
}

type Outcome struct {
	HostResultsEquivalent bool   `json:"host_results_equivalent"`
	Counts                Counts `json:"counts"`
}

type Metrics struct {
	WritePrecision              float64 `json:"write_precision"`
	WriteRecall                 float64 `json:"write_recall"`
	AssimilationCoverage        float64 `json:"assimilation_coverage"`
	ClaimAccuracy               float64 `json:"claim_accuracy"`
	AnchorTraceability          float64 `json:"anchor_traceability"`
	SchemaDuplicateRate         float64 `json:"schema_duplicate_rate"`
	RecallAt5                   float64 `json:"recall_at_5"`
	MRR                         float64 `json:"mrr"`
	NDCG                        float64 `json:"ndcg"`
	MeanContextCharacters       float64 `json:"mean_context_characters"`
	MeanToolCalls               float64 `json:"mean_tool_calls"`
	IrrelevantRowRate           float64 `json:"irrelevant_row_rate"`
	UnintendedRowRate           float64 `json:"unintended_row_rate"`
	RevisionConflictCaptureRate float64 `json:"revision_conflict_capture_rate"`
	TakeoverSuccessRate         float64 `json:"takeover_success_rate"`
	CrashRecoveryRate           float64 `json:"crash_recovery_rate"`
	IndexConsistencyRate        float64 `json:"index_consistency_rate"`
	DeterministicExportRate     float64 `json:"deterministic_export_rate"`
}

type ScenarioReport struct {
	ScenarioID            string  `json:"scenario_id"`
	Kind                  Kind    `json:"kind"`
	HostResultsEquivalent bool    `json:"host_results_equivalent"`
	Counts                Counts  `json:"counts"`
	Metrics               Metrics `json:"metrics"`
}

type Report struct {
	Version       string           `json:"version"`
	SuiteID       string           `json:"suite_id"`
	SuiteDigest   string           `json:"suite_digest"`
	Adapter       string           `json:"adapter"`
	Dimensions    int              `json:"dimensions"`
	Scenarios     []ScenarioReport `json:"scenarios"`
	Aggregate     Metrics          `json:"aggregate"`
	AllHostsEqual bool             `json:"all_hosts_equal"`
	Evidence      *Evidence        `json:"evidence,omitempty"`
	Hash          string           `json:"hash"`
}

type Adapter interface {
	Name() string
	Run(context.Context, Scenario) (Outcome, error)
}

type Evidence struct {
	Mode            string `json:"mode"`
	Protocol        string `json:"protocol,omitempty"`
	Provider        string `json:"provider,omitempty"`
	EndpointHost    string `json:"endpoint_host,omitempty"`
	Model           string `json:"model,omitempty"`
	InputTokens     uint64 `json:"input_tokens,omitempty"`
	OutputTokens    uint64 `json:"output_tokens,omitempty"`
	OutputDigest    string `json:"output_digest"`
	ExecutionDigest string `json:"execution_digest"`
	CorpusDigest    string `json:"corpus_digest"`
	Implementation  string `json:"implementation"`
}

type evidenceAdapter interface {
	Evidence() Evidence
}

type Error struct {
	Code    result.Code
	Message string
}

func (err *Error) Error() string      { return "AI-native benchmark: " + err.Message }
func (err *Error) StableCode() string { return string(err.Code) }

func benchmarkError(code result.Code, format string, arguments ...any) error {
	return &Error{Code: code, Message: fmt.Sprintf(format, arguments...)}
}
