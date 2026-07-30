package skillconflict

import (
	"fmt"

	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/skillwrite"
)

const (
	ViewVersion       = "memora.semantic-conflict/v1"
	ResolutionVersion = "memora.conflict-resolution/v1"
)

type Provenance struct {
	Actor         string `json:"actor"`
	SourceEventID string `json:"source_event_id"`
	Reason        string `json:"reason"`
}

type Proposal struct {
	Values map[string]any `json:"values"`
	Source Provenance     `json:"source"`
}

type RowSnapshot struct {
	RowID    string         `json:"row_id"`
	Revision uint64         `json:"revision"`
	Values   map[string]any `json:"values"`
	Source   Provenance     `json:"source"`
}

type ComparedValue struct {
	Present bool `json:"present"`
	Value   any  `json:"value"`
}

type FieldDifference struct {
	Field    string        `json:"field"`
	Proposed ComparedValue `json:"proposed"`
	Existing ComparedValue `json:"existing"`
}

type RowDifference struct {
	RowID    string            `json:"row_id"`
	Revision uint64            `json:"revision"`
	Fields   []FieldDifference `json:"fields"`
}

type Request struct {
	ID                  string
	Database            string
	Table               string
	AuthorizedDatabases []string
	Proposal            Proposal
	Existing            []RowSnapshot
}

type View struct {
	Version             string          `json:"version"`
	ID                  string          `json:"id"`
	Database            string          `json:"database"`
	Table               string          `json:"table"`
	AuthorizedDatabases []string        `json:"authorized_databases"`
	Proposal            Proposal        `json:"proposal"`
	Existing            []RowSnapshot   `json:"existing"`
	Differences         []RowDifference `json:"differences"`
}

type Action string

const (
	ActionRetain  Action = "RETAIN"
	ActionRewrite Action = "REWRITE"
	ActionRemove  Action = "REMOVE"
)

type Resolution struct {
	Version             string          `json:"version"`
	ConflictID          string          `json:"conflict_id"`
	Action              Action          `json:"action"`
	Instruction         string          `json:"instruction"`
	Actor               string          `json:"actor"`
	SourceEventID       string          `json:"source_event_id"`
	AuthorizedDatabases []string        `json:"authorized_databases"`
	KeepRowID           string          `json:"keep_row_id"`
	RemoveRowIDs        []string        `json:"remove_row_ids"`
	Plan                skillwrite.Plan `json:"plan"`
}

type Error struct {
	Code    result.Code
	Message string
}

func (err *Error) Error() string      { return "Skill conflict: " + err.Message }
func (err *Error) StableCode() string { return string(err.Code) }

func conflictError(code result.Code, format string, arguments ...any) error {
	return &Error{Code: code, Message: fmt.Sprintf(format, arguments...)}
}
