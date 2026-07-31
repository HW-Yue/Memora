package wal

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
)

func TestTransactionCommitSyncReceiptAndReopen(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "wal.log")
	segment, err := Create(path, 1, 1000)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	writer, err := NewTransactionWriter(segment)
	if err != nil {
		t.Fatalf("NewTransactionWriter() error = %v", err)
	}
	changes := []Record{
		{Type: TypePageInit, SpaceID: 7, PageID: 1, Payload: []byte("init")},
		{Type: TypePageDelta, SpaceID: 7, PageID: 1, Payload: []byte("delta")},
	}
	receipt, err := writer.Commit(11, changes)
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if receipt.TransactionID != 11 || receipt.RecordCount != 2 ||
		receipt.FirstLSN != 1064 || receipt.CommitLSN <= receipt.FirstLSN ||
		receipt.DurableLSN <= receipt.CommitLSN {
		t.Fatalf("Commit() receipt = %#v", receipt)
	}
	durable, _ := segment.DurableLSN()
	if receipt.DurableLSN != durable {
		t.Fatalf("receipt durable LSN = %d, Segment = %d", receipt.DurableLSN, durable)
	}
	committed, err := ScanCommitted(segment)
	if err != nil {
		t.Fatalf("ScanCommitted() error = %v", err)
	}
	assertCommitted(t, committed, 11, changes, receipt)
	if err := segment.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := Open(path, 1, 1000)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	committed, err = ScanCommitted(reopened)
	if err != nil {
		t.Fatalf("ScanCommitted(reopen) error = %v", err)
	}
	assertCommitted(t, committed, 11, changes, receipt)
	if _, err := NewTransactionWriter(reopened); err != nil {
		t.Fatalf("NewTransactionWriter(reopen) error = %v", err)
	}
	reopenedWriter, err := NewTransactionWriter(reopened)
	if err != nil {
		t.Fatalf("NewTransactionWriter(reopen duplicate check) error = %v", err)
	}
	if _, err := reopenedWriter.Commit(11, changes); !errors.Is(err, ErrDuplicateTransaction) {
		t.Fatalf("Commit(reopen duplicate) error = %v, want ErrDuplicateTransaction", err)
	}
}

func TestTransactionRejectsInvalidAndDuplicate(t *testing.T) {
	t.Parallel()

	segment := createTestSegment(t)
	writer, err := NewTransactionWriter(segment)
	if err != nil {
		t.Fatalf("NewTransactionWriter() error = %v", err)
	}
	valid := []Record{{Type: TypePageDelta}}
	tests := []struct {
		name    string
		id      uint64
		records []Record
	}{
		{name: "zero ID", records: valid},
		{name: "empty", id: 1},
		{name: "commit type", id: 2, records: []Record{{Type: TypeCommit}}},
		{name: "checkpoint type", id: 3, records: []Record{{Type: TypeCheckpoint}}},
		{name: "prefilled LSN", id: 4, records: []Record{{Type: TypePageDelta, LSN: 9}}},
		{name: "prefilled transaction", id: 5, records: []Record{{Type: TypePageDelta, TransactionID: 8}}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			if _, err := writer.Commit(test.id, test.records); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Commit() error = %v, want ErrInvalid", err)
			}
		})
	}
	if _, err := writer.Commit(7, valid); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if _, err := writer.Commit(7, valid); !errors.Is(err, ErrDuplicateTransaction) {
		t.Fatalf("Commit(duplicate) error = %v, want ErrDuplicateTransaction", err)
	}
}

