package sqlstore_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/sqlstore"
)

// An explicit transaction's lifetime belongs to the wrapper that handed it out —
// Commit, Rollback, and the idle timer — not to the context of whichever request
// opened it. database/sql finishes a transaction by itself once its context is
// done, so closing a session (a disconnect, or the daemon shutting down) used to
// end the transaction behind the wrapper's back; the wrapper's own rollback then
// failed with "sql: transaction has already been committed or rolled back" and an
// orderly shutdown came back as an error. `internal/daemon` caught it as
// "daemon exited with sql: transaction has already been committed or rolled back".
func TestAnExplicitTransactionOutlivesItsCallersContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memora.db")
	// The idle timer is off: the context is the only thing under test.
	db, err := sqlstore.Open(path, sqlstore.Options{TransactionIdle: -1, CheckInvariants: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	transaction, err := db.BeginTransaction(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	// database/sql reacts to a cancelled context asynchronously, so give it room
	// to finish the transaction behind the wrapper's back: that is the race.
	time.Sleep(50 * time.Millisecond)

	if err := transaction.Rollback(); err != nil {
		t.Fatalf("the wrapper still owns this transaction: %v", err)
	}
}
