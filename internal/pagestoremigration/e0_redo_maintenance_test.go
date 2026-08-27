package pagestoremigration

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/row"
)

// TestRedoLogRollsCheckpointsAndReclaims is E0 stage 4's gate, and the fix for
// known-risk 7a.
//
// The log had no size limit and never rolled, and Roll / PublishCheckpoint /
// Reclaim had zero production callers — so a database accumulated one
// ever-growing Segment, and every restart replayed it from the very beginning.
//
// The property that matters is not "a checkpoint happened" but "the log stops
// growing with the write count", so the test measures bytes on disk across a
// long run rather than counting Segments.
//
// The threshold is lowered so a handful of writes crosses it: what is under
// test is the mechanism, not the constant.
func TestRedoLogRollsCheckpointsAndReclaims(t *testing.T) {
	restore := walSegmentRollBytes
	walSegmentRollBytes = 64 << 10
	t.Cleanup(func() { walSegmentRollBytes = restore })

	ctx := context.Background()
	directory, file, authority := newAuthorityFixture(t)
	_, rows, table, _ := authorityValuesWithoutRow(t, ctx, file, authority)
	logDirectory := filepath.Join(directory, GenerationDirectory, sharedWALDirectory)

	written := make(map[string]struct{})
	insert := func(index int) {
		t.Helper()
		inserted, err := rows.Insert(ctx, "work", "notes", map[string]any{
			"title": fmt.Sprintf("maintenance subject %02d", index),
		}, row.WriteOptions{ExpectedSchemaVersion: table.SchemaVersion})
		if err != nil {
			t.Fatalf("insert %d: %v", index, err)
		}
		written[inserted.ID] = struct{}{}
	}

	for index := 0; index < 8; index++ {
		insert(index)
	}
	earlyBytes := directoryBytes(t, logDirectory)
	earlyCheckpoint, exists, err := authority.generation.log.LatestCheckpoint()
	if err != nil || !exists {
		t.Fatalf("LatestCheckpoint() after 8 writes = %v, %v; want a published checkpoint", exists, err)
	}

	for index := 8; index < 40; index++ {
		insert(index)
	}
	if authority.redoMaintenanceErr != nil {
		t.Fatalf("redo log maintenance failed: %v", authority.redoMaintenanceErr)
	}

	// Four times the writes must not mean four times the log. Before the
	// wiring this grew without bound, which is the whole of known-risk 7a.
	lateBytes := directoryBytes(t, logDirectory)
	if lateBytes > earlyBytes*2 {
		t.Fatalf("redo log grew from %d to %d bytes over 32 more writes", earlyBytes, lateBytes)
	}

	lateCheckpoint, exists, err := authority.generation.log.LatestCheckpoint()
	if err != nil || !exists {
		t.Fatalf("LatestCheckpoint() after 40 writes = %v, %v", exists, err)
	}
	// A checkpoint that never advances frees nothing; the recovery start has to
	// move forward or restart time still grows with the database's age.
	if lateCheckpoint.RecoveryLSN <= earlyCheckpoint.RecoveryLSN {
		t.Fatalf(
			"checkpoint RecoveryLSN did not advance: %d then %d",
			earlyCheckpoint.RecoveryLSN, lateCheckpoint.RecoveryLSN,
		)
	}
	segments, err := authority.generation.log.State()
	if err != nil {
		t.Fatal(err)
	}
	if segments[0].ID == 1 {
		t.Fatal("first retained Segment is still 1, nothing was reclaimed")
	}

	// Deleting log Segments is irreversible, so the hard gate is that a reopen
	// after reclaim still returns every Row.
	if err := authority.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenAuthority(ctx, file, directory)
	if err != nil {
		t.Fatalf("reopen after reclaim: %v", err)
	}
	defer reopened.Close()
	for rowID := range written {
		locator, err := reopened.generation.CurrentRowsFor(table.ID).Lookup(rowID)
		if err != nil {
			t.Fatalf("reopened Lookup(%s): %v", rowID, err)
		}
		if locator.State != row.StateLive {
			t.Fatalf("reopened %s state = %v, want live", rowID, locator.State)
		}
	}
	postings, err := reopened.generation.fulltext.Postings("subject")
	if err != nil {
		t.Fatal(err)
	}
	if len(postings) != len(written) {
		t.Fatalf("postings after reopen = %d, want %d", len(postings), len(written))
	}
}

func directoryBytes(t *testing.T, directory string) int64 {
	t.Helper()
	var total int64
	err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return total
}

// TestRedoMaintenanceTreatsNoWorkAsSuccess pins the error policy, which is the
// easiest part of this to get wrong.
//
// "Nothing to roll", "no new commit since the last checkpoint" and "nothing
// reclaimable" all mean the log is already in the state maintenance wanted.
// Reporting them as failures would light up a healthy database, and — because
// maintenance runs after a committed write — would attach an alarming error to
// a write that succeeded.
func TestRedoMaintenanceTreatsNoWorkAsSuccess(t *testing.T) {
	restore := walSegmentRollBytes
	// Zero makes every round due, so the round runs and has to decide what to
	// do with a log that has nothing to give it.
	walSegmentRollBytes = 0
	t.Cleanup(func() { walSegmentRollBytes = restore })

	ctx := context.Background()
	_, file, authority := newAuthorityFixture(t)
	_, rows, table, _ := authorityValuesWithoutRow(t, ctx, file, authority)

	// A freshly rolled log has no committed transaction in its active Segment,
	// so a second round back to back finds nothing to roll.
	for round := 0; round < 3; round++ {
		if err := authority.generation.maintainRedoLog(); err != nil {
			t.Fatalf("round %d on an idle log = %v, want no error", round, err)
		}
	}
	if _, err := rows.Insert(ctx, "work", "notes", map[string]any{"title": "after idle rounds"}, row.WriteOptions{
		ExpectedSchemaVersion: table.SchemaVersion,
	}); err != nil {
		t.Fatalf("insert after idle maintenance rounds: %v", err)
	}
	if authority.RedoMaintenanceError() != nil {
		t.Fatalf("RedoMaintenanceError() = %v, want nil", authority.RedoMaintenanceError())
	}
}
