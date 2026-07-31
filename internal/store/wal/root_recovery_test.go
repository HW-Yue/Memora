package wal

import (
	"encoding/binary"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/HW-Yue/Memora/internal/store/page"
	"github.com/HW-Yue/Memora/internal/store/treecontrol"
)

func TestRecoverBootstrapsCommittedRootAndAllocator(t *testing.T) {
	segment := createTestSegment(t)
	writer, err := NewTransactionWriter(segment)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Commit(1, []Record{
		{
			Type: TypePageInit, SpaceID: 7, PageID: 2,
			Payload: mustPageImage(t, page.Page{
				Header: page.Header{
					Type: page.TypeBTreeLeaf, SpaceID: 7, PageID: 2, Generation: 1,
				},
			}),
		},
		{
			Type: TypeAllocator, SpaceID: 7, PageID: 1,
			Payload: testAllocatorRedo(0, 2, 3),
		},
		{
			Type: TypeRoot, SpaceID: 7, PageID: 1,
			Payload: testRootRedo(0, 1, 0, 2, 2),
		},
	}); err != nil {
		t.Fatal(err)
	}

	store := newFakePageStore()
	report, err := Recover(segment, map[uint64]PageStore{7: store})
	if err != nil {
		t.Fatalf("Recover() error = %v", err)
	}
	if report.Transactions != 1 || report.PageWrites != 1 ||
		report.ControlWrites != 1 || report.BootstrapWrites != 1 ||
		store.writeCalls != 3 || store.syncCalls != 3 {
		t.Fatalf("Recover() report = %#v, writes=%d syncs=%d",
			report, store.writeCalls, store.syncCalls)
	}
	if _, exists := store.pages[1]; !exists {
		t.Fatal("Tree control Page was not written")
	}
	if root, exists := store.pages[2]; !exists ||
		root.Header.Type != page.TypeBTreeLeaf {
		t.Fatalf("root Page = %#v, exists=%t", root, exists)
	}
	if !reflect.DeepEqual(store.writeOrder, []uint64{1, 2, 1}) {
		t.Fatalf("write order = %v, want bootstrap, root, control", store.writeOrder)
	}
	assertTreeControl(t, store, treecontrol.State{
		SpaceID: 7, Generation: 1, RootPageID: 2, NextPageID: 3,
		LSN: committedRootLSN(t, segment, 0),
	})
}

