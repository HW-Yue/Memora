package semantichealth

import (
	"context"
	"fmt"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/row"
)

const (
	ReportVersion  = "memora.semantic-health/v2"
	RequestVersion = "memora.maintenance-request/v1"
	ReceiptVersion = "memora.maintenance-receipt/v1"
)

type Kind string

const (
	KindDuplicateRow           Kind = "duplicate_row"
	KindSynonymousColumns      Kind = "synonymous_columns"
	KindStaleDescription       Kind = "stale_description"
	KindRouteCapacity          Kind = "route_capacity"
	KindMultiRowLeaf           Kind = "multi_row_leaf"
	KindAmbiguousSiblings      Kind = "ambiguous_siblings"
	KindInvalidRouteStructure  Kind = "invalid_route_structure"
	KindUnroutedRow            Kind = "unrouted_row"
	KindOrphanMembership       Kind = "orphan_membership"
	KindStaleMembership        Kind = "stale_membership"
	KindInvalidMembershipScope Kind = "invalid_membership_scope"
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
	ListRouterNodes(context.Context) ([]router.Node, error)
	ListRouterLeafPage(context.Context, string, string, int) ([]router.Locator, router.ReadPage, error)
}

// FanoutSource lets a backend report the Database's structural Route branch
// fan-out limit, so route_capacity is reported against this Database's own
// limit instead of a hard-coded number.
type FanoutSource interface {
	CurrentBranchFanout(context.Context) (int, error)
}

func branchFanout(ctx context.Context, source Source) (int, error) {
	limits, ok := source.(FanoutSource)
	if !ok {
		return router.DefaultBranchFanout, nil
	}
	fanout, err := limits.CurrentBranchFanout(ctx)
	if err != nil {
		return 0, healthError(result.CodeInternal, "read Route branch fan-out: %v", err)
	}
	return fanout, nil
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
