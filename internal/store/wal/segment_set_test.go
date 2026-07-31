package wal

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestSegmentSetCommitRollReopenAndScan(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "redo")
	set, err := CreateSegmentSet(path, 1000)
	if err != nil {
		t.Fatalf("CreateSegmentSet() error = %v", err)
	}
	first, err := set.Commit(1, []Record{{Type: TypePageInit, Payload: []byte("first")}})
	if err != nil {
		t.Fatalf("Commit(first) error = %v", err)
	}
	secondSegment, err := set.Roll()
	if err != nil {
		t.Fatalf("Roll() error = %v", err)
	}
	if secondSegment.ID != 2 || secondSegment.StartLSN != first.DurableLSN ||
		secondSegment.NextLSN != first.DurableLSN+segmentHeaderSize {
		t.Fatalf("Roll() Segment = %#v, first receipt = %#v", secondSegment, first)
	}
	second, err := set.Commit(2, []Record{{Type: TypePageDelta, Payload: []byte("second")}})
	if err != nil {
		t.Fatalf("Commit(second) error = %v", err)
	}
	if second.FirstLSN != secondSegment.NextLSN {
		t.Fatalf("second first LSN = %d, want %d", second.FirstLSN, secondSegment.NextLSN)
	}
	transactions, err := set.ScanCommitted()
	if err != nil {
		t.Fatalf("ScanCommitted() error = %v", err)
	}
	assertTransactionIDs(t, transactions, 1, 2)
	if err := set.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := OpenSegmentSet(path, 1000)
	if err != nil {
		t.Fatalf("OpenSegmentSet() error = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	state, err := reopened.State()
	if err != nil {
		t.Fatalf("State() error = %v", err)
	}
	if len(state) != 2 || state[0].ID != 1 || state[1].ID != 2 ||
		state[1].NextLSN != second.DurableLSN {
		t.Fatalf("State() = %#v", state)
	}
	transactions, err = reopened.ScanCommitted()
	if err != nil {
		t.Fatalf("ScanCommitted(reopen) error = %v", err)
	}
	assertTransactionIDs(t, transactions, 1, 2)
	if _, err := reopened.Commit(1, []Record{{Type: TypePageInit}}); !errors.Is(
		err,
		ErrDuplicateTransaction,
	) {
		t.Fatalf("Commit(reopen duplicate) error = %v", err)
	}
}

func TestSegmentSetRejectsEmptyAndUncommittedRoll(t *testing.T) {
	t.Parallel()

	set, err := CreateSegmentSet(filepath.Join(t.TempDir(), "redo"), 0)
	if err != nil {
		t.Fatalf("CreateSegmentSet() error = %v", err)
	}
	t.Cleanup(func() { _ = set.Close() })
	if _, err := set.Roll(); !errors.Is(err, ErrEmptySegment) {
		t.Fatalf("Roll(empty) error = %v, want ErrEmptySegment", err)
	}
	if _, err := set.Commit(1, []Record{{Type: TypePageInit}}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if _, err := set.Roll(); err != nil {
		t.Fatalf("Roll() error = %v", err)
	}
	if _, err := set.Roll(); !errors.Is(err, ErrEmptySegment) {
		t.Fatalf("Roll(second empty) error = %v, want ErrEmptySegment", err)
	}

	active := set.segments[len(set.segments)-1]
	if _, err := active.Append(Record{Type: TypePageDelta, TransactionID: 99}); err != nil {
		t.Fatalf("Append(uncommitted) error = %v", err)
	}
	if err := active.Sync(); err != nil {
		t.Fatalf("Sync(uncommitted) error = %v", err)
	}
	if _, err := set.Roll(); !errors.Is(err, ErrPoisoned) {
		t.Fatalf("Roll(uncommitted) error = %v, want ErrPoisoned", err)
	}
}

func TestOpenSegmentSetRejectsUncommittedTail(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "redo")
	set, err := CreateSegmentSet(path, 0)
	if err != nil {
		t.Fatalf("CreateSegmentSet() error = %v", err)
	}
	if _, err := set.Commit(1, []Record{{Type: TypePageInit}}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	active := set.segments[0]
	if _, err := active.Append(Record{Type: TypePageDelta, TransactionID: 99}); err != nil {
		t.Fatalf("Append(uncommitted) error = %v", err)
	}
	if err := active.Sync(); err != nil {
		t.Fatalf("Sync(uncommitted) error = %v", err)
	}
	if err := set.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if opened, err := OpenSegmentSet(path, 0); !errors.Is(err, ErrPoisoned) {
		if opened != nil {
			_ = opened.Close()
		}
		t.Fatalf("OpenSegmentSet() error = %v, want ErrPoisoned", err)
	}
}

func TestOpenSegmentSetRejectsDuplicateTransactionAcrossSegments(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "redo")
	set, err := CreateSegmentSet(path, 0)
	if err != nil {
		t.Fatalf("CreateSegmentSet() error = %v", err)
	}
	if _, err := set.Commit(1, []Record{{Type: TypePageInit}}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if _, err := set.Roll(); err != nil {
		t.Fatalf("Roll() error = %v", err)
	}
	rawWriter, err := NewTransactionWriter(set.segments[1])
	if err != nil {
		t.Fatalf("NewTransactionWriter() error = %v", err)
	}
	if _, err := rawWriter.Commit(1, []Record{{Type: TypePageDelta}}); err != nil {
		t.Fatalf("raw Commit(duplicate) error = %v", err)
	}
	if err := set.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if opened, err := OpenSegmentSet(path, 0); !errors.Is(err, ErrCorrupt) {
		if opened != nil {
			_ = opened.Close()
		}
		t.Fatalf("OpenSegmentSet() error = %v, want ErrCorrupt", err)
	}
}

func TestOpenSegmentSetRejectsInvalidLayout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(t *testing.T, directory string)
	}{
		{
			name: "unknown entry",
			mutate: func(t *testing.T, directory string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(directory, "notes"), []byte("x"), 0o600); err != nil {
					t.Fatalf("WriteFile() error = %v", err)
				}
			},
		},
		{
			name: "missing first Segment",
			mutate: func(t *testing.T, directory string) {
				t.Helper()
				if err := os.Remove(segmentPath(directory, 1)); err != nil {
					t.Fatalf("Remove() error = %v", err)
				}
			},
		},
		{
			name: "wrong header identity",
			mutate: func(t *testing.T, directory string) {
				t.Helper()
				file, err := os.OpenFile(segmentPath(directory, 1), os.O_RDWR, 0)
				if err != nil {
					t.Fatalf("OpenFile() error = %v", err)
				}
				if _, err := file.WriteAt(encodeSegmentHeader(2, 100), 0); err != nil {
					t.Fatalf("WriteAt() error = %v", err)
				}
				if err := file.Close(); err != nil {
					t.Fatalf("Close() error = %v", err)
				}
			},
		},
		{
			name: "wrong next start LSN",
			mutate: func(t *testing.T, directory string) {
				t.Helper()
				file, err := os.OpenFile(segmentPath(directory, 2), os.O_RDWR, 0)
				if err != nil {
					t.Fatalf("OpenFile() error = %v", err)
				}
				if _, err := file.WriteAt(encodeSegmentHeader(2, 0), 0); err != nil {
					t.Fatalf("WriteAt() error = %v", err)
				}
				if err := file.Close(); err != nil {
					t.Fatalf("Close() error = %v", err)
				}
			},
		},
		{
			name: "bit flip",
			mutate: func(t *testing.T, directory string) {
				t.Helper()
				file, err := os.OpenFile(segmentPath(directory, 1), os.O_RDWR, 0)
				if err != nil {
					t.Fatalf("OpenFile() error = %v", err)
				}
				if _, err := file.WriteAt([]byte{0xff}, segmentHeaderSize+recordHeaderSize); err != nil {
					t.Fatalf("WriteAt() error = %v", err)
				}
				if err := file.Close(); err != nil {
					t.Fatalf("Close() error = %v", err)
				}
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "redo")
			set, err := CreateSegmentSet(directory, 100)
			if err != nil {
				t.Fatalf("CreateSegmentSet() error = %v", err)
			}
			if _, err := set.Commit(1, []Record{{
				Type: TypePageInit, Payload: []byte("payload"),
			}}); err != nil {
				t.Fatalf("Commit() error = %v", err)
			}
			if _, err := set.Roll(); err != nil {
				t.Fatalf("Roll() error = %v", err)
			}
			if err := set.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
			test.mutate(t, directory)
			if opened, err := OpenSegmentSet(directory, 100); !errors.Is(err, ErrCorrupt) {
				if opened != nil {
					_ = opened.Close()
				}
				t.Fatalf("OpenSegmentSet() error = %v, want ErrCorrupt", err)
			}
		})
	}
}

