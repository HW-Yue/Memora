package wal

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/store/page"
)

func TestRecoverCommittedRedoReopenAndRepeat(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	pagePath := filepath.Join(directory, "space.pages")
	walPath := filepath.Join(directory, "wal.log")
	manager, err := page.Create(pagePath, 7)
	if err != nil {
		t.Fatalf("page.Create() error = %v", err)
	}
	segment, err := Create(walPath, 1, 0)
	if err != nil {
		t.Fatalf("wal.Create() error = %v", err)
	}
	writer, err := NewTransactionWriter(segment)
	if err != nil {
		t.Fatalf("NewTransactionWriter() error = %v", err)
	}
	first, err := writer.Commit(1, []Record{{
		Type:    TypePageInit,
		SpaceID: 7,
		PageID:  1,
		Payload: mustPageImage(t, page.Page{
			Header:  page.Header{Type: page.TypeData, SpaceID: 7, PageID: 1, Generation: 3},
			Payload: []byte("hello"),
		}),
	}})
	if err != nil {
		t.Fatalf("Commit(init) error = %v", err)
	}
	delta, err := EncodePageDelta(1, []byte("A"))
	if err != nil {
		t.Fatalf("EncodePageDelta() error = %v", err)
	}
	second, err := writer.Commit(2, []Record{{
		Type: TypePageDelta, SpaceID: 7, PageID: 1, Payload: delta,
	}})
	if err != nil {
		t.Fatalf("Commit(delta) error = %v", err)
	}
	if _, err := segment.Append(Record{
		Type:          TypePageDelta,
		TransactionID: 99,
		SpaceID:       7,
		PageID:        1,
		Payload:       delta,
	}); err != nil {
		t.Fatalf("Append(uncommitted) error = %v", err)
	}
	if err := segment.Sync(); err != nil {
		t.Fatalf("Sync(uncommitted) error = %v", err)
	}
	if err := segment.Close(); err != nil {
		t.Fatalf("segment.Close() error = %v", err)
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("manager.Close() error = %v", err)
	}

	segment, err = Open(walPath, 1, 0)
	if err != nil {
		t.Fatalf("wal.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = segment.Close() })
	manager, err = page.Open(pagePath, 7)
	if err != nil {
		t.Fatalf("page.Open() error = %v", err)
	}
	report, err := Recover(segment, map[uint64]PageStore{7: manager})
	if err != nil {
		t.Fatalf("Recover() error = %v", err)
	}
	if report.Transactions != 2 || report.PageWrites != 2 ||
		report.SkippedRecords != 0 || report.LastCommitLSN != second.CommitLSN {
		t.Fatalf("Recover() report = %#v", report)
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("manager.Close(after recover) error = %v", err)
	}
	manager, err = page.Open(pagePath, 7)
	if err != nil {
		t.Fatalf("page.Open(after recover) error = %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	got, err := manager.Read(1)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if string(got.Payload) != "hAllo" || got.Header.LSN != second.FirstLSN {
		t.Fatalf("recovered Page = %#v", got)
	}
	if first.FirstLSN >= second.FirstLSN {
		t.Fatalf("transaction LSNs = %d then %d", first.FirstLSN, second.FirstLSN)
	}

	repeated, err := Recover(segment, map[uint64]PageStore{7: manager})
	if err != nil {
		t.Fatalf("Recover(repeat) error = %v", err)
	}
	if repeated.Transactions != 2 || repeated.PageWrites != 0 ||
		repeated.SkippedRecords != 2 {
		t.Fatalf("Recover(repeat) report = %#v", repeated)
	}
}

func TestRecoverFullPageImageRepairsTornPage(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	pagePath := filepath.Join(directory, "space.pages")
	manager, err := page.Create(pagePath, 7)
	if err != nil {
		t.Fatalf("page.Create() error = %v", err)
	}
	if err := manager.Write(page.Page{
		Header:  page.Header{Type: page.TypeData, SpaceID: 7, PageID: 1, LSN: 1},
		Payload: []byte("before"),
	}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := manager.Sync(); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	file, err := os.OpenFile(pagePath, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("OpenFile() error = %v", err)
	}
	if _, err := file.WriteAt([]byte{0xff}, int64(page.Size+page.HeaderSize)); err != nil {
		t.Fatalf("WriteAt(corrupt) error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("file.Close() error = %v", err)
	}
	manager, err = page.Open(pagePath, 7)
	if err != nil {
		t.Fatalf("page.Open(corrupt data Page) error = %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if _, err := manager.Read(1); !errors.Is(err, page.ErrCorrupt) {
		t.Fatalf("Read(corrupt) error = %v, want page.ErrCorrupt", err)
	}

	segment := createTestSegment(t)
	writer, err := NewTransactionWriter(segment)
	if err != nil {
		t.Fatalf("NewTransactionWriter() error = %v", err)
	}
	receipt, err := writer.Commit(1, []Record{{
		Type:    TypeFullPageImage,
		SpaceID: 7,
		PageID:  1,
		Payload: mustPageImage(t, page.Page{
			Header:  page.Header{Type: page.TypeData, SpaceID: 7, PageID: 1},
			Payload: []byte("repaired"),
		}),
	}})
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if _, err := Recover(segment, map[uint64]PageStore{7: manager}); err != nil {
		t.Fatalf("Recover() error = %v", err)
	}
	got, err := manager.Read(1)
	if err != nil {
		t.Fatalf("Read(repaired) error = %v", err)
	}
	if string(got.Payload) != "repaired" || got.Header.LSN != receipt.FirstLSN {
		t.Fatalf("repaired Page = %#v", got)
	}
}

func TestRecoverValidatesWholeTransactionBeforeWrite(t *testing.T) {
	t.Parallel()

	segment := createTestSegment(t)
	writer, err := NewTransactionWriter(segment)
	if err != nil {
		t.Fatalf("NewTransactionWriter() error = %v", err)
	}
	delta, err := EncodePageDelta(0, []byte("x"))
	if err != nil {
		t.Fatalf("EncodePageDelta() error = %v", err)
	}
	if _, err := writer.Commit(1, []Record{
		{
			Type: TypePageInit, SpaceID: 7, PageID: 1,
			Payload: mustPageImage(t, page.Page{
				Header: page.Header{Type: page.TypeData, SpaceID: 7, PageID: 1},
			}),
		},
		{Type: TypePageDelta, SpaceID: 7, PageID: 2, Payload: delta},
	}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	store := newFakePageStore()
	if _, err := Recover(segment, map[uint64]PageStore{7: store}); !errors.Is(err, ErrNeedsPageImage) {
		t.Fatalf("Recover() error = %v, want ErrNeedsPageImage", err)
	}
	if store.writeCalls != 0 {
		t.Fatalf("Page writes = %d before validation completed", store.writeCalls)
	}
}

func TestRecoverStagesMultipleChangesToSamePage(t *testing.T) {
	t.Parallel()

	segment := createTestSegment(t)
	writer, err := NewTransactionWriter(segment)
	if err != nil {
		t.Fatalf("NewTransactionWriter() error = %v", err)
	}
	delta, err := EncodePageDelta(1, []byte("Z"))
	if err != nil {
		t.Fatalf("EncodePageDelta() error = %v", err)
	}
	if _, err := writer.Commit(1, []Record{
		{
			Type: TypePageInit, SpaceID: 7, PageID: 1,
			Payload: mustPageImage(t, page.Page{
				Header:  page.Header{Type: page.TypeData, SpaceID: 7, PageID: 1},
				Payload: []byte("abc"),
			}),
		},
		{Type: TypePageDelta, SpaceID: 7, PageID: 1, Payload: delta},
	}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	store := newFakePageStore()
	if _, err := Recover(segment, map[uint64]PageStore{7: store}); err != nil {
		t.Fatalf("Recover() error = %v", err)
	}
	committed, err := ScanCommitted(segment)
	if err != nil {
		t.Fatalf("ScanCommitted() error = %v", err)
	}
	got := store.pages[1]
	if string(got.Payload) != "aZc" || got.Header.LSN != committed[0].Records[1].LSN {
		t.Fatalf("recovered Page = %#v", got)
	}
	if store.writeCalls != 1 {
		t.Fatalf("Page writes = %d, want one staged write", store.writeCalls)
	}
}

func TestRecoverRejectsDeltaOutsidePage(t *testing.T) {
	t.Parallel()

	if _, err := EncodePageDelta(0, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("EncodePageDelta(empty) error = %v, want ErrInvalid", err)
	}
	if _, err := EncodePageDelta(^uint32(0), []byte("x")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("EncodePageDelta(overflow) error = %v, want ErrInvalid", err)
	}
	delta, err := EncodePageDelta(2, []byte("zz"))
	if err != nil {
		t.Fatalf("EncodePageDelta() error = %v", err)
	}
	segment := createTestSegment(t)
	writer, err := NewTransactionWriter(segment)
	if err != nil {
		t.Fatalf("NewTransactionWriter() error = %v", err)
	}
	if _, err := writer.Commit(1, []Record{{
		Type: TypePageDelta, SpaceID: 7, PageID: 1, Payload: delta,
	}}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	store := fakeStoreWithPage(page.Page{
		Header:  page.Header{Type: page.TypeData, SpaceID: 7, PageID: 1},
		Payload: []byte("abc"),
	})
	if _, err := Recover(segment, map[uint64]PageStore{7: store}); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Recover() error = %v, want ErrCorrupt", err)
	}
	if store.writeCalls != 0 {
		t.Fatalf("Page writes = %d", store.writeCalls)
	}
}

func TestRecoverRejectsBadCommitBeforePageWrite(t *testing.T) {
	t.Parallel()

	segment := createTestSegment(t)
	firstLSN, err := segment.Append(Record{
		Type:          TypePageInit,
		TransactionID: 1,
		SpaceID:       7,
		PageID:        1,
		Payload: mustPageImage(t, page.Page{
			Header: page.Header{Type: page.TypeData, SpaceID: 7, PageID: 1},
		}),
	})
	if err != nil {
		t.Fatalf("Append(change) error = %v", err)
	}
	if _, err := segment.Append(Record{
		Type:          TypeCommit,
		TransactionID: 1,
		Payload:       encodeCommitPayload(1, firstLSN, [32]byte{}),
	}); err != nil {
		t.Fatalf("Append(commit) error = %v", err)
	}
	if err := segment.Sync(); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	store := newFakePageStore()
	if _, err := Recover(segment, map[uint64]PageStore{7: store}); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Recover() error = %v, want ErrCorrupt", err)
	}
	if store.writeCalls != 0 {
		t.Fatalf("Page writes = %d", store.writeCalls)
	}
}

func TestRecoverRejectsPhysicalWALCorruptionBeforePageWrite(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"bit flip", "truncate"} {
		name := name
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "wal.log")
			segment, err := Create(path, 1, 0)
			if err != nil {
				t.Fatalf("Create() error = %v", err)
			}
			t.Cleanup(func() { _ = segment.Close() })
			writer, err := NewTransactionWriter(segment)
			if err != nil {
				t.Fatalf("NewTransactionWriter() error = %v", err)
			}
			receipt, err := writer.Commit(1, []Record{{
				Type: TypePageInit, SpaceID: 7, PageID: 1,
				Payload: mustPageImage(t, page.Page{
					Header: page.Header{Type: page.TypeData, SpaceID: 7, PageID: 1},
				}),
			}})
			if err != nil {
				t.Fatalf("Commit() error = %v", err)
			}
			switch name {
			case "bit flip":
				file, err := os.OpenFile(path, os.O_RDWR, 0)
				if err != nil {
					t.Fatalf("OpenFile() error = %v", err)
				}
				if _, err := file.WriteAt(
					[]byte{0xff},
					int64(receipt.FirstLSN+recordHeaderSize),
				); err != nil {
					t.Fatalf("WriteAt() error = %v", err)
				}
				if err := file.Close(); err != nil {
					t.Fatalf("file.Close() error = %v", err)
				}
			case "truncate":
				info, err := os.Stat(path)
				if err != nil {
					t.Fatalf("Stat() error = %v", err)
				}
				if err := os.Truncate(path, info.Size()-1); err != nil {
					t.Fatalf("Truncate() error = %v", err)
				}
			}
			store := newFakePageStore()
			if _, err := Recover(segment, map[uint64]PageStore{7: store}); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("Recover() error = %v, want ErrCorrupt", err)
			}
			if store.writeCalls != 0 {
				t.Fatalf("Page writes = %d", store.writeCalls)
			}
		})
	}
}

func TestRecoverRejectsInvalidRedoBeforeWrite(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		record Record
		spaces map[uint64]PageStore
		want   error
	}{
		{
			name: "missing space",
			record: Record{
				Type: TypePageInit, SpaceID: 8, PageID: 1,
				Payload: mustPageImage(t, page.Page{
					Header: page.Header{Type: page.TypeData, SpaceID: 8, PageID: 1},
				}),
			},
			spaces: map[uint64]PageStore{7: newFakePageStore()},
			want:   ErrMissingSpace,
		},
		{
			name:   "unsupported root",
			record: Record{Type: TypeRoot, SpaceID: 7, PageID: 1},
			spaces: map[uint64]PageStore{7: newFakePageStore()},
			want:   ErrUnsupportedRedo,
		},
		{
			name: "wrong image identity",
			record: Record{
				Type: TypeFullPageImage, SpaceID: 7, PageID: 1,
				Payload: mustPageImage(t, page.Page{
					Header: page.Header{Type: page.TypeData, SpaceID: 7, PageID: 2},
				}),
			},
			spaces: map[uint64]PageStore{7: newFakePageStore()},
			want:   ErrCorrupt,
		},
		{
			name:   "invalid delta",
			record: Record{Type: TypePageDelta, SpaceID: 7, PageID: 1, Payload: []byte("bad")},
			spaces: map[uint64]PageStore{7: fakeStoreWithPage(page.Page{
				Header:  page.Header{Type: page.TypeData, SpaceID: 7, PageID: 1},
				Payload: []byte("old"),
			})},
			want: ErrCorrupt,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			segment := createTestSegment(t)
			writer, err := NewTransactionWriter(segment)
			if err != nil {
				t.Fatalf("NewTransactionWriter() error = %v", err)
			}
			if _, err := writer.Commit(1, []Record{test.record}); err != nil {
				t.Fatalf("Commit() error = %v", err)
			}
			if _, err := Recover(segment, test.spaces); !errors.Is(err, test.want) {
				t.Fatalf("Recover() error = %v, want %v", err, test.want)
			}
			for _, value := range test.spaces {
				if store, ok := value.(*fakePageStore); ok && store.writeCalls != 0 {
					t.Fatalf("Page writes = %d", store.writeCalls)
				}
			}
		})
	}
}

