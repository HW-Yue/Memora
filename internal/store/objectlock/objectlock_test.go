package objectlock_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/store/objectlock"
)

func TestGuardTryAcquireConflictsAtomicallyAndReleasesAtTerminal(t *testing.T) {
	manager := objectlock.New()
	first, err := manager.Begin(1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Begin(2)
	if err != nil {
		t.Fatal(err)
	}
	rowKey, err := objectlock.RowKey("db_work", "tbl_notes", "row_one")
	if err != nil {
		t.Fatal(err)
	}
	if err := first.TryAcquire(context.Background(), rowKey); err != nil {
		t.Fatalf("first TryAcquire() error = %v", err)
	}
	err = second.TryAcquire(context.Background(), rowKey)
	if !errors.Is(err, objectlock.ErrConflict) {
		t.Fatalf("second TryAcquire() error = %v", err)
	}
	var stable interface{ StableCode() string }
	if !errors.As(err, &stable) || stable.StableCode() != string(result.CodeWriteConflict) {
		t.Fatalf("conflict stable code = %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	if err := second.TryAcquire(context.Background(), rowKey); err != nil {
		t.Fatalf("retry TryAcquire() error = %v", err)
	}
}

func TestBatchConflictAddsNoNewLocksAndKeepsEarlierGuardLocks(t *testing.T) {
	manager := objectlock.New()
	first := begin(t, manager, 1)
	second := begin(t, manager, 2)
	third := begin(t, manager, 3)
	blockedRow := rowKey(t, "row_blocked")
	blockedSecond := rowKey(t, "row_blocked_second")
	free := rowKey(t, "row_free")
	prior := rowKey(t, "row_prior")
	if err := first.TryAcquire(context.Background(), blockedSecond, blockedRow); err != nil {
		t.Fatal(err)
	}
	if err := second.TryAcquire(context.Background(), prior); err != nil {
		t.Fatal(err)
	}
	err := second.TryAcquire(context.Background(), free, blockedSecond, blockedRow)
	var conflict *objectlock.ConflictError
	if !errors.As(err, &conflict) || conflict.Blocked != blockedRow {
		t.Fatalf("batch conflict = %#v, %v; want blocked Row", conflict, err)
	}
	if err := third.TryAcquire(context.Background(), free); err != nil {
		t.Fatalf("failed batch leaked the free Row lock: %v", err)
	}
	if err := third.TryAcquire(context.Background(), prior); !errors.Is(err, objectlock.ErrConflict) {
		t.Fatalf("failed batch dropped earlier guard lock: %v", err)
	}
}

func TestGuardReentryDeduplicationCloseAndOwnerReuse(t *testing.T) {
	manager := objectlock.New()
	guard := begin(t, manager, 41)
	key := rowKey(t, "row_one")
	if err := guard.TryAcquire(context.Background(), key, key); err != nil {
		t.Fatal(err)
	}
	if err := guard.TryAcquire(context.Background(), key); err != nil {
		t.Fatalf("reentrant TryAcquire() error = %v", err)
	}
	if _, err := manager.Begin(41); !errors.Is(err, objectlock.ErrInvalid) {
		t.Fatalf("duplicate owner Begin() error = %v", err)
	}
	if err := guard.Release(); err != nil {
		t.Fatal(err)
	}
	if err := guard.Release(); err != nil {
		t.Fatalf("idempotent Release() error = %v", err)
	}
	if err := guard.TryAcquire(context.Background(), key); !errors.Is(err, objectlock.ErrInvalid) {
		t.Fatalf("closed guard TryAcquire() error = %v", err)
	}
	reused := begin(t, manager, 41)
	if err := reused.TryAcquire(context.Background(), key); err != nil {
		t.Fatalf("reused owner TryAcquire() error = %v", err)
	}
}

// TestIndependentObjectsDoNotConflict pins that the same object ID under a
// different Database or Table is a different lock: the key is all three
// components, not just the last one.
func TestIndependentObjectsDoNotConflict(t *testing.T) {
	manager := objectlock.New()
	first := begin(t, manager, 1)
	second := begin(t, manager, 2)
	third := begin(t, manager, 3)
	sameID, _ := objectlock.RowKey("db_work", "tbl_notes", "same_id")
	otherTable, _ := objectlock.RowKey("db_work", "tbl_tasks", "same_id")
	otherDatabase, _ := objectlock.RowKey("db_other", "tbl_notes", "same_id")
	for _, request := range []struct {
		guard *objectlock.Guard
		key   objectlock.Key
	}{{first, sameID}, {second, otherTable}, {third, otherDatabase}} {
		if err := request.guard.TryAcquire(context.Background(), request.key); err != nil {
			t.Fatalf("independent TryAcquire(%s) error = %v", request.key, err)
		}
	}
}

func TestCancelledAndInvalidRequestsAcquireNothing(t *testing.T) {
	manager := objectlock.New()
	guard := begin(t, manager, 1)
	contender := begin(t, manager, 2)
	key := rowKey(t, "row_cancelled")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := guard.TryAcquire(ctx, key); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled TryAcquire() error = %v", err)
	}
	if err := contender.TryAcquire(context.Background(), key); err != nil {
		t.Fatalf("cancelled request leaked lock: %v", err)
	}
	if err := guard.TryAcquire(context.Background()); !errors.Is(err, objectlock.ErrInvalid) {
		t.Fatalf("empty TryAcquire() error = %v", err)
	}
	if _, err := objectlock.RowKey(" db", "table", "row"); !errors.Is(err, objectlock.ErrInvalid) {
		t.Fatalf("whitespace RowKey() error = %v", err)
	}
	if _, err := objectlock.RowKey("db", "table", string([]byte{0xff})); !errors.Is(err, objectlock.ErrInvalid) {
		t.Fatalf("invalid UTF-8 RowKey() error = %v", err)
	}
	if _, err := objectlock.RowKey("db", "table", strings.Repeat("x", 2049)); !errors.Is(err, objectlock.ErrInvalid) {
		t.Fatalf("oversized RowKey() error = %v", err)
	}
	batch := make([]objectlock.Key, 1001)
	for index := range batch {
		batch[index] = key
	}
	if err := guard.TryAcquire(context.Background(), batch...); !errors.Is(err, objectlock.ErrInvalid) {
		t.Fatalf("oversized batch error = %v", err)
	}
}

func TestOppositeBatchOrderHasOneWinnerWithoutDeadlock(t *testing.T) {
	for iteration := 0; iteration < 100; iteration++ {
		manager := objectlock.New()
		first := begin(t, manager, 1)
		second := begin(t, manager, 2)
		left := rowKey(t, fmt.Sprintf("row_left_%d", iteration))
		right := rowKey(t, fmt.Sprintf("row_right_%d", iteration))
		start := make(chan struct{})
		results := make(chan struct {
			guard *objectlock.Guard
			err   error
		}, 2)
		var wait sync.WaitGroup
		for _, request := range []struct {
			guard *objectlock.Guard
			keys  []objectlock.Key
		}{{first, []objectlock.Key{left, right}}, {second, []objectlock.Key{right, left}}} {
			wait.Add(1)
			go func(request struct {
				guard *objectlock.Guard
				keys  []objectlock.Key
			}) {
				defer wait.Done()
				<-start
				results <- struct {
					guard *objectlock.Guard
					err   error
				}{request.guard, request.guard.TryAcquire(context.Background(), request.keys...)}
			}(request)
		}
		close(start)
		wait.Wait()
		close(results)
		var winner, loser *objectlock.Guard
		for result := range results {
			if result.err == nil {
				if winner != nil {
					t.Fatalf("iteration %d had two winners", iteration)
				}
				winner = result.guard
			} else if errors.Is(result.err, objectlock.ErrConflict) {
				loser = result.guard
			} else {
				t.Fatalf("iteration %d error = %v", iteration, result.err)
			}
		}
		if winner == nil || loser == nil {
			t.Fatalf("iteration %d winner/loser = %p/%p", iteration, winner, loser)
		}
		if err := winner.Release(); err != nil {
			t.Fatal(err)
		}
		if err := loser.TryAcquire(context.Background(), left, right); err != nil {
			t.Fatalf("iteration %d loser retry error = %v", iteration, err)
		}
	}
}

func begin(t *testing.T, manager *objectlock.Manager, owner uint64) *objectlock.Guard {
	t.Helper()
	guard, err := manager.Begin(owner)
	if err != nil {
		t.Fatal(err)
	}
	return guard
}

func rowKey(t *testing.T, rowID string) objectlock.Key {
	t.Helper()
	key, err := objectlock.RowKey("db_work", "tbl_notes", rowID)
	if err != nil {
		t.Fatal(err)
	}
	return key
}
