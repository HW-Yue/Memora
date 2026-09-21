// Package readquery owns the statement allowlist shared by every read-only
// MSQL transport. It deliberately inspects recovered batch items so an early
// parse error cannot hide a later mutation.
package readquery

import (
	"errors"
	"fmt"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/msql/parser"
	"github.com/HW-Yue/Memora/internal/security"
)

var ErrNotReadOnly = errors.New("MSQL batch is not read-only")

// Validate returns the number of statement inputs expected by the executor.
// Request-level lexer/empty-batch failures return zero without a policy error;
// the executor remains responsible for returning its canonical parse envelope.
func Validate(source string) (int, error) {
	items, err := parser.ParseBatchItems(source)
	if err != nil {
		return 0, nil
	}
	for _, item := range items {
		if !Allowed(item) {
			return 0, fmt.Errorf("%w: statement %d has kind %s", ErrNotReadOnly, item.Index, item.Kind)
		}
	}
	return len(items), nil
}

// Allowed asks the one classification rather than keeping a list of its own: a
// statement that reads is exactly one the engine would not write with, so a new
// read statement is reachable the moment it is classified.
//
// A statement that did not parse has no classification to ask, so the recovered
// kind decides. That keeps a syntax error inside a read surfacing as a parse
// error instead of a policy refusal.
func Allowed(item parser.BatchItem) bool {
	if item.Statement != nil {
		return executor.StatementRiskLevel(*item.Statement) == security.LevelRead
	}
	switch item.Kind {
	case "SHOW", "DESCRIBE", "DESCRIBE_ROUTE", "SELECT", "OPEN_ROUTE",
		"OPEN_ARCHIVE", "RECALL", "PLAN_ROUTE_MUTATION", "PLAN_SCHEMA_CHANGE":
		return true
	default:
		return false
	}
}