func TestRecoverWriteAndSyncFaultsConvergeOnRetry(t *testing.T) {
	t.Parallel()

	segment := createTestSegment(t)
	writer, err := NewTransactionWriter(segment)
	if err != nil {
		t.Fatalf("NewTransactionWriter() error = %v", err)
	}
	records := []Record{
		{
			Type: TypePageInit, SpaceID: 7, PageID: 1,
			Payload: mustPageImage(t, page.Page{
				Header: page.Header{Type: page.TypeData, SpaceID: 7, PageID: 1},
			}),
		},
		{
			Type: TypePageInit, SpaceID: 7, PageID: 2,
			Payload: mustPageImage(t, page.Page{
				Header: page.Header{Type: page.TypeData, SpaceID: 7, PageID: 2},
			}),
		},
	}
	if _, err := writer.Commit(1, records); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	for failWriteCall := 1; failWriteCall <= len(records); failWriteCall++ {
		store := newFakePageStore()
		injected := errors.New("injected Page write failure")
		store.failWriteCall = failWriteCall
		store.writeErr = injected
		if _, err := Recover(segment, map[uint64]PageStore{7: store}); !errors.Is(err, injected) {
			t.Fatalf("Recover(write %d) error = %v, want injected", failWriteCall, err)
		}
		store.failWriteCall = 0
		store.writeErr = nil
		if _, err := Recover(segment, map[uint64]PageStore{7: store}); err != nil {
			t.Fatalf("Recover(retry write %d) error = %v", failWriteCall, err)
		}
		if len(store.pages) != len(records) || store.syncCalls != 1 {
			t.Fatalf("retry state = pages %d, syncs %d", len(store.pages), store.syncCalls)
		}
	}

	store := newFakePageStore()
	injected := errors.New("injected Page sync failure")
	store.syncErr = injected
	if _, err := Recover(segment, map[uint64]PageStore{7: store}); !errors.Is(err, injected) {
		t.Fatalf("Recover(sync) error = %v, want injected", err)
	}
	store.syncErr = nil
	if report, err := Recover(segment, map[uint64]PageStore{7: store}); err != nil {
		t.Fatalf("Recover(sync retry) error = %v", err)
	} else if report.PageWrites != 0 || report.SkippedRecords != 2 {
		t.Fatalf("Recover(sync retry) report = %#v", report)
	}
	if store.syncCalls != 2 {
		t.Fatalf("Sync calls = %d, want 2", store.syncCalls)
	}
}

