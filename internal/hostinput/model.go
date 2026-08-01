package hostinput

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/result"
)

const (
	InputVersion          = "memora.host-input/v1"
	ReceiptVersion        = "memora.host-input-receipt/v1"
	MaximumCandidateBytes = 12_000
	MaximumPendingInputs  = 256
)

type Status string

const StatusPending Status = "pending"

type Source struct {
	Kind          history.SourceKind `json:"kind"`
	Title         string             `json:"title"`
	Locator       string             `json:"locator,omitempty"`
	ContentSHA256 string             `json:"content_sha256,omitempty"`
}

type Input struct {
	Version             string   `json:"version"`
	InputID             string   `json:"input_id"`
	Workspace           string   `json:"workspace"`
	Actor               string   `json:"actor"`
	AuthorizedDatabases []string `json:"authorized_databases"`
	CandidateText       string   `json:"candidate_text"`
	Source              Source   `json:"source"`
}

type Receipt struct {
	Version             string    `json:"version"`
	InputID             string    `json:"input_id"`
	Workspace           string    `json:"workspace"`
	Status              Status    `json:"status"`
	InputSHA256         string    `json:"input_sha256"`
	ContentSHA256       string    `json:"content_sha256"`
	ScopeSHA256         string    `json:"scope_sha256"`
	ContentBytes        int       `json:"content_bytes"`
	AuthorizedDatabases []string  `json:"authorized_databases"`
	CapturedAt          time.Time `json:"captured_at"`
	Replayed            bool      `json:"replayed"`
}

type Pending struct {
	Input   Input   `json:"input"`
	Receipt Receipt `json:"receipt"`
}

func (receipt Receipt) String() string {
	encoded, _ := json.Marshal(receipt)
	return string(encoded)
}

type Error struct {
	Code    result.Code
	Message string
}

func (err *Error) Error() string      { return "host input: " + err.Message }
func (err *Error) StableCode() string { return string(err.Code) }

func inputError(code result.Code, format string, arguments ...any) error {
	return &Error{Code: code, Message: fmt.Sprintf(format, arguments...)}
}