func TestTransactionWriteAndSyncFailurePoisonSegment(t *testing.T) {
	t.Parallel()

	for failWriteCall := 1; failWriteCall <= 3; failWriteCall++ {
		failWriteCall := failWriteCall
		t.Run("write "+string(rune('0'+failWriteCall)), func(t *testing.T) {
			segment := createTestSegment(t)
			injected := errors.New("injected write failure")
			segment.file = &transactionFaultFile{
				segmentFile:   segment.file,
				failWriteCall: failWriteCall,
				writeErr:      injected,
			}
			writer, err := NewTransactionWriter(segment)
			if err != nil {
				t.Fatalf("NewTransactionWriter() error = %v", err)
			}
			before, _ := segment.DurableLSN()
			if _, err := writer.Commit(1, []Record{
				{Type: TypePageInit},
				{Type: TypePageDelta},
			}); !errors.Is(err, injected) {
				t.Fatalf("Commit() error = %v, want injected error", err)
			}
			after, _ := segment.DurableLSN()
			if after != before {
				t.Fatalf("durable LSN advanced from %d to %d", before, after)
			}
			if _, err := writer.Commit(2, []Record{{Type: TypePageInit}}); !errors.Is(err, ErrPoisoned) {
				t.Fatalf("Commit(after fault) error = %v, want ErrPoisoned", err)
			}
			if _, err := segment.Append(Record{Type: TypePageInit}); !errors.Is(err, ErrPoisoned) {
				t.Fatalf("Append(after fault) error = %v, want ErrPoisoned", err)
			}
		})
	}

	t.Run("sync", func(t *testing.T) {
		segment := createTestSegment(t)
		injected := errors.New("injected sync failure")
		segment.file = &transactionFaultFile{
			segmentFile: segment.file,
			syncErr:     injected,
		}
		writer, err := NewTransactionWriter(segment)
		if err != nil {
			t.Fatalf("NewTransactionWriter() error = %v", err)
		}
		before, _ := segment.DurableLSN()
		if _, err := writer.Commit(1, []Record{{Type: TypePageInit}}); !errors.Is(err, injected) {
			t.Fatalf("Commit() error = %v, want injected error", err)
		}
		after, _ := segment.DurableLSN()
		if after != before {
			t.Fatalf("durable LSN advanced from %d to %d", before, after)
		}
		if _, err := writer.Commit(2, []Record{{Type: TypePageInit}}); !errors.Is(err, ErrPoisoned) {
			t.Fatalf("Commit(after fault) error = %v, want ErrPoisoned", err)
		}
		if _, err := segment.Append(Record{Type: TypePageInit}); !errors.Is(err, ErrPoisoned) {
			t.Fatalf("Append(after fault) error = %v, want ErrPoisoned", err)
		}
	})
}

func TestScanCommittedIgnoresCompleteUncommittedTail(t *testing.T) {
	t.Parallel()

	segment := createTestSegment(t)
	writer, err := NewTransactionWriter(segment)
	if err != nil {
		t.Fatalf("NewTransactionWriter() error = %v", err)
	}
	committedReceipt, err := writer.Commit(1, []Record{
		{Type: TypePageInit, Payload: []byte("committed")},
	})
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if _, err := segment.Append(Record{
		Type:          TypePageDelta,
		TransactionID: 99,
		Payload:       []byte("not committed"),
	}); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if err := segment.Sync(); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	committed, err := ScanCommitted(segment)
	if err != nil {
		t.Fatalf("ScanCommitted() error = %v", err)
	}
	if len(committed) != 1 || committed[0].Receipt != committedReceipt {
		t.Fatalf("ScanCommitted() = %#v, want committed transaction only", committed)
	}
}

func TestScanCommittedRejectsTamperedCommit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		payloadOffset int
	}{
		{name: "record count", payloadOffset: 8},
		{name: "first LSN", payloadOffset: 16},
		{name: "digest", payloadOffset: 24},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "wal.log")
			segment, err := Create(path, 1, 0)
			if err != nil {
				t.Fatalf("Create() error = %v", err)
			}
			writer, err := NewTransactionWriter(segment)
			if err != nil {
				t.Fatalf("NewTransactionWriter() error = %v", err)
			}
			receipt, err := writer.Commit(1, []Record{
				{Type: TypePageDelta, Payload: []byte("change")},
			})
			if err != nil {
				t.Fatalf("Commit() error = %v", err)
			}
			if err := segment.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}

			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile() error = %v", err)
			}
			commitOffset := int(receipt.CommitLSN)
			totalLength := int(binary.LittleEndian.Uint32(data[commitOffset+12 : commitOffset+16]))
			commit := data[commitOffset : commitOffset+totalLength]
			commit[recordHeaderSize+test.payloadOffset] ^= 0xff
			binary.LittleEndian.PutUint32(commit[52:56], checksum(commit, 52))
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}

			reopened, err := Open(path, 1, 0)
			if err != nil {
				t.Fatalf("Open() error = %v", err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			if _, err := ScanCommitted(reopened); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("ScanCommitted(tampered) error = %v, want ErrCorrupt", err)
			}
		})
	}
}

func TestScanCommittedRejectsInterleavedTransaction(t *testing.T) {
	t.Parallel()

	segment := createTestSegment(t)
	for _, record := range []Record{
		{Type: TypePageInit, TransactionID: 1},
		{Type: TypePageDelta, TransactionID: 2},
	} {
		if _, err := segment.Append(record); err != nil {
			t.Fatalf("Append() error = %v", err)
		}
	}
	if err := segment.Sync(); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if _, err := ScanCommitted(segment); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("ScanCommitted(interleaved) error = %v, want ErrCorrupt", err)
	}
}

