package wal

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	checkpointPayloadSize    = 32
	checkpointPayloadVersion = uint16(1)
)

var (
	ErrNoCheckpointProgress = errors.New("WAL checkpoint has no new committed transaction")
	checkpointPayloadMagic  = [4]byte{'M', 'C', 'H', 'K'}
)

type DurabilityBarrier interface {
	FlushThrough(recoveryLSN uint64) error
}

type Checkpoint struct {
	ID               uint64
	RecoveryLSN      uint64
	CoveredSegmentID uint64
	RecordLSN        uint64
	DurableLSN       uint64
}

func (set *SegmentSet) PublishCheckpoint(barrier DurabilityBarrier) (Checkpoint, error) {
	set.mu.Lock()
	defer set.mu.Unlock()
	if set.closed {
		return Checkpoint{}, ErrSegmentSetClosed
	}
	if barrier == nil {
		return Checkpoint{}, fmt.Errorf("%w: durability barrier", ErrInvalid)
	}
	if set.lastCommitDurableLSN == 0 ||
		(set.hasCheckpoint && set.lastCommitDurableLSN <= set.lastCheckpoint.RecoveryLSN) {
		return Checkpoint{}, ErrNoCheckpointProgress
	}
	active := set.segments[len(set.segments)-1]
	records, err := active.transactionRecords(false)
	if err != nil {
		return Checkpoint{}, err
	}
	transactions, _, hasTail, err := scanCommittedRecords(records)
	if err != nil {
		return Checkpoint{}, err
	}
	if hasTail {
		active.markPoisoned()
		return Checkpoint{}, ErrPoisoned
	}
	if len(transactions) != set.activeCommits {
		return Checkpoint{}, fmt.Errorf("%w: active Segment changed outside Set", ErrCorrupt)
	}
	if err := barrier.FlushThrough(set.lastCommitDurableLSN); err != nil {
		return Checkpoint{}, fmt.Errorf("checkpoint Page durability barrier: %w", err)
	}
	checkpoint := Checkpoint{
		ID:               1,
		RecoveryLSN:      set.lastCommitDurableLSN,
		CoveredSegmentID: set.lastCommitSegmentID,
	}
	if set.hasCheckpoint {
		checkpoint.ID = set.lastCheckpoint.ID + 1
	}
	recordLSN, err := active.Append(Record{
		Type:    TypeCheckpoint,
		Payload: encodeCheckpointPayload(checkpoint),
	})
	if err != nil {
		active.markPoisoned()
		return Checkpoint{}, fmt.Errorf("append WAL checkpoint: %w", err)
	}
	if err := active.Sync(); err != nil {
		return Checkpoint{}, err
	}
	durableLSN, err := active.DurableLSN()
	if err != nil {
		return Checkpoint{}, err
	}
	checkpoint.RecordLSN = recordLSN
	checkpoint.DurableLSN = durableLSN
	set.lastCheckpoint = checkpoint
	set.hasCheckpoint = true
	return checkpoint, nil
}

func (set *SegmentSet) LatestCheckpoint() (Checkpoint, bool, error) {
	set.mu.Lock()
	defer set.mu.Unlock()
	if set.closed {
		return Checkpoint{}, false, ErrSegmentSetClosed
	}
	return set.lastCheckpoint, set.hasCheckpoint, nil
}

func RecoverSegmentSet(
	set *SegmentSet,
	spaces map[uint64]PageStore,
) (RecoveryReport, error) {
	if set == nil {
		return RecoveryReport{}, fmt.Errorf("%w: Segment Set", ErrInvalid)
	}
	set.mu.Lock()
	defer set.mu.Unlock()
	if set.closed {
		return RecoveryReport{}, ErrSegmentSetClosed
	}
	transactions, err := set.scanCommittedLocked()
	if err != nil {
		return RecoveryReport{}, err
	}
	if set.hasCheckpoint {
		first := 0
		for first < len(transactions) &&
			transactions[first].Receipt.DurableLSN <= set.lastCheckpoint.RecoveryLSN {
			first++
		}
		transactions = transactions[first:]
	}
	return recoverTransactions(transactions, spaces)
}

func encodeCheckpointPayload(checkpoint Checkpoint) []byte {
	payload := make([]byte, checkpointPayloadSize)
	copy(payload[0:4], checkpointPayloadMagic[:])
	binary.LittleEndian.PutUint16(payload[4:6], checkpointPayloadVersion)
	binary.LittleEndian.PutUint16(payload[6:8], checkpointPayloadSize)
	binary.LittleEndian.PutUint64(payload[8:16], checkpoint.ID)
	binary.LittleEndian.PutUint64(payload[16:24], checkpoint.RecoveryLSN)
	binary.LittleEndian.PutUint64(payload[24:32], checkpoint.CoveredSegmentID)
	return payload
}

func decodeCheckpointRecord(record Record, segmentID uint64) (Checkpoint, error) {
	if record.Type != TypeCheckpoint ||
		record.TransactionID != 0 ||
		record.SpaceID != 0 ||
		record.PageID != 0 ||
		len(record.Payload) != checkpointPayloadSize ||
		!bytes.Equal(record.Payload[0:4], checkpointPayloadMagic[:]) ||
		binary.LittleEndian.Uint16(record.Payload[4:6]) != checkpointPayloadVersion ||
		binary.LittleEndian.Uint16(record.Payload[6:8]) != checkpointPayloadSize {
		return Checkpoint{}, fmt.Errorf("%w: invalid checkpoint Record", ErrCorrupt)
	}
	checkpoint := Checkpoint{
		ID:               binary.LittleEndian.Uint64(record.Payload[8:16]),
		RecoveryLSN:      binary.LittleEndian.Uint64(record.Payload[16:24]),
		CoveredSegmentID: binary.LittleEndian.Uint64(record.Payload[24:32]),
		RecordLSN:        record.LSN,
		DurableLSN:       record.LSN + uint64(recordHeaderSize+len(record.Payload)),
	}
	if checkpoint.ID == 0 ||
		checkpoint.RecoveryLSN == 0 ||
		checkpoint.CoveredSegmentID == 0 ||
		checkpoint.CoveredSegmentID > segmentID ||
		checkpoint.RecoveryLSN > checkpoint.RecordLSN {
		return Checkpoint{}, fmt.Errorf("%w: invalid checkpoint fields", ErrCorrupt)
	}
	return checkpoint, nil
}
