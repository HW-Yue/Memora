package wal

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sync"
)

const (
	commitPayloadSize    = 56
	commitPayloadVersion = uint16(1)
)

var (
	ErrDuplicateTransaction = errors.New("WAL transaction ID was already committed")
	ErrPoisoned             = errors.New("WAL segment requires recovery")
	commitPayloadMagic      = [4]byte{'M', 'T', 'X', 'N'}
)

type Receipt struct {
	TransactionID uint64
	FirstLSN      uint64
	CommitLSN     uint64
	DurableLSN    uint64
	RecordCount   uint32
	Digest        [sha256.Size]byte
}

type CommittedTransaction struct {
	Receipt Receipt
	Records []Record
}

type TransactionWriter struct {
	mu        sync.Mutex
	segment   *Segment
	committed map[uint64]struct{}
}

func NewTransactionWriter(segment *Segment) (*TransactionWriter, error) {
	if segment == nil {
		return nil, fmt.Errorf("%w: WAL segment is required", ErrInvalid)
	}
	records, err := segment.transactionRecords(false)
	if err != nil {
		return nil, err
	}
	_, committed, hasTail, err := scanCommittedRecords(records)
	if err != nil {
		return nil, err
	}
	if hasTail {
		segment.markPoisoned()
		return nil, ErrPoisoned
	}
	return &TransactionWriter{
		segment:   segment,
		committed: committed,
	}, nil
}

func (writer *TransactionWriter) Commit(transactionID uint64, records []Record) (Receipt, error) {
	if writer == nil || writer.segment == nil {
		return Receipt{}, fmt.Errorf("%w: transaction writer is required", ErrInvalid)
	}
	prepared, err := prepareChanges(transactionID, records)
	if err != nil {
		return Receipt{}, err
	}

	writer.mu.Lock()
	defer writer.mu.Unlock()
	if _, exists := writer.committed[transactionID]; exists {
		return Receipt{}, ErrDuplicateTransaction
	}
	receipt, err := writer.segment.appendTransaction(transactionID, prepared)
	if err != nil {
		return Receipt{}, err
	}
	writer.committed[transactionID] = struct{}{}
	return receipt, nil
}

func ScanCommitted(segment *Segment) ([]CommittedTransaction, error) {
	if segment == nil {
		return nil, fmt.Errorf("%w: WAL segment is required", ErrInvalid)
	}
	records, err := segment.transactionRecords(true)
	if err != nil {
		return nil, err
	}
	transactions, _, _, err := scanCommittedRecords(records)
	return transactions, err
}

func prepareChanges(transactionID uint64, records []Record) ([]Record, error) {
	if transactionID == 0 || len(records) == 0 || uint64(len(records)) > math.MaxUint32 {
		return nil, fmt.Errorf("%w: transaction ID or Record count", ErrInvalid)
	}
	prepared := make([]Record, len(records))
	for index, record := range records {
		if !isChangeType(record.Type) || record.LSN != 0 || record.TransactionID != 0 ||
			len(record.Payload) > maxPayloadSize {
			return nil, fmt.Errorf("%w: transaction change Record", ErrInvalid)
		}
		record.Payload = bytes.Clone(record.Payload)
		prepared[index] = record
	}
	return prepared, nil
}

func (segment *Segment) appendTransaction(
	transactionID uint64,
	records []Record,
) (Receipt, error) {
	segment.mu.Lock()
	defer segment.mu.Unlock()
	if segment.closed {
		return Receipt{}, ErrClosed
	}
	if segment.poisoned {
		return Receipt{}, ErrPoisoned
	}

	firstLSN := segment.nextLSN
	nextLSN := firstLSN
	encodedChanges := make([][]byte, len(records))
	digest := sha256.New()
	for index, record := range records {
		totalLength := uint64(recordHeaderSize + len(record.Payload))
		if nextLSN > math.MaxUint64-totalLength {
			return Receipt{}, fmt.Errorf("%w: LSN overflow", ErrInvalid)
		}
		record.LSN = nextLSN
		record.TransactionID = transactionID
		encoded := encodeRecord(record)
		encodedChanges[index] = encoded
		_, _ = digest.Write(encoded)
		nextLSN += totalLength
	}
	var transactionDigest [sha256.Size]byte
	copy(transactionDigest[:], digest.Sum(nil))
	commit := Record{
		Type:          TypeCommit,
		LSN:           nextLSN,
		TransactionID: transactionID,
		Payload: encodeCommitPayload(
			uint32(len(records)),
			firstLSN,
			transactionDigest,
		),
	}
	encodedCommit := encodeRecord(commit)
	if nextLSN > math.MaxUint64-uint64(len(encodedCommit)) {
		return Receipt{}, fmt.Errorf("%w: LSN overflow", ErrInvalid)
	}
	finalLSN := nextLSN + uint64(len(encodedCommit))
	if finalLSN-segment.startLSN > math.MaxInt64 {
		return Receipt{}, fmt.Errorf("%w: WAL file offset overflow", ErrInvalid)
	}

	for _, encoded := range append(encodedChanges, encodedCommit) {
		offset := segment.nextLSN - segment.startLSN
		if err := writeFullAt(segment.file, encoded, int64(offset)); err != nil {
			segment.poisoned = true
			return Receipt{}, fmt.Errorf("append WAL transaction: %w", err)
		}
		segment.nextLSN += uint64(len(encoded))
	}
	if err := segment.file.Sync(); err != nil {
		segment.poisoned = true
		return Receipt{}, fmt.Errorf("sync WAL transaction: %w", err)
	}
	segment.durableLSN = segment.nextLSN
	return Receipt{
		TransactionID: transactionID,
		FirstLSN:      firstLSN,
		CommitLSN:     commit.LSN,
		DurableLSN:    segment.durableLSN,
		RecordCount:   uint32(len(records)),
		Digest:        transactionDigest,
	}, nil
}

