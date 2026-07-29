package executor

import (
	"context"
	"errors"

	"github.com/HW-Yue/Memora/internal/result"
)

func normalizeError(err error) error {
	if err == nil {
		return nil
	}
	var stable interface{ StableCode() string }
	if errors.As(err, &stable) {
		return err
	}
	switch {
	case errors.Is(err, context.Canceled):
		return executeError(result.CodeCancelled, "query was cancelled")
	case errors.Is(err, context.DeadlineExceeded):
		return executeError(result.CodeDeadlineExceeded, "query exceeded its deadline")
	default:
		return executeError(result.CodeInternal, "query failed")
	}
}
