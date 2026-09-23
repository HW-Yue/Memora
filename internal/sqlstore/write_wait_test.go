package sqlstore_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/sqlstore"
)

func openInstance(t *testing.T, options sqlstore.Options) *sqlstore.DB {
	t.Helper()
	db, err := sqlstore.Open(filepath.Join(t.TempDir(), "memora.db"), options)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func databaseDefinition(name string) catalog.DatabaseDefinition {
	return catalog.DatabaseDefinition{Name: name, Purpose: "Work memory", Scope: "Projects"}
}

// An explicit transaction holds the write lock from BEGIN to COMMIT on purpose.
// A caller that opens one and never comes back must not hold it forever: the
// transaction is rolled back and the next writer gets through.
func TestAnIdleTransactionRollsItselfBack(t *testing.T) {
	db := openInstance(t, sqlstore.Options{TransactionIdle: 50 * time.Millisecond})

	transaction, err := db.BeginTransaction(context.Background())
	if err != nil {
		t.Fatalf("BeginTransaction() error = %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := db.CreateDatabase(context.Background(), databaseDefinition("work")); err == nil {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("the idle transaction never let go: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	if _, err := transaction.DescribeDatabase(context.Background(), "work"); err == nil {
		t.Fatal("the idle transaction is still usable")
	}
	if err := transaction.Commit(); err == nil {
		t.Fatal("an idle transaction that was rolled back still committed")
	}
}

// Waiting for the write lock is waiting, not hanging: a context that ends ends
// the wait, and the caller is told so with a code it can act on.
func TestWaitingForTheWriteLockEndsWithItsContext(t *testing.T) {
	db := openInstance(t, sqlstore.Options{TransactionIdle: time.Hour})

	transaction, err := db.BeginTransaction(context.Background())
	if err != nil {
		t.Fatalf("BeginTransaction() error = %v", err)
	}
	defer func() { _ = transaction.Rollback() }()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = db.CreateDatabase(ctx, databaseDefinition("work"))
	if err == nil {
		t.Fatal("a second writer got in while a transaction was open")
	}
	if waited := time.Since(start); waited > 5*time.Second {
		t.Fatalf("the wait ignored its context for %v", waited)
	}
	var storeErr *sqlstore.Error
	if !errors.As(err, &storeErr) || storeErr.Code != result.CodeDeadlineExceeded {
		t.Fatalf("CreateDatabase() error = %v, want %s", err, result.CodeDeadlineExceeded)
	}
}
