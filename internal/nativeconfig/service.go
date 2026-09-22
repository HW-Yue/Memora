package nativeconfig

import (
	"strings"
	"time"

	"github.com/HW-Yue/Memora/internal/result"
)

const (
	Version         = "memora.database-configuration/v1"
	QueryBudgetsKey = "query_budgets"
	SnapshotKey     = "memora.configuration.query_budgets"
	recordSchema    = 1
)

// QueryBudgets are the read-side ceilings a caller can move. `route_children`
// is deliberately absent: it was the SHOW ROUTES page size, and route listings
// stopped paginating — a layer is bounded by `route_policy.branch_fanout`, which
// is governed in that one place and not duplicated here.
type QueryBudgets struct {
	OpenLocators    int `json:"open_locators"`
	SelectScan      int `json:"select_scan"`
	SelectRows      int `json:"select_rows"`
	RouteFrameNodes int `json:"route_frame_nodes"`
	// RetiredRouteChildren is not a budget and is never read as one. A revision
	// written while route paging existed still carries the key, and decoding it
	// here is how a host is *told* that the number stopped doing anything instead
	// of watching it disappear; the next write drops it. Nothing may consult it:
	// a second place that bounds a route listing is exactly the duplication that
	// removing the key was meant to end.
	RetiredRouteChildren int `json:"route_children,omitempty"`
}

type Revision struct {
	Version          string       `json:"version"`
	Key              string       `json:"key"`
	Revision         uint64       `json:"revision"`
	Budgets          QueryBudgets `json:"budgets"`
	Actor            string       `json:"actor"`
	Reason           string       `json:"reason"`
	RestoredRevision uint64       `json:"restored_revision,omitempty"`
	RecordedAt       time.Time    `json:"recorded_at"`
}

type Error struct {
	Code    result.Code
	Message string
}

func (err *Error) Error() string      { return err.Message }
func (err *Error) StableCode() string { return string(err.Code) }

func Defaults() QueryBudgets {
	return QueryBudgets{
		OpenLocators: 1, SelectScan: 1000, SelectRows: 10, RouteFrameNodes: 12,
	}
}

func ValidateMutation(expected uint64, actor, reason string) error {
	if expected == 0 {
		return configError(result.CodeValidation, "configuration mutation requires expected revision")
	}
	if strings.TrimSpace(actor) == "" || strings.TrimSpace(reason) == "" {
		return configError(result.CodeValidation, "configuration mutation requires actor and reason")
	}
	return nil
}

func ValidateBudgets(value QueryBudgets) error {
	if value.OpenLocators < 1 || value.OpenLocators > 100 {
		return configError(result.CodeConstraint, "open_locators must be between 1 and 100")
	}
	if value.SelectScan < 1 || value.SelectScan > 1000 {
		return configError(result.CodeConstraint, "select_scan must be between 1 and 1000")
	}
	if value.SelectRows < 1 || value.SelectRows > value.SelectScan {
		return configError(result.CodeConstraint, "select_rows must be between 1 and select_scan")
	}
	if value.RouteFrameNodes < 1 || value.RouteFrameNodes > 100 {
		return configError(result.CodeConstraint, "route_frame_nodes must be between 1 and 100")
	}
	return nil
}

func configError(code result.Code, message string) error {
	return &Error{Code: code, Message: message}
}
