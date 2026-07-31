package wal

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/store/page"
)

func TestCheckpointPublishReopenAndRecoveryStart(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "redo")
	set, err := CreateSegmentSet(path, 0)
	if err != nil {
		t.Fatalf("CreateSegmentSet() error = %v", err)
	}
	initReceipt, err := set.Commit(1, []Record{{
		Type: TypePageInit, SpaceID: 7, PageID: 1,
		Payload: mustPageImage(t, page.Page{
			Header:  page.Header{Type: page.TypeData, SpaceID: 7, PageID: 1},
			Payload: []byte("abc"),
		}),
	}})
	if err != nil {
		t.Fatalf("Commit(init) error = %v", err)
	}
	if _, err := set.Roll(); err != nil {
		t.Fatalf("Roll() error = %v", err)
	}
	barrier := &recordingBarrier{}
	checkpoint, err := set.PublishCheckpoint(barrier)
	if err != nil {
		t.Fatalf("PublishCheckpoint() error = %v", err)
	}
	if checkpoint.ID != 1 || checkpoint.RecoveryLSN != initReceipt.DurableLSN ||
		checkpoint.CoveredSegmentID != 1 || checkpoint.RecordLSN < checkpoint.RecoveryLSN ||
		checkpoint.DurableLSN <= checkpoint.RecordLSN {
		t.Fatalf("checkpoint = %#v, init = %#v", checkpoint, initReceipt)
	}
	if len(barrier.calls) != 1 || barrier.calls[0] != initReceipt.DurableLSN {
		t.Fatalf("barrier calls = %#v", barrier.calls)
	}
	delta, err := EncodePageDelta(1, []byte("Z"))
	if err != nil {
		t.Fatalf("EncodePageDelta() error = %v", err)
	}
	deltaReceipt, err := set.Commit(2, []Record{{
		Type: TypePageDelta, SpaceID: 7, PageID: 1, Payload: delta,
	}})
	if err != nil {
		t.Fatalf("Commit(delta) error = %v", err)
	}
	store := fakeStoreWithPage(page.Page{
		Header: page.Header{
			Type: page.TypeData, SpaceID: 7, PageID: 1, LSN: initReceipt.FirstLSN,
		},
		Payload: []byte("abc"),
	})
	report, err := RecoverSegmentSet(set, map[uint64]PageStore{7: store})
	if err != nil {
		t.Fatalf("RecoverSegmentSet() error = %v", err)
	}
	if report.Transactions != 1 || report.LastCommitLSN != deltaReceipt.CommitLSN ||
		string(store.pages[1].Payload) != "aZc" {
		t.Fatalf("recovery report = %#v, Page = %#v", report, store.pages[1])
	}
	if err := set.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	reopened, err := OpenSegmentSet(path, 0)
	if err != nil {
		t.Fatalf("OpenSegmentSet() error = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	got, exists, err := reopened.LatestCheckpoint()
	if err != nil {
		t.Fatalf("LatestCheckpoint() error = %v", err)
	}
	if !exists || got != checkpoint {
		t.Fatalf("LatestCheckpoint() = %#v, %t; want %#v", got, exists, checkpoint)
	}
	state, err := reopened.State()
	if err != nil {
		t.Fatalf("State() error = %v", err)
	}
	if len(state) != 2 {
		t.Fatalf("Segments after checkpoint = %d, want no deletion", len(state))
	}
}

func TestCheckpointPublishFaultsDoNotAdvanceLatest(t *testing.T) {
	t.Parallel()

	t.Run("barrier", func(t *testing.T) {
		set := checkpointTestSet(t)
		before := activeNextLSN(t, set)
		injected := errors.New("injected barrier failure")
		if _, err := set.PublishCheckpoint(&recordingBarrier{err: injected}); !errors.Is(
			err,
			injected,
		) {
			t.Fatalf("PublishCheckpoint() error = %v", err)
		}
		if after := activeNextLSN(t, set); after != before {
			t.Fatalf("Next LSN advanced from %d to %d", before, after)
		}
		assertNoCheckpoint(t, set)
	})

	for _, name := range []string{"write", "sync"} {
		name := name
		t.Run(name, func(t *testing.T) {
			set := checkpointTestSet(t)
			active := set.segments[len(set.segments)-1]
			injected := errors.New("injected checkpoint " + name + " failure")
			fault := &transactionFaultFile{segmentFile: active.file}
			if name == "write" {
				fault.failWriteCall = 1
				fault.writeErr = injected
			} else {
				fault.syncErr = injected
			}
			active.file = fault
			if _, err := set.PublishCheckpoint(&recordingBarrier{}); !errors.Is(err, injected) {
				t.Fatalf("PublishCheckpoint() error = %v, want injected", err)
			}
			assertNoCheckpoint(t, set)
			if _, err := set.Commit(2, []Record{{Type: TypePageInit}}); !errors.Is(
				err,
				ErrPoisoned,
			) {
				t.Fatalf("Commit(after fault) error = %v, want ErrPoisoned", err)
			}
		})
	}
}

func TestCheckpointRejectsNoProgress(t *testing.T) {
	t.Parallel()

	set := checkpointTestSet(t)
	t.Cleanup(func() { _ = set.Close() })
	if _, err := set.PublishCheckpoint(&recordingBarrier{}); err != nil {
		t.Fatalf("PublishCheckpoint() error = %v", err)
	}
	if _, err := set.PublishCheckpoint(&recordingBarrier{}); !errors.Is(
		err,
		ErrNoCheckpointProgress,
	) {
		t.Fatalf("PublishCheckpoint(no progress) error = %v", err)
	}
}

func TestOpenSegmentSetRejectsForgedCheckpoint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		payloadOffset int
	}{
		{name: "checkpoint ID", payloadOffset: 8},
		{name: "recovery LSN", payloadOffset: 16},
		{name: "covered Segment", payloadOffset: 24},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "redo")
			set := checkpointTestSetAt(t, directory)
			checkpoint, err := set.PublishCheckpoint(&recordingBarrier{})
			if err != nil {
				t.Fatalf("PublishCheckpoint() error = %v", err)
			}
			if err := set.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
			path := segmentPath(directory, 1)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile() error = %v", err)
			}
			offset := int(checkpoint.RecordLSN)
			totalLength := int(binary.LittleEndian.Uint32(data[offset+12 : offset+16]))
			record := data[offset : offset+totalLength]
			record[recordHeaderSize+test.payloadOffset] ^= 0xff
			binary.LittleEndian.PutUint32(record[52:56], checksum(record, 52))
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
			if opened, err := OpenSegmentSet(directory, 0); !errors.Is(err, ErrCorrupt) {
				if opened != nil {
					_ = opened.Close()
				}
				t.Fatalf("OpenSegmentSet() error = %v, want ErrCorrupt", err)
			}
		})
	}
}

