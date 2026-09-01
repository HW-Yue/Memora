package objectindex

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/store/treecommit"
)

// TestStagingOneTreeTwiceInOneGroupIsRefusedNotHung is the structural guard.
//
// Several object families share this Tree — Routes, Relations and the Catalog —
// so reaching for one stage call per family is the natural mistake. It used to
// hang: the first call hands its write lock to the group, and the second waits
// for a lock the group cannot release until the collector it is still inside
// returns. Nothing timed out, nothing logged; the process simply stopped.
//
// The claim is taken before the lock, so the mistake is now an error at the
// offending call site. This test would not fail without it — it would never
// finish, which is why it exists.
func TestStagingOneTreeTwiceInOneGroupIsRefusedNotHung(t *testing.T) {
	_, _, _, index := newTestIndex(t)
	if _, err := index.Bootstrap(1, nil); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		done <- treecommit.CommitGroupFunc(2, func(group *treecommit.Group) error {
			if err := index.StageApply(group, []Update{{
				Record: Record{Kind: kindRoute, ID: "route_a", Revision: 1, Body: []byte("a")},
			}}); err != nil {
				return err
			}
			// The second family. This is the mistake.
			return index.StageApply(group, []Update{{
				Record: Record{Kind: kindRelation, ID: "rel_a", Revision: 1, Body: []byte("b")},
			}})
		})
	}()

	select {
	case err := <-done:
		if !errors.Is(err, treecommit.ErrTreeAlreadyStaged) {
			t.Fatalf("second stage error = %v, want ErrTreeAlreadyStaged", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("staging one Tree twice in one group hung")
	}

	// And the Tree is untouched: a refused group commits nothing.
	if _, err := index.Lookup(kindRoute, "route_a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a refused group left a record behind: %v", err)
	}
}

// TestBatchingTheFamiliesIntoOneStageWorks is the other half: the fix for the
// mistake above is to batch, and batching must be exactly as atomic.
func TestBatchingTheFamiliesIntoOneStageWorks(t *testing.T) {
	_, _, _, index := newTestIndex(t)
	if _, err := index.Bootstrap(1, nil); err != nil {
		t.Fatal(err)
	}
	if err := treecommit.CommitGroupFunc(2, func(group *treecommit.Group) error {
		return index.StageApply(group, []Update{
			{Record: Record{Kind: kindRoute, ID: "route_a", Revision: 1, Body: []byte("a")}},
			{Record: Record{Kind: kindRelation, ID: "rel_a", Revision: 1, Body: []byte("b")}},
		})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := index.Lookup(kindRoute, "route_a"); err != nil {
		t.Fatal(err)
	}
	if _, err := index.Lookup(kindRelation, "rel_a"); err != nil {
		t.Fatal(err)
	}
}

// TestTwoGroupsStagingTheSameTreeStillSerialise pins what the claim must NOT
// break. Two goroutines staging the same Tree into different groups is ordinary
// contention: the second blocks on the Index lock and proceeds once the first
// group commits. Claims are per group, so this stays exactly as it was.
func TestTwoGroupsStagingTheSameTreeStillSerialise(t *testing.T) {
	_, _, _, index := newTestIndex(t)
	if _, err := index.Bootstrap(1, nil); err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	errs := make([]error, 2)
	for worker := range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errs[worker] = treecommit.CommitGroupFunc(uint64(worker+2),
				func(group *treecommit.Group) error {
					return index.StageApply(group, []Update{{Record: Record{
						Kind: kindRoute, ID: []string{"route_a", "route_b"}[worker],
						Revision: 1, Body: []byte("body"),
					}}})
				})
		}()
	}
	wait.Wait()
	for worker, err := range errs {
		if err != nil {
			t.Fatalf("worker %d = %v", worker, err)
		}
	}
	for _, id := range []string{"route_a", "route_b"} {
		if _, err := index.Lookup(kindRoute, id); err != nil {
			t.Fatalf("%s after concurrent groups: %v", id, err)
		}
	}
}
