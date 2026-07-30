package conversation

import (
	"fmt"

	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/skillwrite"
)

const (
	EventVersion   = "memora.conversation-event/v1"
	ReceiptVersion = "memora.conversation-receipt/v1"
)

type Kind string

const (
	KindDelta      Kind = "delta"
	KindCheckpoint Kind = "checkpoint"
	KindSessionEnd Kind = "session_end"
)

type Disposition string

const (
	DispositionIgnore  Disposition = "ignore"
	DispositionPersist Disposition = "persist"
)

type Event struct {
	Version             string      `json:"version"`
	EventID             string      `json:"event_id"`
	SessionID           string      `json:"session_id"`
	Kind                Kind        `json:"kind"`
	Workspace           string      `json:"workspace"`
	AuthorizedDatabases []string    `json:"authorized_databases"`
	Deltas              []Delta     `json:"deltas,omitempty"`
	Checkpoint          *Checkpoint `json:"checkpoint,omitempty"`
}

type Delta struct {
	ID          string           `json:"id"`
	Disposition Disposition      `json:"disposition"`
	Reason      string           `json:"reason,omitempty"`
	Database    string           `json:"database,omitempty"`
	Plan        *skillwrite.Plan `json:"plan,omitempty"`
}

type Checkpoint struct {
	ActiveDatabase string `json:"active_database"`
	RoutePath      string `json:"route_path,omitempty"`
	LastEventID    string `json:"last_event_id"`
}

type StoredCheckpoint struct {
	Checkpoint
	SessionID      string `json:"session_id"`
	Workspace      string `json:"workspace"`
	UpdatedEventID string `json:"updated_event_id"`
}

type Status string

const (
	StatusProcessed    Status = "processed"
	StatusNeedsContext Status = "needs_context"
	StatusCheckpointed Status = "checkpointed"
	StatusEnded        Status = "ended"
)

type Receipt struct {
	Version   string               `json:"version"`
	EventID   string               `json:"event_id"`
	SessionID string               `json:"session_id"`
	Status    Status               `json:"status"`
	Replayed  bool                 `json:"replayed"`
	Ignored   int                  `json:"ignored"`
	Mutations []skillwrite.Receipt `json:"mutations"`
	Missing   []string             `json:"missing"`
}

type Error struct {
	Code    result.Code
	Message string
}

func (err *Error) Error() string      { return "conversation delta: " + err.Message }
func (err *Error) StableCode() string { return string(err.Code) }

func conversationError(code result.Code, format string, arguments ...any) error {
	return &Error{Code: code, Message: fmt.Sprintf(format, arguments...)}
}
