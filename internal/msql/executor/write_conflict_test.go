package executor

import (
	"testing"

	"github.com/HW-Yue/Memora/internal/result"
)

type writeConflictError struct{}

func (writeConflictError) Error() string      { return "logical object is held by another writer" }
func (writeConflictError) StableCode() string { return string(result.CodeWriteConflict) }

func TestWriteConflictProducesRegisteredRetryableStatementError(t *testing.T) {
	statement := statementFailure(3, "UPDATE", "UPDATE work.notes", writeConflictError{})
	if statement.Error == nil || statement.Error.Code != result.CodeWriteConflict ||
		!statement.Error.Retryable || statement.Error.StatementIndex == nil ||
		*statement.Error.StatementIndex != 3 {
		t.Fatalf("write conflict statement = %#v", statement)
	}
}
