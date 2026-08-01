package result

import (
	"encoding/json"
	"errors"
	"fmt"
)

const Version = "memora.result/v1"

const ListPageVersion = "memora.list-page/v1"

const RowDetailVersion = "memora.row-detail/v1"

var ErrInvalidEnvelope = errors.New("invalid result envelope")

type Status string

const (
	StatusSucceeded  Status = "succeeded"
	StatusFailed     Status = "failed"
	StatusSkipped    Status = "skipped"
	StatusRolledBack Status = "rolled_back"
)

type Row map[string]any

type Column struct {
	Name         string `json:"name"`
	Type         string `json:"type"`
	Nullable     bool   `json:"nullable"`
	ColumnID     string `json:"column_id,omitempty"`
	Purpose      string `json:"purpose,omitempty"`
	SemanticRole string `json:"semantic_role,omitempty"`
}

type RowDisplay struct {
	TitleColumn   string `json:"title_column,omitempty"`
	SummaryColumn string `json:"summary_column,omitempty"`
	Fallback      string `json:"fallback,omitempty"`
}

type RowDetail struct {
	Version       string     `json:"version"`
	DatabaseID    string     `json:"database_id"`
	DatabaseName  string     `json:"database_name"`
	TableID       string     `json:"table_id"`
	TableName     string     `json:"table_name"`
	SchemaVersion uint64     `json:"schema_version"`
	RowSemantics  string     `json:"row_semantics"`
	Display       RowDisplay `json:"display"`
}

type ListPage struct {
	Version    string `json:"version"`
	Limit      uint64 `json:"limit"`
	Cursor     string `json:"cursor"`
	Snapshot   string `json:"snapshot"`
	Truncated  bool   `json:"truncated"`
	NextCursor string `json:"next_cursor,omitempty"`
}

type StatementResult struct {
	Index          int          `json:"index"`
	Statement      string       `json:"statement"`
	Source         string       `json:"source"`
	Status         Status       `json:"status"`
	Columns        []Column     `json:"columns"`
	Rows           []Row        `json:"rows"`
	AffectedRows   uint64       `json:"affected_rows"`
	Revision       *uint64      `json:"revision,omitempty"`
	CommitSequence *uint64      `json:"commit_sequence,omitempty"`
	Truncated      bool         `json:"truncated"`
	NextCursor     string       `json:"next_cursor,omitempty"`
	Page           *ListPage    `json:"page,omitempty"`
	RowDetail      *RowDetail   `json:"row_detail,omitempty"`
	Warnings       []Notice     `json:"warnings"`
	Error          *ResultError `json:"error,omitempty"`
}

type Envelope struct {
	Version    string            `json:"version"`
	RequestID  string            `json:"request_id"`
	OK         bool              `json:"ok"`
	Results    []StatementResult `json:"results"`
	Truncated  bool              `json:"truncated"`
	NextCursor string            `json:"next_cursor,omitempty"`
	Warnings   []Notice          `json:"warnings"`
	Error      *ResultError      `json:"error,omitempty"`
}

func NewStatement(index int, statement, source string) StatementResult {
	return StatementResult{
		Index: index, Statement: statement, Source: source, Status: StatusSucceeded,
		Columns: []Column{}, Rows: []Row{}, Warnings: []Notice{},
	}
}

func FailedStatement(index int, statement, source string, code Code, message string, retryable bool) StatementResult {
	result := NewStatement(index, statement, source)
	result.Status = StatusFailed
	result.Error = statementError(index, code, message, retryable)
	return result
}

func NewEnvelope(requestID string, results ...StatementResult) Envelope {
	envelope := Envelope{Version: Version, RequestID: requestID, Results: results, Warnings: []Notice{}}
	envelope = envelope.normalized()
	envelope.OK = envelope.computedOK()
	return envelope
}

func FailedRequest(requestID string, code Code, message string, retryable bool) Envelope {
	return Envelope{
		Version: Version, RequestID: requestID, Results: []StatementResult{}, Warnings: []Notice{},
		Error: &ResultError{Code: code, Message: message, Retryable: retryable},
	}
}

func (envelope Envelope) MarshalJSON() ([]byte, error) {
	normalized := envelope.normalized()
	if err := normalized.Validate(); err != nil {
		return nil, err
	}
	type wire Envelope
	return json.Marshal(wire(normalized))
}

func (envelope *Envelope) UnmarshalJSON(data []byte) error {
	type wire Envelope
	var decoded wire
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	normalized := Envelope(decoded).normalized()
	if err := normalized.Validate(); err != nil {
		return err
	}
	*envelope = normalized
	return nil
}

func statementError(index int, code Code, message string, retryable bool) *ResultError {
	return &ResultError{Code: code, Message: message, Retryable: retryable, StatementIndex: &index}
}

func invalid(format string, arguments ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidEnvelope, fmt.Sprintf(format, arguments...))
}
