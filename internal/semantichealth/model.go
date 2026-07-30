package semantichealth

import (
	"context"
	"fmt"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/reindex"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/row"
)

const (
	ReportVersion  = "memora.semantic-health/v1"
	RequestVersion = "memora.maintenance-request/v1"
	ReceiptVersion = "memora.maintenance-receipt/v1"
)

type Kind string

const (
	KindDuplicateRow      Kind = "duplicate_row"
	KindSynonymousColumns Kind = "synonymous_columns"
	KindRouterChildren    Kind = "router_children_capacity"
	KindRouterLeaf        Kind = "router_leaf_capacity"
	KindPendingReindex    Kind = "pending_reindex"
	KindStaleDescription  Kind = "stale_description"
)

type Severity string

const (
	SeverityReviewRequired Severity = "review_required"
	SeverityLowRisk        Severity = "low_risk"
)

type Issue struct {
	ID         string   `json:"id"`
	Kind       Kind     `json:"kind"`
	Severity   Severity `json:"severity"`
	AutoFix    bool     `json:"auto_fix"`
	Action     string   `json:"action"`
	DatabaseID string   `json:"database_id,omitempty"`
	Database   string   `json:"database,omitempty"`
	TableID    string   `json:"table_id,omitempty"`
	Table      string   `json:"table,omitempty"`
	RowID      string   `json:"row_id,omitempty"`
	ObjectIDs  []string `json:"object_ids"`
	Count      int      `json:"count"`
}

type Report struct {
	Version      string  `json:"version"`
	Status       string  `json:"status"`
	Hash         string  `json:"hash"`
	Issues       []Issue `json:"issues"`
	IssueCount   int     `json:"issue_count"`
	AutoFixCount int     `json:"auto_fix_count"`
	Truncated    bool    `json:"truncated"`
}

type Trigger string

const (
	TriggerCheckpoint  Trigger = "checkpoint"
	TriggerUserRequest Trigger = "user_request"
)

type Request struct {
	Version            string   `json:"version"`
	RequestID          string   `json:"request_id"`
	Trigger            Trigger  `json:"trigger"`
	Actor              string   `json:"actor"`
	ExpectedReportHash string   `json:"expected_report_hash"`
	IssueIDs           []string `json:"issue_ids"`
}

type Action struct {
	IssueID string `json:"issue_id"`
	Action  string `json:"action"`
	Target  string `json:"target"`
	Status  string `json:"status"`
}

type Receipt struct {
	Version    string   `json:"version"`
	RequestID  string   `json:"request_id"`
	Status     string   `json:"status"`
	ReportHash string   `json:"report_hash"`
	Replayed   bool     `json:"replayed"`
	Actions    []Action `json:"actions"`
}

type Source interface {
	ShowDatabases(context.Context) ([]catalog.Database, error)
	ListPage(context.Context, string, string, int) ([]row.Row, bool, error)
	ResolveRouterPath(context.Context, string, string) (router.Node, error)
	ListRouterChildren(context.Context, string, string, int) ([]router.Node, string, error)
	ListRouterLeaf(context.Context, string, int) ([]router.Locator, bool, error)
	ListReindexTasks(context.Context) ([]reindex.Task, error)
	RetryReindex(context.Context, string, string, string) error
}

type Error struct {
	Code    result.Code
	Message string
}

func (err *Error) Error() string      { return "semantic health: " + err.Message }
func (err *Error) StableCode() string { return string(err.Code) }

func healthError(code result.Code, format string, arguments ...any) error {
	return &Error{Code: code, Message: fmt.Sprintf(format, arguments...)}
}
