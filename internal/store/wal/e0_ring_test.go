package wal

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestRedoRingRefusesRatherThanGrowingPastItsCapacity is E0 stage 5's gate.
//
// Stage 4 made the log roll, checkpoint and reclaim, which bounds the log
// *while the checkpoint can advance*. It cannot always: a checkpoint needs the
// dirty Pages flushed, and when that fails or is slow the tail pointer stops
// moving. Before this stage nothing noticed — the log simply kept growing, and
// a bound that a policy can quietly fail to enforce is not a bound.
//
// The ring makes the limit structural: the in-use span (write LSN back to the
// checkpoint's recovery LSN) may not exceed the ring, and a write that would
// exceed it is refused. Refusing is the point. Overwriting the span before the
// recovery LSN would discard changes no Page file holds yet, which is silent
// data loss; a loud error is the only other option.
//
// See docs/storage/shared-circular-redo-v1.md §3.
func TestRedoRingRefusesRatherThanGrowingPastItsCapacity(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "redo")
	set, err := CreateSegmentSetWithCapacity(path, 1000, 64<<10)
	if err != nil {
		t.Fatalf("CreateSegmentSetWithCapacity() error = %v", err)
	}
	t.Cleanup(func() { _ = set.Close() })

	// No checkpoint is ever published, so the tail never moves: every byte
	// written stays in use. This is the condition stage 4 cannot bound.
	payload := bytes.Repeat([]byte("ring"), 512)
	refusedAt := 0
	for index := 1; index <= 200; index++ {
		_, err := set.Commit(uint64(index), []Record{{Type: TypePageDelta, Payload: payload}})
		if errors.Is(err, ErrRingFull) {
			refusedAt = index
			break
		}
		if err != nil {
			t.Fatalf("Commit(%d) error = %v", index, err)
		}
		if index%4 == 0 {
			if _, err := set.Roll(); err != nil && !errors.Is(err, ErrEmptySegment) {
				t.Fatalf("Roll() after %d error = %v", index, err)
			}
		}
	}
	if refusedAt == 0 {
		t.Fatal("the log accepted 200 writes with a tail that never moved; it has no capacity")
	}

	// Refusing is not the same as bounding: the bytes on disk have to actually
	// stay inside the ring, allowing the one transaction that was in flight
	// when the limit was reached.
	if bytes := ringDirectoryBytes(t, path); bytes > (64<<10)+(1<<16) {
		t.Fatalf("in-use log is %d bytes for a %d byte ring", bytes, 64<<10)
	}

	// And nothing was lost: every transaction the log accepted is still there,
	// in order, after a reopen.
	if err := set.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	reopened, err := OpenSegmentSetWithCapacity(path, 1000, 64<<10)
	if err != nil {
		t.Fatalf("OpenSegmentSetWithCapacity() error = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	transactions, err := reopened.ScanCommitted()
	if err != nil {
		t.Fatalf("ScanCommitted() error = %v", err)
	}
	if len(transactions) != refusedAt-1 {
		t.Fatalf("reopened log holds %d transactions, want the %d it accepted",
			len(transactions), refusedAt-1)
	}
	for index, transaction := range transactions {
		if transaction.Receipt.TransactionID != uint64(index+1) {
			t.Fatalf("transaction %d = %d", index, transaction.Receipt.TransactionID)
		}
	}
}

// TestRedoRingAcceptsAgainOnceTheCheckpointFreesSpace is the other half: the
// backpressure is a full ring, not a broken one. When the checkpoint advances
// the tail and reclaim frees the Segments behind it, writing resumes.
func TestRedoRingAcceptsAgainOnceTheCheckpointFreesSpace(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "redo")
	set, err := CreateSegmentSetWithCapacity(path, 1000, 64<<10)
	if err != nil {
		t.Fatalf("CreateSegmentSetWithCapacity() error = %v", err)
	}
	t.Cleanup(func() { _ = set.Close() })

	payload := bytes.Repeat([]byte("ring"), 512)
	next := uint64(1)
	fill := func() error {
		for {
			_, err := set.Commit(next, []Record{{Type: TypePageDelta, Payload: payload}})
			if err != nil {
				return err
			}
			next++
			if next%4 == 0 {
				if _, err := set.Roll(); err != nil && !errors.Is(err, ErrEmptySegment) {
					return err
				}
			}
			if next > 500 {
				return fmt.Errorf("the ring never filled")
			}
		}
	}
	if err := fill(); !errors.Is(err, ErrRingFull) {
		t.Fatalf("filling the ring = %v, want ErrRingFull", err)
	}

	// Advance the tail. The barrier reports every Page durable, which is what a
	// real flush of every dirty Page amounts to.
	if _, err := set.Roll(); err != nil && !errors.Is(err, ErrEmptySegment) {
		t.Fatalf("Roll() before checkpoint = %v", err)
	}
	if _, err := set.PublishCheckpoint(everythingDurable{}); err != nil {
		t.Fatalf("PublishCheckpoint() = %v", err)
	}
	if _, err := set.Reclaim(); err != nil && !errors.Is(err, ErrNoReclaimableSegments) {
		t.Fatalf("Reclaim() = %v", err)
	}
	if _, err := set.Commit(next, []Record{{Type: TypePageDelta, Payload: payload}}); err != nil {
		t.Fatalf("Commit after the checkpoint freed space = %v", err)
	}
}

// everythingDurable is a barrier that reports the flush already done. A real
// one flushes the buffer pool; for the ring's purpose what matters is only that
// the recovery LSN is allowed to move.
type everythingDurable struct{}

func (everythingDurable) FlushThrough(uint64) error { return nil }

func ringDirectoryBytes(t *testing.T, directory string) int64 {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read %q: %v", directory, err)
	}
	total := int64(0)
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatalf("stat %q: %v", entry.Name(), err)
		}
		total += info.Size()
	}
	return total
}
