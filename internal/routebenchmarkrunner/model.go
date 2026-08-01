package routebenchmarkrunner

import (
	"context"
	"fmt"

	"github.com/HW-Yue/Memora/internal/hostcontract"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/routebenchmark"
)

const (
	RunnerVersion      = "memora.route-benchmark-runner/v1"
	RequestVersion     = "memora.route-benchmark-request/v1"
	ObservationVersion = "memora.route-benchmark-observation/v1"
	ReportVersion      = "memora.route-benchmark-report/v1"
)

type Arm string

const (
	ArmRouter         Arm = "router"
	ArmLexical        Arm = "lexical"
	ArmVector         Arm = "vector"
	ArmHybrid         Arm = "hybrid"
	ArmHybridPrefetch Arm = "hybrid_prefetch"
)

var canonicalArms = []Arm{ArmRouter, ArmLexical, ArmVector, ArmHybrid, ArmHybridPrefetch}

func Arms() []Arm { return append([]Arm(nil), canonicalArms...) }

type Fixture struct {
	Snapshot string                  `json:"snapshot"`
	Database routebenchmark.Database `json:"database"`
	Table    routebenchmark.Table    `json:"table"`
	Nodes    []routebenchmark.Node   `json:"nodes"`
}

type Request struct {
	Version        string                   `json:"version"`
	RunID          string                   `json:"run_id"`
	Arm            Arm                      `json:"arm"`
	TaskDigest     string                   `json:"task_digest"`
	SkillDigest    string                   `json:"skill_digest"`
	ProtocolDigest string                   `json:"protocol_digest"`
	CorpusDigest   string                   `json:"corpus_digest"`
	Host           hostcontract.HostProfile `json:"host"`
	Task           hostcontract.Task        `json:"task"`
	Fixture        Fixture                  `json:"fixture"`
}

type Timing struct {
	QueryEmbeddingMillis uint64 `json:"query_embedding_ms"`
	CPUScanMillis        uint64 `json:"cpu_scan_ms"`
	MSQLMillis           uint64 `json:"msql_ms"`
	ModelMillis          uint64 `json:"model_ms"`
	EndToEndMillis       uint64 `json:"end_to_end_ms"`
}

type VectorCost struct {
	GenerationBytes     uint64 `json:"generation_bytes"`
	RebuildMillis       uint64 `json:"rebuild_ms"`
	PeakMemoryBytes     uint64 `json:"peak_memory_bytes"`
	QueryEmbeddingCount uint64 `json:"query_embedding_count"`
}

type Observation struct {
	Version                string               `json:"version"`
	RunID                  string               `json:"run_id"`
	TaskDigest             string               `json:"task_digest"`
	SkillDigest            string               `json:"skill_digest"`
	ProtocolDigest         string               `json:"protocol_digest"`
	CorpusDigest           string               `json:"corpus_digest"`
	Snapshot               string               `json:"snapshot"`
	Arm                    Arm                  `json:"arm"`
	Receipt                hostcontract.Receipt `json:"receipt"`
	LevelChoices           []string             `json:"level_choices"`
	SelectedRowID          string               `json:"selected_row_id,omitempty"`
	FactReadRowIDs         []string             `json:"fact_read_row_ids"`
	PredictorRouteIDs      []string             `json:"predictor_route_ids"`
	PrefetchedRootRouteIDs []string             `json:"prefetched_root_route_ids"`
	FallbackCalls          uint64               `json:"fallback_calls"`
	ModelCalls             uint64               `json:"model_calls"`
	MispredictTokens       uint64               `json:"mispredict_tokens"`
	Truncations            uint64               `json:"truncations"`
	PermissionDenials      uint64               `json:"permission_denials"`
	Timing                 Timing               `json:"timing"`
	Vector                 VectorCost           `json:"vector"`
}

type Driver interface {
	Run(context.Context, Request) (Observation, error)
}

type Profile struct {
	Host   hostcontract.HostProfile
	Driver Driver
}

type Options struct {
	Contract       hostcontract.Contract
	ContractDigest string
	SkillDigest    string
	ProtocolDigest string
	Profiles       []Profile
}

type Error struct {
	Code    result.Code
	Message string
}

func (err *Error) Error() string      { return "Route benchmark runner: " + err.Message }
func (err *Error) StableCode() string { return string(err.Code) }

func runnerError(code result.Code, format string, arguments ...any) error {
	return &Error{Code: code, Message: fmt.Sprintf(format, arguments...)}
}