func checkpointTestSet(t *testing.T) *SegmentSet {
	t.Helper()
	return checkpointTestSetAt(t, filepath.Join(t.TempDir(), "redo"))
}

func checkpointTestSetAt(t *testing.T, path string) *SegmentSet {
	t.Helper()
	set, err := CreateSegmentSet(path, 0)
	if err != nil {
		t.Fatalf("CreateSegmentSet() error = %v", err)
	}
	t.Cleanup(func() { _ = set.Close() })
	if _, err := set.Commit(1, []Record{{Type: TypePageInit}}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	return set
}

func activeNextLSN(t *testing.T, set *SegmentSet) uint64 {
	t.Helper()
	value, err := set.segments[len(set.segments)-1].NextLSN()
	if err != nil {
		t.Fatalf("NextLSN() error = %v", err)
	}
	return value
}

func assertNoCheckpoint(t *testing.T, set *SegmentSet) {
	t.Helper()
	if value, exists, err := set.LatestCheckpoint(); err != nil {
		t.Fatalf("LatestCheckpoint() error = %v", err)
	} else if exists {
		t.Fatalf("LatestCheckpoint() = %#v, want none", value)
	}
}

type recordingBarrier struct {
	calls []uint64
	err   error
}

func (barrier *recordingBarrier) FlushThrough(recoveryLSN uint64) error {
	barrier.calls = append(barrier.calls, recoveryLSN)
	return barrier.err
}