func TestNewTransactionWriterRejectsUncommittedTail(t *testing.T) {
	t.Parallel()

	segment := createTestSegment(t)
	if _, err := segment.Append(Record{
		Type:          TypePageDelta,
		TransactionID: 99,
	}); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if err := segment.Sync(); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if _, err := NewTransactionWriter(segment); !errors.Is(err, ErrPoisoned) {
		t.Fatalf("NewTransactionWriter() error = %v, want ErrPoisoned", err)
	}
	if _, err := segment.Append(Record{Type: TypePageInit}); !errors.Is(err, ErrPoisoned) {
		t.Fatalf("Append(after tail) error = %v, want ErrPoisoned", err)
	}
}

func TestTransactionConcurrentCommitsDoNotInterleave(t *testing.T) {
	t.Parallel()

	segment := createTestSegment(t)
	writer, err := NewTransactionWriter(segment)
	if err != nil {
		t.Fatalf("NewTransactionWriter() error = %v", err)
	}
	const count = 32
	receipts := make(chan Receipt, count)
	var group sync.WaitGroup
	for id := 1; id <= count; id++ {
		group.Add(1)
		go func(id uint64) {
			defer group.Done()
			receipt, err := writer.Commit(id, []Record{
				{Type: TypePageInit, PageID: id},
				{Type: TypePageDelta, PageID: id},
			})
			if err != nil {
				t.Errorf("Commit(%d) error = %v", id, err)
				return
			}
			receipts <- receipt
		}(uint64(id))
	}
	group.Wait()
	close(receipts)

	ordered := make([]Receipt, 0, count)
	for receipt := range receipts {
		ordered = append(ordered, receipt)
	}
	sort.Slice(ordered, func(left, right int) bool {
		return ordered[left].FirstLSN < ordered[right].FirstLSN
	})
	for index := 1; index < len(ordered); index++ {
		if ordered[index].FirstLSN != ordered[index-1].DurableLSN {
			t.Fatalf("transaction %d starts at %d, previous ends %d",
				index, ordered[index].FirstLSN, ordered[index-1].DurableLSN)
		}
	}
	committed, err := ScanCommitted(segment)
	if err != nil {
		t.Fatalf("ScanCommitted() error = %v", err)
	}
	if len(committed) != count {
		t.Fatalf("committed transactions = %d, want %d", len(committed), count)
	}
}

func createTestSegment(t *testing.T) *Segment {
	t.Helper()
	segment, err := Create(filepath.Join(t.TempDir(), "wal.log"), 1, 0)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	t.Cleanup(func() { _ = segment.Close() })
	return segment
}

func assertCommitted(
	t *testing.T,
	got []CommittedTransaction,
	transactionID uint64,
	wantRecords []Record,
	wantReceipt Receipt,
) {
	t.Helper()
	if len(got) != 1 {
		t.Fatalf("committed transactions = %d, want 1", len(got))
	}
	if got[0].Receipt != wantReceipt {
		t.Fatalf("receipt = %#v, want %#v", got[0].Receipt, wantReceipt)
	}
	if got[0].Receipt.TransactionID != transactionID ||
		len(got[0].Records) != len(wantRecords) {
		t.Fatalf("transaction = %#v", got[0])
	}
	for index := range wantRecords {
		if got[0].Records[index].Type != wantRecords[index].Type ||
			got[0].Records[index].SpaceID != wantRecords[index].SpaceID ||
			got[0].Records[index].PageID != wantRecords[index].PageID ||
			string(got[0].Records[index].Payload) != string(wantRecords[index].Payload) {
			t.Fatalf("record[%d] = %#v, want %#v", index, got[0].Records[index], wantRecords[index])
		}
	}
}

type transactionFaultFile struct {
	segmentFile
	failWriteCall int
	writeCalls    int
	writeErr      error
	syncErr       error
}

func (file *transactionFaultFile) WriteAt(value []byte, offset int64) (int, error) {
	file.writeCalls++
	if file.writeCalls == file.failWriteCall {
		return 0, file.writeErr
	}
	return file.segmentFile.WriteAt(value, offset)
}

func (file *transactionFaultFile) Sync() error {
	if file.syncErr != nil {
		return file.syncErr
	}
	return file.segmentFile.Sync()
}