func (segment *Segment) transactionRecords(durableOnly bool) ([]Record, error) {
	segment.mu.RLock()
	defer segment.mu.RUnlock()
	if segment.closed {
		return nil, ErrClosed
	}
	if segment.poisoned {
		return nil, ErrPoisoned
	}
	endLSN := segment.nextLSN
	if durableOnly {
		endLSN = segment.durableLSN
	}
	fileSize := int64(endLSN - segment.startLSN)
	records, _, err := scanRecords(segment.file, segment.startLSN, fileSize)
	return records, err
}

func (segment *Segment) markPoisoned() {
	segment.mu.Lock()
	defer segment.mu.Unlock()
	segment.poisoned = true
}

func scanCommittedRecords(
	records []Record,
) ([]CommittedTransaction, map[uint64]struct{}, bool, error) {
	transactions := make([]CommittedTransaction, 0)
	committed := make(map[uint64]struct{})
	var pending []Record
	var pendingID uint64

	for _, record := range records {
		if record.Type == TypeCheckpoint {
			if len(pending) != 0 {
				return nil, nil, false, corruptTransaction("checkpoint inside transaction")
			}
			if _, err := decodeCheckpointRecord(record, math.MaxUint64); err != nil {
				return nil, nil, false, err
			}
			continue
		}
		if isChangeType(record.Type) {
			if record.TransactionID == 0 {
				return nil, nil, false, corruptTransaction("zero transaction ID")
			}
			if len(pending) == 0 {
				if _, duplicate := committed[record.TransactionID]; duplicate {
					return nil, nil, false, corruptTransaction("reused transaction ID")
				}
				pendingID = record.TransactionID
			} else if record.TransactionID != pendingID {
				return nil, nil, false, corruptTransaction("interleaved transaction")
			}
			pending = append(pending, record)
			continue
		}
		if record.Type != TypeCommit || len(pending) == 0 ||
			record.TransactionID != pendingID || record.SpaceID != 0 || record.PageID != 0 {
			return nil, nil, false, corruptTransaction("invalid commit boundary")
		}
		count, firstLSN, digest, err := decodeCommitPayload(record.Payload)
		if err != nil {
			return nil, nil, false, err
		}
		if count != uint32(len(pending)) || firstLSN != pending[0].LSN {
			return nil, nil, false, corruptTransaction("commit count or first LSN")
		}
		calculated := sha256.New()
		for _, change := range pending {
			_, _ = calculated.Write(encodeRecord(change))
		}
		if !bytes.Equal(digest[:], calculated.Sum(nil)) {
			return nil, nil, false, corruptTransaction("commit digest")
		}
		if _, duplicate := committed[pendingID]; duplicate {
			return nil, nil, false, corruptTransaction("duplicate commit")
		}
		receipt := Receipt{
			TransactionID: pendingID,
			FirstLSN:      firstLSN,
			CommitLSN:     record.LSN,
			DurableLSN:    record.LSN + uint64(recordHeaderSize+len(record.Payload)),
			RecordCount:   count,
			Digest:        digest,
		}
		transactions = append(transactions, CommittedTransaction{
			Receipt: receipt,
			Records: pending,
		})
		committed[pendingID] = struct{}{}
		pending = nil
		pendingID = 0
	}
	return transactions, committed, len(pending) != 0, nil
}

func encodeCommitPayload(
	recordCount uint32,
	firstLSN uint64,
	digest [sha256.Size]byte,
) []byte {
	payload := make([]byte, commitPayloadSize)
	copy(payload[0:4], commitPayloadMagic[:])
	binary.LittleEndian.PutUint16(payload[4:6], commitPayloadVersion)
	binary.LittleEndian.PutUint16(payload[6:8], commitPayloadSize)
	binary.LittleEndian.PutUint32(payload[8:12], recordCount)
	binary.LittleEndian.PutUint64(payload[16:24], firstLSN)
	copy(payload[24:56], digest[:])
	return payload
}

func decodeCommitPayload(payload []byte) (uint32, uint64, [sha256.Size]byte, error) {
	var digest [sha256.Size]byte
	if len(payload) != commitPayloadSize ||
		!bytes.Equal(payload[0:4], commitPayloadMagic[:]) ||
		binary.LittleEndian.Uint16(payload[4:6]) != commitPayloadVersion ||
		binary.LittleEndian.Uint16(payload[6:8]) != commitPayloadSize ||
		binary.LittleEndian.Uint32(payload[8:12]) == 0 ||
		!allZero(payload[12:16]) {
		return 0, 0, digest, corruptTransaction("commit payload")
	}
	copy(digest[:], payload[24:56])
	return binary.LittleEndian.Uint32(payload[8:12]),
		binary.LittleEndian.Uint64(payload[16:24]),
		digest,
		nil
}

func isChangeType(value Type) bool {
	return value >= TypePageInit && value <= TypeAllocator
}

func corruptTransaction(reason string) error {
	return fmt.Errorf("%w: WAL transaction %s", ErrCorrupt, reason)
}
