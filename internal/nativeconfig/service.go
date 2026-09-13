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

type QueryBudgets struct {
	RouteChildren   int `json:"route_children"`
	OpenLocators    int `json:"open_locators"`
	SelectScan      int `json:"select_scan"`
	SelectRows      int `json:"select_rows"`
	RouteFrameNodes int `json:"route_frame_nodes"`
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
		RouteChildren: 12, OpenLocators: 1, SelectScan: 1000,
		SelectRows: 10, RouteFrameNodes: 12,
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
	if value.RouteChildren < 1 || value.RouteChildren > 100 {
		return configError(result.CodeConstraint, "route_children must be between 1 and 100")
	}
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
