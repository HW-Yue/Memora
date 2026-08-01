package schemachangeplan

import (
	"context"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
)

const (
	ProposalVersion = "memora.schema-change-proposal/v1"
	PlanVersion     = "memora.schema-change-plan/v1"
	ReceiptVersion  = "memora.schema-change-receipt/v1"
)

type Status string

const (
	StatusReviewRequired Status = "review_required"
	StatusBlocked        Status = "blocked"
)

type Action string

const (
	ActionAdd    Action = "ADD_COLUMN"
	ActionRename Action = "RENAME_COLUMN"
	ActionAlter  Action = "ALTER_COLUMN"
	ActionDrop   Action = "DROP_COLUMN"
)

type ChangeProposal struct {
	ID               string                    `json:"change_id"`
	Action           Action                    `json:"action"`
	ColumnID         string                    `json:"column_id,omitempty"`
	ExpectedRevision uint64                    `json:"expected_revision,omitempty"`
	NewName          string                    `json:"new_name,omitempty"`
	Definition       *catalog.ColumnDefinition `json:"definition,omitempty"`
}

type Proposal struct {
	Version               string           `json:"version"`
	ID                    string           `json:"proposal_id"`
	Actor                 string           `json:"actor"`
	SourceEventID         string           `json:"source_event_id"`
	Reason                string           `json:"reason"`
	ExpectedTableRevision uint64           `json:"expected_table_revision"`
	Changes               []ChangeProposal `json:"changes"`
}

type Scope struct {
	DatabaseID string `json:"database_id"`
	Database   string `json:"database"`
	TableID    string `json:"table_id"`
	Table      string `json:"table"`
}

type ColumnShape struct {
	ColumnID      string   `json:"column_id"`
	Name          string   `json:"name"`
	Aliases       []string `json:"aliases"`
	Type          string   `json:"type"`
	MaxCharacters int      `json:"max_characters,omitempty"`
	Nullable      bool     `json:"nullable"`
	Purpose       string   `json:"purpose"`
	SemanticRole  string   `json:"semantic_role,omitempty"`
	Revision      uint64   `json:"revision"`
}

type PlannedAction struct {
	ChangeID string       `json:"change_id"`
	Action   Action       `json:"action"`
	Before   *ColumnShape `json:"before,omitempty"`
	After    *ColumnShape `json:"after,omitempty"`
}

type RowGuard struct {
	RowID    string `json:"row_id"`
	Revision uint64 `json:"revision"`
}

type Blocker struct {
	Code     string `json:"code"`
	ChangeID string `json:"change_id"`
	RowID    string `json:"row_id,omitempty"`
	Message  string `json:"message"`
}

type Impact struct {
	AffectedColumns        int  `json:"affected_columns"`
	RowsScanned            int  `json:"rows_scanned"`
	IncompatibleRows       int  `json:"incompatible_rows"`
	PopulatedDroppedValues int  `json:"populated_dropped_values"`
	RequiresRowRewrite     bool `json:"requires_row_rewrite"`
	Destructive            bool `json:"destructive"`
	Reversible             bool `json:"reversible"`
}

type Plan struct {
	Version          string          `json:"version"`
	PlanID           string          `json:"plan_id"`
	ProposalID       string          `json:"proposal_id"`
	Status           Status          `json:"status"`
	Scope            Scope           `json:"scope"`
	Actor            string          `json:"actor"`
	SourceEventID    string          `json:"source_event_id"`
	Reason           string          `json:"reason"`
	DatabaseRevision uint64          `json:"database_revision"`
	TableRevision    uint64          `json:"table_revision"`
	BaseSchemaHash   string          `json:"base_schema_hash"`
	RowSnapshotHash  string          `json:"row_snapshot_hash,omitempty"`
	ColumnGuards     []ColumnShape   `json:"column_guards"`
	RowGuards        []RowGuard      `json:"row_guards"`
	Actions          []PlannedAction `json:"actions"`
	Impact           Impact          `json:"impact"`
	Blockers         []Blocker       `json:"blockers"`
	Hash             string          `json:"hash"`
}

type Receipt struct {
	Version              string    `json:"version"`
	PlanID               string    `json:"plan_id"`
	PlanHash             string    `json:"plan_hash"`
	Status               string    `json:"status"`
	ChangeSequence       uint64    `json:"change_sequence"`
	DatabaseRevision     uint64    `json:"database_revision"`
	TableRevision        uint64    `json:"table_revision"`
	AppliedActions       int       `json:"applied_actions"`
	Verified             bool      `json:"verified"`
	VerificationCode     string    `json:"verification_code,omitempty"`
	CompensationProposal *Proposal `json:"compensation_proposal,omitempty"`
}

type RowSource interface {
	ListPage(context.Context, string, string, int) ([]row.Row, bool, error)
}

type Error struct {
	Code    result.Code
	Message string
}

func (err *Error) Error() string      { return "Schema change plan: " + err.Message }
func (err *Error) StableCode() string { return string(err.Code) }
