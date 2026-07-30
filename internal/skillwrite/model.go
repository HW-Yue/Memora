package skillwrite

import (
	"context"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
)

const (
	PlanVersion    = "memora.mutation-plan/v1"
	ReceiptVersion = "memora.mutation-receipt/v1"
)

type Decision string

const (
	DecisionIgnore Decision = "IGNORE"
	DecisionInsert Decision = "INSERT"
	DecisionRevise Decision = "REVISE"
	DecisionMerge  Decision = "MERGE"
	DecisionSplit  Decision = "SPLIT"
	DecisionMove   Decision = "MOVE"
	DecisionRelate Decision = "RELATE"
)

type Plan struct {
	Version             string   `json:"version"`
	ID                  string   `json:"id"`
	Decision            Decision `json:"decision"`
	Database            string   `json:"database"`
	Table               string   `json:"table"`
	Actor               string   `json:"actor"`
	SourceEventID       string   `json:"source_event_id"`
	Reason              string   `json:"reason"`
	AuthorizedDatabases []string `json:"authorized_databases"`
	Preflight           []Check  `json:"preflight"`
	Steps               []Step   `json:"steps"`
	Verify              []Check  `json:"verify"`
}

type Check struct {
	ID          string                  `json:"id"`
	MSQL        string                  `json:"msql"`
	Input       executor.StatementInput `json:"input,omitempty"`
	ExpectRows  *int                    `json:"expect_rows,omitempty"`
	RowContains map[string]any          `json:"row_contains,omitempty"`
}

type Step struct {
	ID     string                  `json:"id"`
	Kind   string                  `json:"kind"`
	Target string                  `json:"target"`
	MSQL   string                  `json:"msql"`
	Input  executor.StatementInput `json:"input"`
}

type Phase string

const (
	PhasePreflight Phase = "preflight"
	PhaseMutation  Phase = "mutation"
	PhaseVerify    Phase = "verify"
)

type Call struct {
	Phase   Phase
	Request executor.BatchRequest
}

type Tool interface {
	Invoke(context.Context, Call) (result.Envelope, error)
}

type ToolFunc func(context.Context, Call) (result.Envelope, error)

func (function ToolFunc) Invoke(ctx context.Context, call Call) (result.Envelope, error) {
	return function(ctx, call)
}

type ReceiptStatus string

const (
	ReceiptCommitted           ReceiptStatus = "committed"
	ReceiptCommittedUnverified ReceiptStatus = "committed_unverified"
	ReceiptIgnored             ReceiptStatus = "ignored"
)

type Change struct {
	Operation      string  `json:"operation"`
	Target         string  `json:"target"`
	Revision       *uint64 `json:"revision,omitempty"`
	CommitSequence *uint64 `json:"commit_sequence,omitempty"`
}

type Receipt struct {
	Version  string          `json:"version"`
	PlanID   string          `json:"plan_id"`
	Decision Decision        `json:"decision"`
	Status   ReceiptStatus   `json:"status"`
	Changes  []Change        `json:"changes"`
	Ignored  int             `json:"ignored"`
	Verified bool            `json:"verified"`
	Warnings []result.Notice `json:"warnings"`
}

type Report struct {
	Calls   []Call
	Receipt Receipt
}

type Error struct {
	Code    result.Code
	Message string
}

func (err *Error) Error() string {
	return "Skill mutation: " + err.Message
}

func (err *Error) StableCode() string {
	return string(err.Code)
}