func TestSegmentSetRollFailureKeepsActiveWritable(t *testing.T) {
	t.Parallel()

	set, err := CreateSegmentSet(filepath.Join(t.TempDir(), "redo"), 0)
	if err != nil {
		t.Fatalf("CreateSegmentSet() error = %v", err)
	}
	t.Cleanup(func() { _ = set.Close() })
	if _, err := set.Commit(1, []Record{{Type: TypePageInit}}); err != nil {
		t.Fatalf("Commit(first) error = %v", err)
	}
	injected := errors.New("injected Segment create failure")
	set.createSegment = func(string, uint64, uint64) (*Segment, error) {
		return nil, injected
	}
	if _, err := set.Roll(); !errors.Is(err, injected) {
		t.Fatalf("Roll() error = %v, want injected", err)
	}
	set.createSegment = Create
	if _, err := set.Commit(2, []Record{{Type: TypePageDelta}}); err != nil {
		t.Fatalf("Commit(after roll failure) error = %v", err)
	}
	if _, err := set.Roll(); err != nil {
		t.Fatalf("Roll(retry) error = %v", err)
	}
	transactions, err := set.ScanCommitted()
	if err != nil {
		t.Fatalf("ScanCommitted() error = %v", err)
	}
	assertTransactionIDs(t, transactions, 1, 2)
}

