package wal

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

const (
	segmentFilePrefix = "segment-"
	segmentFileSuffix = ".wal"
	segmentIDWidth    = 20
)

var (
	ErrSegmentSetClosed = errors.New("WAL Segment Set is closed")
	ErrEmptySegment     = errors.New("WAL active Segment has no committed transaction")
)

type SegmentInfo struct {
	ID       uint64
	StartLSN uint64
	NextLSN  uint64
}

type SegmentSet struct {
	mu                   sync.Mutex
	directory            string
	segments             []*Segment
	writer               *TransactionWriter
	transactionIDs       map[uint64]struct{}
	activeCommits        int
	closed               bool
	createSegment        func(string, uint64, uint64) (*Segment, error)
	lastCommitDurableLSN uint64
	lastCommitSegmentID  uint64
	lastCheckpoint       Checkpoint
	hasCheckpoint        bool
}

func CreateSegmentSet(directory string, startLSN uint64) (*SegmentSet, error) {
	if directory == "" {
		return nil, fmt.Errorf("%w: Segment Set directory", ErrInvalid)
	}
	if err := os.Mkdir(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create WAL Segment Set directory: %w", err)
	}
	cleanupDirectory := true
	defer func() {
		if cleanupDirectory {
			_ = os.Remove(directory)
		}
	}()
	if err := syncDirectory(filepath.Dir(directory)); err != nil {
		return nil, fmt.Errorf("sync WAL Segment Set parent: %w", err)
	}
	segment, err := Create(segmentPath(directory, 1), 1, startLSN)
	if err != nil {
		return nil, err
	}
	cleanupSegment := true
	defer func() {
		if cleanupSegment {
			_ = segment.Close()
			_ = os.Remove(segmentPath(directory, 1))
		}
	}()
	writer, err := NewTransactionWriter(segment)
	if err != nil {
		return nil, err
	}
	cleanupSegment = false
	cleanupDirectory = false
	return &SegmentSet{
		directory:      directory,
		segments:       []*Segment{segment},
		writer:         writer,
		transactionIDs: make(map[uint64]struct{}),
		createSegment:  Create,
	}, nil
}

