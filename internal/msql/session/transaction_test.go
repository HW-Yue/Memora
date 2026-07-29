package session

import (
	"errors"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/parser"
)

func TestTransactionStatePersistsAcrossRequests(t *testing.T) {
	t.Parallel()

	var state TransactionState
	applySource(t, &state, "BEGIN; SELECT * FROM notes")
	if !state.Active() {
		t.Fatal("transaction is inactive after BEGIN")
	}
	applySource(t, &state, "UPDATE notes SET title = :title")
	if !state.Active() {
		t.Fatal("transaction did not persist across request")
	}
	applySource(t, &state, "COMMIT")
	if state.Active() {
		t.Fatal("transaction is active after COMMIT")
	}
}

func TestTransactionStateRejectsInvalidBoundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		source     string
		wantIndex  int
		wantActive bool
	}{
		{"nested begin", "BEGIN; START TRANSACTION", 1, true},
		{"commit without transaction", "COMMIT", 0, false},
		{"rollback without transaction", "ROLLBACK", 0, false},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var state TransactionState
			batch, err := parser.ParseBatch(test.source)
			if err != nil {
				t.Fatalf("ParseBatch() error = %v", err)
			}
			err = state.Apply(*batch)
			var transactionError *TransactionError
			if !errors.Is(err, ErrInvalidTransactionState) || !errors.As(err, &transactionError) {
				t.Fatalf("Apply() error = %v", err)
			}
			if transactionError.StatementIndex != test.wantIndex || state.Active() != test.wantActive {
				t.Fatalf("Apply() error = %#v, active = %v", transactionError, state.Active())
			}
		})
	}
}

func TestRollbackClosesActiveTransaction(t *testing.T) {
	t.Parallel()

	var state TransactionState
	applySource(t, &state, "BEGIN")
	applySource(t, &state, "ROLLBACK")
	if state.Active() {
		t.Fatal("transaction is active after ROLLBACK")
	}
}

func applySource(t *testing.T, state *TransactionState, source string) {
	t.Helper()
	batch, err := parser.ParseBatch(source)
	if err != nil {
		t.Fatalf("ParseBatch(%q) error = %v", source, err)
	}
	if err := state.Apply(*batch); err != nil {
		t.Fatalf("Apply(%q) error = %v", source, err)
	}
}