func TestRecoverRootGrowReplayAndHigherGeneration(t *testing.T) {
	segment := createTestSegment(t)
	writer, err := NewTransactionWriter(segment)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Commit(1, bootstrapTreeRecords(t, 7)); err != nil {
		t.Fatal(err)
	}
	store := newFakePageStore()
	if _, err := Recover(segment, map[uint64]PageStore{7: store}); err != nil {
		t.Fatal(err)
	}

	if _, err := writer.Commit(2, []Record{
		{
			Type: TypePageInit, SpaceID: 7, PageID: 3,
			Payload: mustPageImage(t, page.Page{Header: page.Header{
				Type: page.TypeBTreeInternal, SpaceID: 7, PageID: 3, Generation: 2,
			}}),
		},
		{
			Type: TypeAllocator, SpaceID: 7, PageID: 1,
			Payload: mustAllocatorRedo(t, AllocatorRedo{
				ExpectedGeneration: 1, ExpectedNextPageID: 3, NextPageID: 4,
			}),
		},
		{
			Type: TypeRoot, SpaceID: 7, PageID: 1,
			Payload: mustRootRedo(t, RootRedo{
				ExpectedGeneration: 1, Generation: 2,
				ExpectedRootPageID: 2, RootPageID: 3, ExpectedNextPageID: 3,
			}),
		},
	}); err != nil {
		t.Fatal(err)
	}
	store.writeCalls, store.syncCalls = 0, 0
	store.writeOrder = nil
	if _, err := Recover(segment, map[uint64]PageStore{7: store}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(store.writeOrder, []uint64{3, 1}) {
		t.Fatalf("grow write order = %v", store.writeOrder)
	}
	assertTreeControl(t, store, treecontrol.State{
		SpaceID: 7, Generation: 2, RootPageID: 3, NextPageID: 4,
		LSN: committedRootLSN(t, segment, 1),
	})

	store.writeCalls, store.syncCalls = 0, 0
	store.writeOrder = nil
	report, err := Recover(segment, map[uint64]PageStore{7: store})
	if err != nil {
		t.Fatal(err)
	}
	if report.PageWrites != 0 || report.ControlWrites != 0 ||
		report.BootstrapWrites != 0 || report.SkippedRecords != 6 ||
		store.writeCalls != 0 {
		t.Fatalf("replay report = %#v, writes=%d", report, store.writeCalls)
	}
}

func TestRecoverRootShrinkRetiresPage(t *testing.T) {
	control, err := treecontrol.Encode(treecontrol.State{
		SpaceID: 7, Generation: 2, RootPageID: 3, NextPageID: 4, LSN: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	store := newFakePageStore()
	store.pages[1] = control
	store.pages[2] = page.Page{Header: page.Header{
		Type: page.TypeBTreeLeaf, SpaceID: 7, PageID: 2, Generation: 2, LSN: 1,
	}}
	store.pages[3] = page.Page{Header: page.Header{
		Type: page.TypeBTreeInternal, SpaceID: 7, PageID: 3, Generation: 2, LSN: 2,
	}}
	segment := createTestSegment(t)
	writer, err := NewTransactionWriter(segment)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Commit(3, []Record{
		{
			Type: TypeFullPageImage, SpaceID: 7, PageID: 3,
			Payload: mustPageImage(t, page.Page{Header: page.Header{
				Type: page.TypeFree, SpaceID: 7, PageID: 3, Generation: 3,
			}}),
		},
		{
			Type: TypeAllocator, SpaceID: 7, PageID: 1,
			Payload: mustAllocatorRedo(t, AllocatorRedo{
				ExpectedGeneration: 2, ExpectedNextPageID: 4, NextPageID: 4,
				RetiredPageIDs: []uint64{3},
			}),
		},
		{
			Type: TypeRoot, SpaceID: 7, PageID: 1,
			Payload: mustRootRedo(t, RootRedo{
				ExpectedGeneration: 2, Generation: 3,
				ExpectedRootPageID: 3, RootPageID: 2, ExpectedNextPageID: 4,
			}),
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := Recover(segment, map[uint64]PageStore{7: store}); err != nil {
		t.Fatal(err)
	}
	if store.pages[3].Header.Type != page.TypeFree ||
		!reflect.DeepEqual(store.writeOrder, []uint64{3, 1}) {
		t.Fatalf("shrink state Page3=%#v order=%v", store.pages[3], store.writeOrder)
	}
	assertTreeControl(t, store, treecontrol.State{
		SpaceID: 7, Generation: 3, RootPageID: 2, NextPageID: 4,
		LSN: committedRootLSN(t, segment, 0),
	})
}

func TestRecoverRejectsInvalidTreeMetadataBeforeWrite(t *testing.T) {
	validControl, err := treecontrol.Encode(treecontrol.State{
		SpaceID: 7, Generation: 1, RootPageID: 2, NextPageID: 3, LSN: 99,
	})
	if err != nil {
		t.Fatal(err)
	}
	existingStore := func() *fakePageStore {
		store := newFakePageStore()
		store.pages[1] = clonePage(validControl)
		store.pages[2] = page.Page{Header: page.Header{
			Type: page.TypeBTreeLeaf, SpaceID: 7, PageID: 2, LSN: 90,
		}}
		return store
	}
	tests := []struct {
		name    string
		records func(t *testing.T) []Record
		store   func() *fakePageStore
		want    error
	}{
		{
			name: "missing allocated Page init", store: newFakePageStore,
			records: func(t *testing.T) []Record {
				return bootstrapTreeMetadata(t, 7)
			},
			want: ErrCorrupt,
		},
		{
			name: "retired Page is not free", store: existingStore,
			records: func(t *testing.T) []Record {
				return []Record{
					{Type: TypeAllocator, SpaceID: 7, PageID: 1, Payload: mustAllocatorRedo(t, AllocatorRedo{
						ExpectedGeneration: 1, ExpectedNextPageID: 3, NextPageID: 3,
						RetiredPageIDs: []uint64{2},
					})},
					{Type: TypeRoot, SpaceID: 7, PageID: 1, Payload: mustRootRedo(t, RootRedo{
						ExpectedGeneration: 1, Generation: 2,
						ExpectedRootPageID: 2, RootPageID: 2, ExpectedNextPageID: 3,
					})},
				}
			},
			want: ErrCorrupt,
		},
		{
			name: "root exceeds high-water", store: newFakePageStore,
			records: func(t *testing.T) []Record {
				records := bootstrapTreeRecords(t, 7)
				records[len(records)-1].Payload = mustRootRedo(t, RootRedo{
					ExpectedGeneration: 0, Generation: 1,
					ExpectedRootPageID: 0, RootPageID: 3, ExpectedNextPageID: 2,
				})
				return records
			},
			want: ErrCorrupt,
		},
		{
			name: "allocator after root", store: newFakePageStore,
			records: func(t *testing.T) []Record {
				records := bootstrapTreeRecords(t, 7)
				records[1], records[2] = records[2], records[1]
				return records
			},
			want: ErrCorrupt,
		},
		{
			name: "allocator root generation mismatch", store: newFakePageStore,
			records: func(t *testing.T) []Record {
				records := bootstrapTreeRecords(t, 7)
				records[1].Payload = mustAllocatorRedo(t, AllocatorRedo{
					ExpectedGeneration: 1, ExpectedNextPageID: 2, NextPageID: 3,
				})
				return records
			},
			want: ErrCorrupt,
		},
		{
			name: "committed root is retired", store: existingStore,
			records: func(t *testing.T) []Record {
				return []Record{
					{
						Type: TypeFullPageImage, SpaceID: 7, PageID: 2,
						Payload: mustPageImage(t, page.Page{Header: page.Header{
							Type: page.TypeFree, SpaceID: 7, PageID: 2,
						}}),
					},
					{Type: TypeAllocator, SpaceID: 7, PageID: 1, Payload: mustAllocatorRedo(t, AllocatorRedo{
						ExpectedGeneration: 1, ExpectedNextPageID: 3, NextPageID: 3,
						RetiredPageIDs: []uint64{2},
					})},
					{Type: TypeRoot, SpaceID: 7, PageID: 1, Payload: mustRootRedo(t, RootRedo{
						ExpectedGeneration: 1, Generation: 2,
						ExpectedRootPageID: 2, RootPageID: 2, ExpectedNextPageID: 3,
					})},
				}
			},
			want: ErrCorrupt,
		},
		{
			name: "generation precondition", store: existingStore,
			records: func(t *testing.T) []Record {
				return []Record{{Type: TypeRoot, SpaceID: 7, PageID: 1, Payload: mustRootRedo(t, RootRedo{
					ExpectedGeneration: 2, Generation: 3,
					ExpectedRootPageID: 2, RootPageID: 2, ExpectedNextPageID: 3,
				})}}
			},
			want: ErrCorrupt,
		},
		{
			name: "same generation different LSN",
			store: func() *fakePageStore {
				store := existingStore()
				control, err := treecontrol.Encode(treecontrol.State{
					SpaceID: 7, Generation: 2, RootPageID: 2,
					NextPageID: 3, LSN: 999,
				})
				if err != nil {
					panic(err)
				}
				store.pages[1] = control
				return store
			},
			records: func(t *testing.T) []Record {
				return []Record{{Type: TypeRoot, SpaceID: 7, PageID: 1, Payload: mustRootRedo(t, RootRedo{
					ExpectedGeneration: 1, Generation: 2,
					ExpectedRootPageID: 2, RootPageID: 2, ExpectedNextPageID: 3,
				})}}
			},
			want: ErrCorrupt,
		},
		{
			name: "ordinary Tree control Page redo", store: existingStore,
			records: func(t *testing.T) []Record {
				control, err := treecontrol.Encode(treecontrol.State{
					SpaceID: 7, Generation: 2, RootPageID: 2,
					NextPageID: 3, LSN: 1,
				})
				if err != nil {
					t.Fatal(err)
				}
				control.Header.LSN = 0
				return []Record{{
					Type: TypeFullPageImage, SpaceID: 7, PageID: 1,
					Payload: mustPageImage(t, control),
				}}
			},
			want: ErrCorrupt,
		},
		{
			name: "bad control identity",
			store: func() *fakePageStore {
				store := existingStore()
				store.pages[1] = page.Page{Header: page.Header{
					Type: page.TypeData, SpaceID: 7, PageID: 1,
				}}
				return store
			},
			records: func(t *testing.T) []Record {
				return []Record{{Type: TypeRoot, SpaceID: 7, PageID: 1, Payload: mustRootRedo(t, RootRedo{
					ExpectedGeneration: 1, Generation: 2,
					ExpectedRootPageID: 2, RootPageID: 2, ExpectedNextPageID: 3,
				})}}
			},
			want: ErrCorrupt,
		},
		{
			name: "unknown root version", store: existingStore,
			records: func(t *testing.T) []Record {
				payload := mustRootRedo(t, RootRedo{
					ExpectedGeneration: 1, Generation: 2,
					ExpectedRootPageID: 2, RootPageID: 2, ExpectedNextPageID: 3,
				})
				binary.LittleEndian.PutUint16(payload[4:6], treeRedoVersion+1)
				return []Record{{Type: TypeRoot, SpaceID: 7, PageID: 1, Payload: payload}}
			},
			want: ErrUnsupportedRedo,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			segment := createTestSegment(t)
			writer, err := NewTransactionWriter(segment)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := writer.Commit(1, test.records(t)); err != nil {
				t.Fatal(err)
			}
			store := test.store()
			if _, err := Recover(segment, map[uint64]PageStore{7: store}); !errors.Is(err, test.want) {
				t.Fatalf("Recover() error = %v, want %v", err, test.want)
			}
			if store.writeCalls != 0 || store.syncCalls != 0 {
				t.Fatalf("validation wrote Pages: writes=%d syncs=%d",
					store.writeCalls, store.syncCalls)
			}
		})
	}
}

func TestRecoverTreeMetadataFaultsConverge(t *testing.T) {
	tests := []struct {
		name          string
		failWriteCall int
		failSyncCall  int
	}{
		{name: "bootstrap write", failWriteCall: 1},
		{name: "bootstrap Sync", failSyncCall: 1},
		{name: "root Page write", failWriteCall: 2},
		{name: "pre-control Sync", failSyncCall: 2},
		{name: "control-last write", failWriteCall: 3},
		{name: "control-last Sync", failSyncCall: 3},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			segment := createTestSegment(t)
			writer, err := NewTransactionWriter(segment)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := writer.Commit(1, bootstrapTreeRecords(t, 7)); err != nil {
				t.Fatal(err)
			}
			store := newFakePageStore()
			injected := errors.New("injected " + test.name)
			store.failWriteCall = test.failWriteCall
			store.failSyncCall = test.failSyncCall
			store.writeErr = injected
			store.syncErr = injected
			if _, err := Recover(segment, map[uint64]PageStore{7: store}); !errors.Is(err, injected) {
				t.Fatalf("Recover() error = %v, want injected", err)
			}

			store.failWriteCall, store.failSyncCall = 0, 0
			store.writeErr, store.syncErr = nil, nil
			store.writeCalls, store.syncCalls = 0, 0
			store.writeOrder = nil
			if _, err := Recover(segment, map[uint64]PageStore{7: store}); err != nil {
				t.Fatalf("Recover(retry) error = %v", err)
			}
			assertTreeControl(t, store, treecontrol.State{
				SpaceID: 7, Generation: 1, RootPageID: 2, NextPageID: 3,
				LSN: committedRootLSN(t, segment, 0),
			})
			if root := store.pages[2]; root.Header.Type != page.TypeBTreeLeaf {
				t.Fatalf("root after retry = %#v", root)
			}
		})
	}

	t.Run("root-only final Sync retry", func(t *testing.T) {
		bootstrapSegment := createTestSegment(t)
		bootstrapWriter, err := NewTransactionWriter(bootstrapSegment)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := bootstrapWriter.Commit(1, bootstrapTreeRecords(t, 7)); err != nil {
			t.Fatal(err)
		}
		store := newFakePageStore()
		if _, err := Recover(bootstrapSegment, map[uint64]PageStore{7: store}); err != nil {
			t.Fatal(err)
		}

		segment := createTestSegment(t)
		writer, err := NewTransactionWriter(segment)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Commit(2, []Record{{
			Type: TypeRoot, SpaceID: 7, PageID: 1,
			Payload: mustRootRedo(t, RootRedo{
				ExpectedGeneration: 1, Generation: 2,
				ExpectedRootPageID: 2, RootPageID: 2, ExpectedNextPageID: 3,
			}),
		}}); err != nil {
			t.Fatal(err)
		}
		injected := errors.New("injected root-only Sync")
		store.writeCalls, store.syncCalls = 0, 0
		store.failSyncCall, store.syncErr = 2, injected
		if _, err := Recover(segment, map[uint64]PageStore{7: store}); !errors.Is(err, injected) {
			t.Fatalf("Recover() error = %v, want injected", err)
		}
		store.failSyncCall, store.syncErr = 0, nil
		store.writeCalls, store.syncCalls = 0, 0
		report, err := Recover(segment, map[uint64]PageStore{7: store})
		if err != nil {
			t.Fatal(err)
		}
		if report.ControlWrites != 0 || report.SkippedRecords != 1 ||
			store.writeCalls != 0 || store.syncCalls != 1 {
			t.Fatalf("root-only retry report=%#v writes=%d syncs=%d",
				report, store.writeCalls, store.syncCalls)
		}
	})
}

func TestRecoverSegmentSetReopensTreeControlAfterCheckpoint(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "redo")
	set, err := CreateSegmentSet(directory, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := set.Commit(1, bootstrapTreeRecords(t, 7)); err != nil {
		t.Fatal(err)
	}
	pagePath := filepath.Join(t.TempDir(), "tree.pages")
	manager, err := page.Create(pagePath, 7)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RecoverSegmentSet(set, map[uint64]PageStore{7: manager}); err != nil {
		t.Fatal(err)
	}
	if _, err := set.PublishCheckpoint(&recordingBarrier{}); err != nil {
		t.Fatal(err)
	}
	if _, err := set.Commit(2, []Record{{
		Type: TypeRoot, SpaceID: 7, PageID: 1,
		Payload: mustRootRedo(t, RootRedo{
			ExpectedGeneration: 1, Generation: 2,
			ExpectedRootPageID: 2, RootPageID: 2, ExpectedNextPageID: 3,
		}),
	}}); err != nil {
		t.Fatal(err)
	}
	if err := set.Close(); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}

	reopenedSet, err := OpenSegmentSet(directory, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopenedSet.Close() }()
	reopenedManager, err := page.Open(pagePath, 7)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopenedManager.Close() }()
	report, err := RecoverSegmentSet(
		reopenedSet,
		map[uint64]PageStore{7: reopenedManager},
	)
	if err != nil {
		t.Fatal(err)
	}
	if report.Transactions != 1 || report.ControlWrites != 1 || report.PageWrites != 0 {
		t.Fatalf("checkpoint recovery report = %#v", report)
	}
	controlPage, err := reopenedManager.Read(treecontrol.PageID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := treecontrol.Decode(controlPage, 7)
	if err != nil || state.Generation != 2 || state.RootPageID != 2 ||
		state.NextPageID != 3 {
		t.Fatalf("reopened Tree control = %#v, %v", state, err)
	}
}

func bootstrapTreeRecords(t *testing.T, spaceID uint64) []Record {
	t.Helper()
	return append([]Record{{
		Type: TypePageInit, SpaceID: spaceID, PageID: 2,
		Payload: mustPageImage(t, page.Page{Header: page.Header{
			Type: page.TypeBTreeLeaf, SpaceID: spaceID, PageID: 2, Generation: 1,
		}}),
	}}, bootstrapTreeMetadata(t, spaceID)...)
}

func bootstrapTreeMetadata(t *testing.T, spaceID uint64) []Record {
	t.Helper()
	return []Record{
		{
			Type: TypeAllocator, SpaceID: spaceID, PageID: 1,
			Payload: mustAllocatorRedo(t, AllocatorRedo{
				ExpectedGeneration: 0, ExpectedNextPageID: 2, NextPageID: 3,
			}),
		},
		{
			Type: TypeRoot, SpaceID: spaceID, PageID: 1,
			Payload: mustRootRedo(t, RootRedo{
				ExpectedGeneration: 0, Generation: 1,
				ExpectedRootPageID: 0, RootPageID: 2, ExpectedNextPageID: 2,
			}),
		},
	}
}

func mustRootRedo(t *testing.T, value RootRedo) []byte {
	t.Helper()
	encoded, err := EncodeRootRedo(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func mustAllocatorRedo(t *testing.T, value AllocatorRedo) []byte {
	t.Helper()
	encoded, err := EncodeAllocatorRedo(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func committedRootLSN(t *testing.T, segment *Segment, transactionIndex int) uint64 {
	t.Helper()
	transactions, err := ScanCommitted(segment)
	if err != nil {
		t.Fatal(err)
	}
	records := transactions[transactionIndex].Records
	return records[len(records)-1].LSN
}

func assertTreeControl(
	t *testing.T,
	store *fakePageStore,
	want treecontrol.State,
) {
	t.Helper()
	value, exists := store.pages[treecontrol.PageID]
	if !exists {
		t.Fatal("Tree control Page is missing")
	}
	got, err := treecontrol.Decode(value, want.SpaceID)
	if err != nil || got != want {
		t.Fatalf("Tree control = %#v, %v; want %#v", got, err, want)
	}
}

func testRootRedo(
	expectedGeneration, generation, expectedRoot, root, expectedNext uint64,
) []byte {
	payload := make([]byte, 56)
	copy(payload[:4], "MROT")
	binary.LittleEndian.PutUint16(payload[4:6], 1)
	binary.LittleEndian.PutUint16(payload[6:8], 56)
	binary.LittleEndian.PutUint64(payload[16:24], expectedGeneration)
	binary.LittleEndian.PutUint64(payload[24:32], generation)
	binary.LittleEndian.PutUint64(payload[32:40], expectedRoot)
	binary.LittleEndian.PutUint64(payload[40:48], root)
	binary.LittleEndian.PutUint64(payload[48:56], expectedNext)
	return payload
}

func testAllocatorRedo(
	expectedGeneration, expectedNextPageID, nextPageID uint64,
	retired ...uint64,
) []byte {
	payload := make([]byte, 48+len(retired)*8)
	copy(payload[:4], "MALL")
	binary.LittleEndian.PutUint16(payload[4:6], 1)
	binary.LittleEndian.PutUint16(payload[6:8], 48)
	binary.LittleEndian.PutUint64(payload[16:24], expectedGeneration)
	binary.LittleEndian.PutUint64(payload[24:32], expectedNextPageID)
	binary.LittleEndian.PutUint64(payload[32:40], nextPageID)
	binary.LittleEndian.PutUint32(payload[40:44], uint32(len(retired)))
	for index, pageID := range retired {
		binary.LittleEndian.PutUint64(payload[48+index*8:], pageID)
	}
	return payload
}