func OpenSegmentSet(directory string, startLSN uint64) (*SegmentSet, error) {
	if directory == "" {
		return nil, fmt.Errorf("%w: Segment Set directory", ErrInvalid)
	}
	info, err := os.Stat(directory)
	if err != nil {
		return nil, fmt.Errorf("stat WAL Segment Set: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: Segment Set is not a directory", ErrCorrupt)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("read WAL Segment Set: %w", err)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("%w: empty Segment Set", ErrCorrupt)
	}

	segments := make([]*Segment, 0, len(entries))
	closeOnError := true
	defer func() {
		if closeOnError {
			for _, segment := range segments {
				_ = segment.Close()
			}
		}
	}()
	transactionIDs := make(map[uint64]struct{})
	commitBoundaries := make(map[uint64]uint64)
	activeCommits := 0
	var lastCommitDurableLSN uint64
	var lastCommitSegmentID uint64
	var lastCheckpoint Checkpoint
	hasCheckpoint := false
	expectedStartLSN := startLSN
	for index, entry := range entries {
		segmentID, err := parseSegmentFilename(entry.Name())
		if err != nil || entry.IsDir() || segmentID != uint64(index+1) {
			return nil, fmt.Errorf("%w: invalid Segment Set entry %q", ErrCorrupt, entry.Name())
		}
		segment, err := Open(
			filepath.Join(directory, entry.Name()),
			segmentID,
			expectedStartLSN,
		)
		if err != nil {
			return nil, err
		}
		segments = append(segments, segment)
		records, err := segment.transactionRecords(false)
		if err != nil {
			return nil, err
		}
		transactions, _, hasTail, err := scanCommittedRecords(records)
		if err != nil {
			return nil, err
		}
		if hasTail {
			return nil, ErrPoisoned
		}
		if index < len(entries)-1 && len(transactions) == 0 {
			return nil, fmt.Errorf("%w: empty non-active Segment", ErrCorrupt)
		}
		for _, transaction := range transactions {
			if _, duplicate := transactionIDs[transaction.Receipt.TransactionID]; duplicate {
				return nil, fmt.Errorf("%w: duplicate transaction across Segments", ErrCorrupt)
			}
			transactionIDs[transaction.Receipt.TransactionID] = struct{}{}
			commitBoundaries[transaction.Receipt.DurableLSN] = segmentID
			lastCommitDurableLSN = transaction.Receipt.DurableLSN
			lastCommitSegmentID = segmentID
		}
		for _, record := range records {
			if record.Type != TypeCheckpoint {
				continue
			}
			checkpoint, err := decodeCheckpointRecord(record, segmentID)
			if err != nil {
				return nil, err
			}
			if (!hasCheckpoint && checkpoint.ID != 1) ||
				(hasCheckpoint && (checkpoint.ID != lastCheckpoint.ID+1 ||
					checkpoint.RecoveryLSN <= lastCheckpoint.RecoveryLSN)) {
				return nil, fmt.Errorf("%w: checkpoint sequence", ErrCorrupt)
			}
			if coveredSegment, exists := commitBoundaries[checkpoint.RecoveryLSN]; !exists ||
				coveredSegment != checkpoint.CoveredSegmentID {
				return nil, fmt.Errorf("%w: checkpoint recovery boundary", ErrCorrupt)
			}
			lastCheckpoint = checkpoint
			hasCheckpoint = true
		}
		if index == len(entries)-1 {
			activeCommits = len(transactions)
		}
		expectedStartLSN, err = segment.NextLSN()
		if err != nil {
			return nil, err
		}
	}
	writer, err := NewTransactionWriter(segments[len(segments)-1])
	if err != nil {
		return nil, err
	}
	closeOnError = false
	return &SegmentSet{
		directory:            directory,
		segments:             segments,
		writer:               writer,
		transactionIDs:       transactionIDs,
		activeCommits:        activeCommits,
		createSegment:        Create,
		lastCommitDurableLSN: lastCommitDurableLSN,
		lastCommitSegmentID:  lastCommitSegmentID,
		lastCheckpoint:       lastCheckpoint,
		hasCheckpoint:        hasCheckpoint,
	}, nil
}

func (set *SegmentSet) Commit(transactionID uint64, records []Record) (Receipt, error) {
	set.mu.Lock()
	defer set.mu.Unlock()
	if set.closed {
		return Receipt{}, ErrSegmentSetClosed
	}
	if _, duplicate := set.transactionIDs[transactionID]; duplicate {
		return Receipt{}, ErrDuplicateTransaction
	}
	receipt, err := set.writer.Commit(transactionID, records)
	if err != nil {
		return Receipt{}, err
	}
	set.transactionIDs[transactionID] = struct{}{}
	set.activeCommits++
	set.lastCommitDurableLSN = receipt.DurableLSN
	set.lastCommitSegmentID = set.segments[len(set.segments)-1].segmentID
	return receipt, nil
}

