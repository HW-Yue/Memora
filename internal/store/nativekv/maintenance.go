package nativekv

import (
	"errors"
	"fmt"
	"math"

	"github.com/HW-Yue/Memora/internal/store/wal"
)

// walSegmentRollBytes is how large this Store's active redo Segment may grow
// before a maintenance round rolls, checkpoints and reclaims it.
//
// Small on purpose, for the same reason the generation Trees use a small one: a
// threshold sized for a server would never be reached by a personal-scale
// Store, and maintenance that never runs is maintenance that does not exist.
//
// A var only so tests can lower it; nothing configures it at runtime.
var walSegmentRollBytes = uint64(1 << 20)

// flushThroughBarrier is the wal.DurabilityBarrier a checkpoint needs: the
// Tree's dirty Pages have to be on disk before the checkpoint claims they are.
//
// The buffer Pool's frames carry no LSN index, so there is no way to select
// "only the Pages below this LSN"; every dirty Page is flushed, which satisfies
// any bound and is therefore correct, just more work than strictly required.
// The cost is bounded by the Pool's capacity. recoveryLSN is passed through
// rather than ignored — it is the no-steal bound each Page is checked against,
// and it has to be handed in because the log holds its own lock while calling
// this, so reading its durable LSN back here would deadlock.
type flushThroughBarrier struct{ database *Database }

func (barrier flushThroughBarrier) FlushThrough(recoveryLSN uint64) error {
	report, err := barrier.database.runtime.FlushDirtyThrough(math.MaxUint64, recoveryLSN)
	if err != nil {
		return fmt.Errorf("flush native KV Tree for checkpoint: %w", err)
	}
	if report.Remaining != 0 {
		return fmt.Errorf("native KV Tree kept %d dirty Pages at checkpoint", report.Remaining)
	}
	if err := barrier.database.manager.Sync(); err != nil {
		return fmt.Errorf("sync native KV Pages for checkpoint: %w", err)
	}
	return nil
}

// maintainRedoLog runs one round once the active Segment has outgrown the roll
// threshold.
//
// Without it this Store's redo log only ever grows, and — worse than the disk
// cost — Open replays the whole of it, which pulls every Page it ever wrote into
// the buffer Pool. That would put the resident index straight back by a
// different door: the Store would hold all its data in memory again, as
// recovered Pages rather than as a map.
//
// Synchronous, and after the write rather than before it: the write is already
// committed and maintenance must not be able to undo it.
func (database *Database) maintainRedoLog() error {
	due, err := rollIsDue(database.set)
	if err != nil || !due {
		return err
	}
	return database.runRedoMaintenance()
}

// relieveRedoRing frees ring space before an append when the ring is already
// full. The ordinary round is driven by the roll threshold and runs after a
// write; this is the last chance to avoid back-pressure, because a full ring
// means the next commit is refused and only a checkpoint moving the tail can
// free space.
func (database *Database) relieveRedoRing() error {
	capacity := database.set.RingBytes()
	if capacity == 0 {
		return nil
	}
	inUse, err := database.set.InUseBytes()
	if err != nil {
		return fmt.Errorf("read native KV redo ring usage: %w", err)
	}
	if inUse < capacity {
		return nil
	}
	return database.runRedoMaintenance()
}

// runRedoMaintenance is one round: roll, checkpoint, reclaim. The three benign
// sentinels mean "nothing to do" and are swallowed; anything else is reported.
func (database *Database) runRedoMaintenance() error {
	barrier := flushThroughBarrier{database: database}
	if _, err := database.set.Roll(); err != nil {
		if errors.Is(err, wal.ErrEmptySegment) {
			return nil
		}
		return fmt.Errorf("roll native KV redo log: %w", err)
	}
	if _, err := database.set.PublishCheckpoint(barrier); err != nil {
		if errors.Is(err, wal.ErrNoCheckpointProgress) {
			return nil
		}
		return fmt.Errorf("publish native KV redo checkpoint: %w", err)
	}
	if _, err := database.set.Reclaim(); err != nil {
		if errors.Is(err, wal.ErrNoReclaimableSegments) {
			return nil
		}
		return fmt.Errorf("reclaim native KV redo Segments: %w", err)
	}
	return nil
}

func rollIsDue(log *wal.SegmentSet) (bool, error) {
	segments, err := log.State()
	if err != nil {
		return false, fmt.Errorf("read native KV redo log state: %w", err)
	}
	if len(segments) == 0 {
		return false, nil
	}
	active := segments[len(segments)-1]
	return active.NextLSN-active.StartLSN >= walSegmentRollBytes, nil
}
