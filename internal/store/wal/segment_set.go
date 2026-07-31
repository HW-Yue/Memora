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
	mu                            sync.Mutex
	directory                     string
	segments                      []*Segment
	writer                        *TransactionWriter
	transactionIDs                map[uint64]struct{}
	activeCommits                 int
	closed                        bool
	createSegment                 func(string, uint64, uint64) (*Segment, error)
	lastCommitDurableLSN          uint64
	lastCommitSegmentID           uint64
	lastCheckpoint                Checkpoint
	hasCheckpoint                 bool
	writeManifestTemp             func(string, []byte) (string, error)
	renameManifest                func(string, string) error
	removeSegmentFile             func(string) error
	syncSetDirectory              func(string) error
	retainedManifest              retainedManifest
	hasRetainedManifest           bool
	reclaimedTransactionHighWater uint64
	frontierFiles                 [2]segmentFile
	frontier                      frontierState
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
	firstLSN, err := segment.NextLSN()
	if err != nil {
		return nil, err
	}
	frontierFiles, frontier, err := createFrontierControls(directory, FrontierInfo{
		Generation: 1, SegmentID: 1, DurableEndLSN: firstLSN,
	})
	if err != nil {
		return nil, err
	}
	cleanupFrontier := true
	defer func() {
		if cleanupFrontier {
			closeAndRemoveFrontierFiles(directory, frontierFiles)
		}
	}()
	cleanupSegment = false
	cleanupDirectory = false
	cleanupFrontier = false
	set := &SegmentSet{
		directory:      directory,
		segments:       []*Segment{segment},
		writer:         writer,
		transactionIDs: make(map[uint64]struct{}),
		createSegment:  Create,
		frontierFiles:  frontierFiles,
		frontier:       frontier,
	}
	configureReclaimOperations(set)
	return set, nil
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
	segmentEntries, retained, hasRetained, err := inspectSetDirectory(directory, entries)
	if err != nil {
		return nil, err
	}
	frontierFiles, frontier, err := openFrontierControls(directory)
	if err != nil {
		return nil, err
	}
	closeFrontierOnError := true
	defer func() {
		if closeFrontierOnError {
			_ = closeFrontierFiles(frontierFiles)
		}
	}()

	segments := make([]*Segment, 0, len(segmentEntries))
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
	foundRetainedCheckpoint := false
	firstSegmentID := uint64(1)
	expectedStartLSN := startLSN
	if hasRetained {
		firstSegmentID = retained.FirstSegmentID
		expectedStartLSN = retained.FirstStartLSN
	}
	for index, entry := range segmentEntries {
		segmentID, err := parseSegmentFilename(entry.Name())
		if err != nil || entry.IsDir() || segmentID != firstSegmentID+uint64(index) {
			return nil, fmt.Errorf("%w: invalid Segment Set entry %q", ErrCorrupt, entry.Name())
		}
		if err := validateFrontierFileBoundary(
			entry, segmentID, expectedStartLSN, frontier.FrontierInfo,
		); err != nil {
			return nil, err
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
		if index < len(segmentEntries)-1 && len(transactions) == 0 {
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
			if (!hasCheckpoint && !hasRetained && checkpoint.ID != 1) ||
				(hasCheckpoint && (checkpoint.ID != lastCheckpoint.ID+1 ||
					checkpoint.RecoveryLSN <= lastCheckpoint.RecoveryLSN)) {
				return nil, fmt.Errorf("%w: checkpoint sequence", ErrCorrupt)
			}
			coveredSegment, boundaryExists := commitBoundaries[checkpoint.RecoveryLSN]
			if boundaryExists && coveredSegment != checkpoint.CoveredSegmentID {
				return nil, fmt.Errorf("%w: checkpoint recovery boundary", ErrCorrupt)
			}
			if !boundaryExists && (!hasRetained || checkpoint.ID > retained.Checkpoint.ID) {
				return nil, fmt.Errorf("%w: checkpoint recovery boundary", ErrCorrupt)
			}
			if hasRetained && checkpoint == retained.Checkpoint {
				if segmentID != retained.FirstSegmentID {
					return nil, fmt.Errorf("%w: retained checkpoint Segment", ErrCorrupt)
				}
				foundRetainedCheckpoint = true
			}
			lastCheckpoint = checkpoint
			hasCheckpoint = true
		}
		if index == len(segmentEntries)-1 {
			activeCommits = len(transactions)
		}
		expectedStartLSN, err = segment.NextLSN()
		if err != nil {
			return nil, err
		}
	}
	if len(segments) == 0 || segments[len(segments)-1].segmentID != frontier.SegmentID {
		return nil, fmt.Errorf("%w: durable frontier Segment is missing", ErrCorrupt)
	}
	if expectedStartLSN != frontier.DurableEndLSN {
		return nil, fmt.Errorf("%w: durable frontier end LSN", ErrCorrupt)
	}
	if lastCommitDurableLSN != 0 && frontier.LastTransactionID == 0 {
		return nil, fmt.Errorf("%w: durable frontier lost last transaction", ErrCorrupt)
	}
	if frontier.LastTransactionID != 0 {
		_, retainedTransaction := transactionIDs[frontier.LastTransactionID]
		if !retainedTransaction &&
			(!hasRetained || frontier.LastTransactionID > retained.ReclaimedTransactionHigh) {
			return nil, fmt.Errorf("%w: durable frontier transaction identity", ErrCorrupt)
		}
	}
	if hasRetained && !foundRetainedCheckpoint {
		return nil, fmt.Errorf("%w: retained checkpoint is missing", ErrCorrupt)
	}
	writer, err := NewTransactionWriter(segments[len(segments)-1])
	if err != nil {
		return nil, err
	}
	closeOnError = false
	closeFrontierOnError = false
	set := &SegmentSet{
		directory:                     directory,
		segments:                      segments,
		writer:                        writer,
		transactionIDs:                transactionIDs,
		activeCommits:                 activeCommits,
		createSegment:                 Create,
		lastCommitDurableLSN:          lastCommitDurableLSN,
		lastCommitSegmentID:           lastCommitSegmentID,
		lastCheckpoint:                lastCheckpoint,
		hasCheckpoint:                 hasCheckpoint,
		retainedManifest:              retained,
		hasRetainedManifest:           hasRetained,
		reclaimedTransactionHighWater: retained.ReclaimedTransactionHigh,
		frontierFiles:                 frontierFiles,
		frontier:                      frontier,
	}
	configureReclaimOperations(set)
	return set, nil
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
	if transactionID != 0 && transactionID <= set.reclaimedTransactionHighWater {
		return Receipt{}, ErrDuplicateTransaction
	}
	receipt, err := set.writer.Commit(transactionID, records)
	if err != nil {
		if errors.Is(err, ErrInvalid) || errors.Is(err, ErrDuplicateTransaction) ||
			errors.Is(err, ErrPoisoned) || errors.Is(err, ErrClosed) {
			return Receipt{}, err
		}
		return Receipt{}, outcomeUnknown("commit WAL", err)
	}
	active := set.segments[len(set.segments)-1]
	if err := set.publishFrontier(active.segmentID, receipt.DurableLSN, transactionID); err != nil {
		active.markPoisoned()
		return Receipt{}, outcomeUnknown("publish commit frontier", err)
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
		return SegmentInfo{}, outcomeUnknown("sync active Segment for roll", err)
	}
	startLSN, err := active.NextLSN()
	if err != nil {
		return SegmentInfo{}, err
	}
	segmentID := set.segments[len(set.segments)-1].segmentID + 1
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
		active.markPoisoned()
		return SegmentInfo{}, outcomeUnknown("open new active Segment", err)
	}
	nextLSN, err := segment.NextLSN()
	if err != nil {
		_ = segment.Close()
		active.markPoisoned()
		return SegmentInfo{}, outcomeUnknown("read new active Segment boundary", err)
	}
	if err := set.publishFrontier(
		segmentID, nextLSN, set.frontier.LastTransactionID,
	); err != nil {
		_ = segment.Close()
		active.markPoisoned()
		return SegmentInfo{}, outcomeUnknown("publish roll frontier", err)
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
	result = errors.Join(result, closeFrontierFiles(set.frontierFiles))
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