func (set *SegmentSet) Roll() (SegmentInfo, error) {
	set.mu.Lock()
	defer set.mu.Unlock()
	if set.closed {
		return SegmentInfo{}, ErrSegmentSetClosed
	}
	active := set.segments[len(set.segments)-1]
	records, err := active.transactionRecords(false)
	if err != nil {
		return SegmentInfo{}, err
	}
	transactions, _, hasTail, err := scanCommittedRecords(records)
	if err != nil {
		return SegmentInfo{}, err
	}
	if hasTail {
		active.markPoisoned()
		return SegmentInfo{}, ErrPoisoned
	}
	if set.activeCommits == 0 {
		return SegmentInfo{}, ErrEmptySegment
	}
	if len(transactions) != set.activeCommits {
		return SegmentInfo{}, fmt.Errorf("%w: active Segment changed outside Set", ErrCorrupt)
	}
	if err := active.Sync(); err != nil {
		return SegmentInfo{}, err
	}
	startLSN, err := active.NextLSN()
	if err != nil {
		return SegmentInfo{}, err
	}
	segmentID := uint64(len(set.segments) + 1)
	segment, err := set.createSegment(
		segmentPath(set.directory, segmentID),
		segmentID,
		startLSN,
	)
	if err != nil {
		return SegmentInfo{}, err
	}
	writer, err := NewTransactionWriter(segment)
	if err != nil {
		_ = segment.Close()
		return SegmentInfo{}, err
	}
	nextLSN, err := segment.NextLSN()
	if err != nil {
		_ = segment.Close()
		return SegmentInfo{}, err
	}
	set.segments = append(set.segments, segment)
	set.writer = writer
	set.activeCommits = 0
	return SegmentInfo{ID: segmentID, StartLSN: startLSN, NextLSN: nextLSN}, nil
}

func (set *SegmentSet) ScanCommitted() ([]CommittedTransaction, error) {
	set.mu.Lock()
	defer set.mu.Unlock()
	if set.closed {
		return nil, ErrSegmentSetClosed
	}
	return set.scanCommittedLocked()
}

func (set *SegmentSet) scanCommittedLocked() ([]CommittedTransaction, error) {
	transactions := make([]CommittedTransaction, 0)
	seen := make(map[uint64]struct{})
	for _, segment := range set.segments {
		current, err := ScanCommitted(segment)
		if err != nil {
			return nil, err
		}
		for _, transaction := range current {
			if _, duplicate := seen[transaction.Receipt.TransactionID]; duplicate {
				return nil, fmt.Errorf("%w: duplicate transaction across Segments", ErrCorrupt)
			}
			seen[transaction.Receipt.TransactionID] = struct{}{}
			transactions = append(transactions, transaction)
		}
	}
	return transactions, nil
}

func (set *SegmentSet) State() ([]SegmentInfo, error) {
	set.mu.Lock()
	defer set.mu.Unlock()
	if set.closed {
		return nil, ErrSegmentSetClosed
	}
	state := make([]SegmentInfo, len(set.segments))
	for index, segment := range set.segments {
		nextLSN, err := segment.NextLSN()
		if err != nil {
			return nil, err
		}
		state[index] = SegmentInfo{
			ID:       segment.segmentID,
			StartLSN: segment.startLSN,
			NextLSN:  nextLSN,
		}
	}
	return state, nil
}

func (set *SegmentSet) Close() error {
	set.mu.Lock()
	defer set.mu.Unlock()
	if set.closed {
		return nil
	}
	set.closed = true
	var result error
	for _, segment := range set.segments {
		result = errors.Join(result, segment.Close())
	}
	return result
}

func segmentPath(directory string, segmentID uint64) string {
	return filepath.Join(directory, fmt.Sprintf(
		"%s%0*d%s",
		segmentFilePrefix,
		segmentIDWidth,
		segmentID,
		segmentFileSuffix,
	))
}

func parseSegmentFilename(name string) (uint64, error) {
	expectedLength := len(segmentFilePrefix) + segmentIDWidth + len(segmentFileSuffix)
	if len(name) != expectedLength ||
		!strings.HasPrefix(name, segmentFilePrefix) ||
		!strings.HasSuffix(name, segmentFileSuffix) {
		return 0, ErrCorrupt
	}
	digits := name[len(segmentFilePrefix) : len(segmentFilePrefix)+segmentIDWidth]
	segmentID, err := strconv.ParseUint(digits, 10, 64)
	if err != nil || segmentID == 0 {
		return 0, ErrCorrupt
	}
	return segmentID, nil
}
