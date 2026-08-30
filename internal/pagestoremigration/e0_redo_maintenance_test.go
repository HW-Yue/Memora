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

// TestRedoRingBoundsTheLogUnderContinuousWrites is E0 stage 5's production gate.
//
// Stage 4 bounds the log while the checkpoint can advance. The ring bounds it
// full stop: the in-use span may not exceed the ring, and the relief round runs
// before a publication rather than after it, so an ordinary write never meets
// the cap.
//
// What is measured is bytes on disk across a long run against the configured
// ring, not "a checkpoint happened" — a bound that only holds when a policy
// keeps up is not a bound.
//
// The ring and the roll threshold are both lowered so a handful of writes
// exercises the mechanism; what is under test is the mechanism, not the
// constants.
func TestRedoRingBoundsTheLogUnderContinuousWrites(t *testing.T) {
	restoreRoll, restoreRing := walSegmentRollBytes, walRingBytes
	walSegmentRollBytes, walRingBytes = 32<<10, 256<<10
	t.Cleanup(func() { walSegmentRollBytes, walRingBytes = restoreRoll, restoreRing })

	ctx := context.Background()
	directory, file, authority := newAuthorityFixture(t)
	_, rows, table, _ := authorityValuesWithoutRow(t, ctx, file, authority)
	logDirectory := filepath.Join(directory, GenerationDirectory, sharedWALDirectory)

	titles := make(map[string]struct{})
	for index := 0; index < 60; index++ {
		title := fmt.Sprintf("ring subject %03d", index)
		inserted, err := rows.Insert(ctx, "work", "notes", map[string]any{
			"title": title,
		}, row.WriteOptions{ExpectedSchemaVersion: table.SchemaVersion})
		if err != nil {
			// A refusal here would mean the relief round could not free space.
			// That is the back-pressure working, but under an ordinary write
			// load it means the ring is mis-sized, so it fails the gate.
			t.Fatalf("insert %d: %v", index, err)
		}
		titles[inserted.ID] = struct{}{}
		if bytes := directoryBytes(t, logDirectory); bytes > int64(walRingBytes)*2 {
			t.Fatalf("after %d writes the log is %d bytes for a %d byte ring",
				index+1, bytes, walRingBytes)
		}
	}
	if authority.redoMaintenanceErr != nil {
		t.Fatalf("redo maintenance failed: %v", authority.redoMaintenanceErr)
	}

	// Deleting log space is irreversible, so the gate that matters most is that
	// everything written still reads back after a reopen.
	if err := authority.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenAuthority(ctx, file, directory)
	if err != nil {
		t.Fatalf("reopen after ring maintenance: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	for rowID := range titles {
		locator, err := reopened.generation.CurrentRowsFor(table.ID).Lookup(rowID)
		if err != nil {
			t.Fatalf("Row %q after reopen: %v", rowID, err)
		}
		if locator.State != row.StateLive {
			t.Fatalf("Row %q state after reopen = %v, want live", rowID, locator.State)
		}
	}
	// The revisions too, not only the current Rows: a reclaimed Segment that
	// took an unflushed change would show up here first.
	for rowID := range titles {
		if _, err := reopened.generation.HistoryFor(table.ID).ByRevision(rowID, 1); err != nil {
			t.Fatalf("Row %q revision 1 after reopen: %v", rowID, err)
		}
	}
}
