package feedback

import (
	"context"
	"fmt"

	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/skillwrite"
)

const (
	EventVersion               = "memora.feedback-event/v1"
	ReceiptVersion             = "memora.feedback-receipt/v1"
	ConfirmationVersion        = "memora.feedback-confirmation/v1"
	ConfirmationReceiptVersion = "memora.feedback-confirmation-receipt/v1"
)

type Kind string

const (
	KindUseful     Kind = "useful"
	KindIrrelevant Kind = "irrelevant"
	KindStale      Kind = "stale"
	KindWrong      Kind = "wrong"
	KindIncomplete Kind = "incomplete"
)

type Target struct {
	Database string `json:"database"`
	Table    string `json:"table"`
	RowID    string `json:"row_id"`
	Revision uint64 `json:"revision"`
}

type Event struct {
	Version string `json:"version"`
	EventID string `json:"event_id"`
	Kind    Kind   `json:"kind"`
	Actor   string `json:"actor"`
	Reason  string `json:"reason"`
	Target  Target `json:"target"`
}

type Receipt struct {
	Version  string `json:"version"`
	EventID  string `json:"event_id"`
	Kind     Kind   `json:"kind"`
	Status   string `json:"status"`
	Replayed bool   `json:"replayed"`
	Target   Target `json:"target"`
}

type Action string

const (
	ActionIgnore Action = "ignore"
	ActionRevise Action = "revise"
	ActionUndo   Action = "undo"
)

type Undo struct {
	TargetRevision        uint64   `json:"target_revision"`
	ExpectedSchemaVersion uint64   `json:"expected_schema_version"`
	RouteLeafIDs          []string `json:"route_leaf_ids"`
}

type Confirmation struct {
	Version             string           `json:"version"`
	ConfirmationID      string           `json:"confirmation_id"`
	FeedbackEventID     string           `json:"feedback_event_id"`
	SourceEventID       string           `json:"source_event_id"`
	Actor               string           `json:"actor"`
	Instruction         string           `json:"instruction"`
	Action              Action           `json:"action"`
	ExpectedRevision    uint64           `json:"expected_revision"`
	AuthorizedDatabases []string         `json:"authorized_databases"`
	Plan                *skillwrite.Plan `json:"plan,omitempty"`
	Undo                *Undo            `json:"undo,omitempty"`
}

type ConfirmationReceipt struct {
	Version          string          `json:"version"`
	ConfirmationID   string          `json:"confirmation_id"`
	FeedbackEventID  string          `json:"feedback_event_id"`
	Action           Action          `json:"action"`
	Status           string          `json:"status"`
	Replayed         bool            `json:"replayed"`
	Target           Target          `json:"target"`
	NewRevision      *uint64         `json:"new_revision,omitempty"`
	RestoredRevision *uint64         `json:"restored_revision,omitempty"`
	CommitSequence   *uint64         `json:"commit_sequence,omitempty"`
	Verified         bool            `json:"verified"`
	Warnings         []result.Notice `json:"warnings"`
}

type Reader interface {
	Get(context.Context, string, string, string) (row.Row, error)
	AsOfRevision(context.Context, string, string, string, uint64) (row.Row, error)
}

type Error struct {
	Code    result.Code
	Message string
}

func (err *Error) Error() string      { return "feedback: " + err.Message }
func (err *Error) StableCode() string { return string(err.Code) }

func feedbackError(code result.Code, format string, arguments ...any) error {
	return &Error{Code: code, Message: fmt.Sprintf(format, arguments...)}
}