func TestRecoverSkipsNewerPageLSN(t *testing.T) {
	t.Parallel()

	segment := createTestSegment(t)
	writer, err := NewTransactionWriter(segment)
	if err != nil {
		t.Fatalf("NewTransactionWriter() error = %v", err)
	}
	if _, err := writer.Commit(1, []Record{{
		Type: TypePageInit, SpaceID: 7, PageID: 1,
		Payload: mustPageImage(t, page.Page{
			Header:  page.Header{Type: page.TypeData, SpaceID: 7, PageID: 1},
			Payload: []byte("old"),
		}),
	}}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	store := fakeStoreWithPage(page.Page{
		Header:  page.Header{Type: page.TypeData, SpaceID: 7, PageID: 1, LSN: 9999},
		Payload: []byte("new"),
	})
	report, err := Recover(segment, map[uint64]PageStore{7: store})
	if err != nil {
		t.Fatalf("Recover() error = %v", err)
	}
	if report.PageWrites != 0 || report.SkippedRecords != 1 || store.writeCalls != 0 {
		t.Fatalf("Recover() report = %#v, writes = %d", report, store.writeCalls)
	}
	if string(store.pages[1].Payload) != "new" {
		t.Fatalf("newer Page was replaced: %#v", store.pages[1])
	}
}

func mustPageImage(t *testing.T, value page.Page) []byte {
	t.Helper()
	encoded, err := page.Encode(value)
	if err != nil {
		t.Fatalf("page.Encode() error = %v", err)
	}
	return encoded
}

type fakePageStore struct {
	pages         map[uint64]page.Page
	corrupt       map[uint64]bool
	failWriteCall int
	writeCalls    int
	writeErr      error
	syncCalls     int
	syncErr       error
}

func newFakePageStore() *fakePageStore {
	return &fakePageStore{
		pages:   make(map[uint64]page.Page),
		corrupt: make(map[uint64]bool),
	}
}

func fakeStoreWithPage(value page.Page) *fakePageStore {
	store := newFakePageStore()
	store.pages[value.Header.PageID] = clonePage(value)
	return store
}

func (store *fakePageStore) Read(pageID uint64) (page.Page, error) {
	if store.corrupt[pageID] {
		return page.Page{}, page.ErrCorrupt
	}
	value, exists := store.pages[pageID]
	if !exists {
		return page.Page{}, page.ErrNotFound
	}
	return clonePage(value), nil
}

func (store *fakePageStore) Write(value page.Page) error {
	store.writeCalls++
	if store.writeCalls == store.failWriteCall {
		return store.writeErr
	}
	store.pages[value.Header.PageID] = clonePage(value)
	delete(store.corrupt, value.Header.PageID)
	return nil
}

func (store *fakePageStore) Sync() error {
	store.syncCalls++
	return store.syncErr
}

func clonePage(value page.Page) page.Page {
	cloned := value
	cloned.Payload = append([]byte(nil), value.Payload...)
	return cloned
}
