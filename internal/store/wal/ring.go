package wal

import (
	"errors"
	"fmt"
)

// The redo log is a ring: a fixed amount of disk that the log circles around
// rather than an ever-growing chain of Segments.
//
// LSN semantics do not change. An LSN is still a globally monotonic byte
// position; what circles is the file space, and the mapping is the plain
// modulus offset = (LSN - ringBase) mod ringSize. Segment files are the ring's
// cells: a Segment is reclaimed once the checkpoint has moved past it, and the
// space it held is the space the next Segment takes. So
// treecontrol.State.LSN, Page header LSNs, the durable frontier and the
// checkpoint records are all untouched.
//
// Two pointers bound the part of the ring that is in use:
//
//   - the write LSN, where the next record goes (the active Segment's NextLSN);
//   - the checkpoint LSN, the oldest LSN whose changes are not yet in a Page
//     file (Checkpoint.RecoveryLSN).
//
// Everything between them has to stay on disk: it is the only copy of changes
// no Page file holds yet. When a write would push that span past the ring, the
// choice is to overwrite it or to refuse. Overwriting is silent data loss, so
// the log refuses — see ErrRingFull.
//
// See docs/storage/shared-circular-redo-v1.md §3.
const DefaultRingBytes = uint64(64) << 20

// ErrRingFull reports that the in-use span already fills the ring, so the
// caller's record cannot be written without discarding changes that no Page
// file holds yet.
//
// It is a back-pressure signal, not corruption: publishing a checkpoint moves
// the tail forward and reclaiming frees the Segments behind it, after which the
// same write succeeds. A caller that owns a durability barrier should do that
// and retry once before surfacing the error.
var ErrRingFull = errors.New("WAL ring is full")

// CreateSegmentSetWithCapacity creates a Set whose ring holds at most
// ringBytes of in-use log.
func CreateSegmentSetWithCapacity(directory string, startLSN, ringBytes uint64) (*SegmentSet, error) {
	if ringBytes == 0 {
		return nil, fmt.Errorf("%w: WAL ring capacity", ErrInvalid)
	}
	set, err := CreateSegmentSet(directory, startLSN)
	if err != nil {
		return nil, err
	}
	set.ringBytes = ringBytes
	return set, nil
}

// OpenSegmentSetWithCapacity opens a Set with the given ring capacity.
//
// The capacity is not stored in the Set's files. It bounds what this process
// will write, and nothing about the bytes already on disk depends on it, so a
// database opened with a larger ring simply gets more room — there is no
// migration and no format change.
func OpenSegmentSetWithCapacity(directory string, startLSN, ringBytes uint64) (*SegmentSet, error) {
	if ringBytes == 0 {
		return nil, fmt.Errorf("%w: WAL ring capacity", ErrInvalid)
	}
	set, err := OpenSegmentSet(directory, startLSN)
	if err != nil {
		return nil, err
	}
	set.ringBytes = ringBytes
	return set, nil
}

// ringHasRoomLocked reports whether another transaction may be written.
//
// The check is made before the write and against the span already in use, not
// against the span the write would produce: the encoded size of a transaction
// is not known until the writer has encoded it. So the ring may overshoot by
// at most the one transaction that crosses the line. That slack is bounded and
// deliberate — the alternative is to encode every transaction twice.
//
// A Set with no capacity configured is unbounded, which is what every caller
// that has not opted in still gets.
func (set *SegmentSet) ringHasRoomLocked() (bool, error) {
	if set.ringBytes == 0 {
		return true, nil
	}
	inUse, err := set.inUseBytesLocked()
	if err != nil {
		return false, err
	}
	return inUse < set.ringBytes, nil
}

// inUseBytesLocked is the distance from the checkpoint's recovery LSN to the
// write LSN: the part of the log that recovery would have to replay, and so the
// part that cannot be discarded.
//
// With no checkpoint published yet the tail is the oldest Segment the Set still
// holds, because nothing has established that anything before it is durable.
func (set *SegmentSet) inUseBytesLocked() (uint64, error) {
	if len(set.segments) == 0 {
		return 0, nil
	}
	tail := set.segments[0].startLSN
	if set.hasCheckpoint && set.lastCheckpoint.RecoveryLSN > tail {
		tail = set.lastCheckpoint.RecoveryLSN
	}
	head, err := set.segments[len(set.segments)-1].NextLSN()
	if err != nil {
		return 0, err
	}
	if head <= tail {
		return 0, nil
	}
	return head - tail, nil
}

// RingBytes reports the Set's ring capacity, or zero when it is unbounded.
func (set *SegmentSet) RingBytes() uint64 {
	set.mu.Lock()
	defer set.mu.Unlock()
	return set.ringBytes
}

// InUseBytes reports how much of the ring the log currently occupies: the span
// from the checkpoint's recovery LSN to the write LSN.
func (set *SegmentSet) InUseBytes() (uint64, error) {
	set.mu.Lock()
	defer set.mu.Unlock()
	if set.closed {
		return 0, ErrSegmentSetClosed
	}
	return set.inUseBytesLocked()
}
