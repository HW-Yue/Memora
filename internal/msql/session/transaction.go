package session

import (
	"errors"
	"fmt"
	"sync"

	"github.com/HW-Yue/Memora/internal/msql/ast"
)

var ErrInvalidTransactionState = errors.New("invalid MSQL transaction state")

type TransactionState struct {
	mu     sync.Mutex
	active bool
}

func (state *TransactionState) Active() bool {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.active
}

func (state *TransactionState) Apply(batch ast.Batch) error {
	state.mu.Lock()
	defer state.mu.Unlock()

	for index, statement := range batch.Statements {
		switch statement.Kind {
		case "BEGIN":
			if state.active {
				return &TransactionError{StatementIndex: index, Action: "BEGIN", Active: true}
			}
			state.active = true
		case "COMMIT", "ROLLBACK":
			if !state.active {
				return &TransactionError{StatementIndex: index, Action: statement.Kind, Active: false}
			}
			state.active = false
		}
	}
	return nil
}

type TransactionError struct {
	StatementIndex int
	Action         string
	Active         bool
}

func (err *TransactionError) Error() string {
	state := "idle"
	if err.Active {
		state = "active"
	}
	return fmt.Sprintf("%v: cannot %s while transaction is %s at statement %d", ErrInvalidTransactionState, err.Action, state, err.StatementIndex)
}

func (err *TransactionError) Is(target error) bool {
	return target == ErrInvalidTransactionState
}
