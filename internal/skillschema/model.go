package skillschema

import (
	"context"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
)

const (
	PlanVersion    = "memora.schema-plan/v1"
	ReceiptVersion = "memora.schema-receipt/v1"
)

type Plan struct {
	Version             string         `json:"version"`
	ID                  string         `json:"id"`
	Actor               string         `json:"actor"`
	SourceEventID       string         `json:"source_event_id"`
	Reason              string         `json:"reason"`
	AuthorizedDatabases []string       `json:"authorized_databases"`
	Ensure              *EnsurePlan    `json:"ensure,omitempty"`
	Migration           *MigrationPlan `json:"migration,omitempty"`
}

type EnsurePlan struct {
	Database         catalog.DatabaseDefinition `json:"database"`
	DatabaseSynonyms []string                   `json:"database_synonyms,omitempty"`
	Table            catalog.TableDefinition    `json:"table"`
	TableSynonyms    []string                   `json:"table_synonyms,omitempty"`
}

type MigrationPlan struct {
	Database                string          `json:"database"`
	ExpectedDatabaseVersion uint64          `json:"expected_database_version"`
	MaxAffectedObjects      int             `json:"max_affected_objects"`
	Steps                   []MigrationStep `json:"steps"`
}

type MigrationAction string

const (
	RenameTable  MigrationAction = "RENAME_TABLE"
	RenameColumn MigrationAction = "RENAME_COLUMN"
)

type MigrationStep struct {
	ID              string          `json:"id"`
	Action          MigrationAction `json:"action"`
	Table           string          `json:"table"`
	Column          string          `json:"column,omitempty"`
	NewName         string          `json:"new_name"`
	ExpectedVersion uint64          `json:"expected_version"`
}

type Impact struct {
	AffectedObjects int             `json:"affected_objects"`
	AffectedRows    uint64          `json:"affected_rows"`
	Reversible      bool            `json:"reversible"`
	Rollback        []MigrationStep `json:"rollback"`
	Hash            string          `json:"hash"`
}

type Phase string

const (
	PhaseDiscover  Phase = "discover"
	PhasePreflight Phase = "preflight"
	PhaseApply     Phase = "apply"
	PhaseRollback  Phase = "rollback"
	PhaseVerify    Phase = "verify"
)

type Call struct {
	Phase   Phase                 `json:"phase"`
	ID      string                `json:"id"`
	Request executor.BatchRequest `json:"request"`
}

type Tool interface {
	Invoke(context.Context, Call) (result.Envelope, error)
}

type ToolFunc func(context.Context, Call) (result.Envelope, error)

func (function ToolFunc) Invoke(ctx context.Context, call Call) (result.Envelope, error) {
	return function(ctx, call)
}

type ObjectAction string

const (
	ObjectCreated ObjectAction = "created"
	ObjectReused  ObjectAction = "reused"
)

type ObjectReceipt struct {
	Action ObjectAction `json:"action"`
	ID     string       `json:"id"`
	Name   string       `json:"name"`
}

type ReceiptStatus string

const (
	ReceiptApplied        ReceiptStatus = "applied"
	ReceiptRolledBack     ReceiptStatus = "rolled_back"
	ReceiptRollbackFailed ReceiptStatus = "rollback_failed"
)

type Receipt struct {
	Version   string          `json:"version"`
	PlanID    string          `json:"plan_id"`
	Status    ReceiptStatus   `json:"status"`
	Database  ObjectReceipt   `json:"database,omitempty"`
	Table     ObjectReceipt   `json:"table,omitempty"`
	Impact    *Impact         `json:"impact,omitempty"`
	Applied   []MigrationStep `json:"applied,omitempty"`
	Rollbacks []MigrationStep `json:"rollbacks,omitempty"`
	Verified  bool            `json:"verified"`
}

type Report struct {
	Calls   []Call  `json:"calls"`
	Receipt Receipt `json:"receipt"`
}

type Error struct {
	Code    result.Code
	Message string
}

func (err *Error) Error() string      { return "Skill schema: " + err.Message }
func (err *Error) StableCode() string { return string(err.Code) }