func TestSegmentSetConcurrentCommitAndRollRemainOrdered(t *testing.T) {
	t.Parallel()

	set, err := CreateSegmentSet(filepath.Join(t.TempDir(), "redo"), 0)
	if err != nil {
		t.Fatalf("CreateSegmentSet() error = %v", err)
	}
	t.Cleanup(func() { _ = set.Close() })
	const commits = 32
	start := make(chan struct{})
	var group sync.WaitGroup
	for id := 1; id <= commits; id++ {
		group.Add(1)
		go func(id uint64) {
			defer group.Done()
			<-start
			if _, err := set.Commit(id, []Record{{Type: TypePageInit, PageID: id}}); err != nil {
				t.Errorf("Commit(%d) error = %v", id, err)
			}
		}(uint64(id))
	}
	for index := 0; index < 8; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			if _, err := set.Roll(); err != nil && !errors.Is(err, ErrEmptySegment) {
				t.Errorf("Roll() error = %v", err)
			}
		}()
	}
	close(start)
	group.Wait()
	transactions, err := set.ScanCommitted()
	if err != nil {
		t.Fatalf("ScanCommitted() error = %v", err)
	}
	if len(transactions) != commits {
		t.Fatalf("transactions = %d, want %d", len(transactions), commits)
	}
	seen := make(map[uint64]bool)
	var previousCommitLSN uint64
	for _, transaction := range transactions {
		if seen[transaction.Receipt.TransactionID] {
			t.Fatalf("duplicate transaction %d", transaction.Receipt.TransactionID)
		}
		seen[transaction.Receipt.TransactionID] = true
		if previousCommitLSN >= transaction.Receipt.FirstLSN {
			t.Fatalf("WAL order regressed from %d to %d", previousCommitLSN, transaction.Receipt.FirstLSN)
		}
		previousCommitLSN = transaction.Receipt.CommitLSN
	}
}

func TestSegmentSetClosedState(t *testing.T) {
	t.Parallel()

	set, err := CreateSegmentSet(filepath.Join(t.TempDir(), "redo"), 0)
	if err != nil {
		t.Fatalf("CreateSegmentSet() error = %v", err)
	}
	if err := set.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := set.Close(); err != nil {
		t.Fatalf("Close(second) error = %v", err)
	}
	if _, err := set.Commit(1, []Record{{Type: TypePageInit}}); !errors.Is(
		err,
		ErrSegmentSetClosed,
	) {
		t.Fatalf("Commit(closed) error = %v", err)
	}
	if _, err := set.Roll(); !errors.Is(err, ErrSegmentSetClosed) {
		t.Fatalf("Roll(closed) error = %v", err)
	}
	if _, err := set.ScanCommitted(); !errors.Is(err, ErrSegmentSetClosed) {
		t.Fatalf("ScanCommitted(closed) error = %v", err)
	}
	if _, err := set.State(); !errors.Is(err, ErrSegmentSetClosed) {
		t.Fatalf("State(closed) error = %v", err)
	}
}

func assertTransactionIDs(t *testing.T, got []CommittedTransaction, want ...uint64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("transactions = %d, want %d", len(got), len(want))
	}
	for index, transactionID := range want {
		if got[index].Receipt.TransactionID != transactionID {
			t.Fatalf("transaction[%d] ID = %d, want %d",
				index, got[index].Receipt.TransactionID, transactionID)
		}
	}
}
