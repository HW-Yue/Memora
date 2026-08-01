// Package readquery owns the statement allowlist shared by every read-only
// MSQL transport. It deliberately inspects recovered batch items so an early
// parse error cannot hide a later mutation.
package readquery

import (
	"errors"
	"fmt"

	"github.com/HW-Yue/Memora/internal/msql/parser"
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
		if !Allowed(item.Kind) {
			return 0, fmt.Errorf("%w: statement %d has kind %s", ErrNotReadOnly, item.Index, item.Kind)
		}
	}
	return len(items), nil
}

func Allowed(kind string) bool {
	switch kind {
	case "SHOW", "DESCRIBE", "DESCRIBE_ROUTE", "SELECT", "OPEN_ROUTE":
		return true
	default:
		return false
	}
}
