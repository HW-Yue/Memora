package buffer

import (
	"errors"
	"reflect"
	"testing"

	"github.com/HW-Yue/Memora/internal/store/page"
	"github.com/HW-Yue/Memora/internal/store/treecontrol"
)

func TestPublishBatchInstallsCommittedPagesControlLast(t *testing.T) {
	state := newBatchHarness(t, 40, Config{Capacity: 5, OldFrames: 2})
	var pinned []*Handle
	for _, pageID := range []uint64{1, 2, 4} {
		pinned = append(pinned, mustFetch(t, state.pool, evictionKey(pageID)))
	}
	defer func() {
		for _, handle := range pinned {
			_ = handle.Release()
		}
	}()
	changes := []BatchChange{
		{Page: batchPage(2, page.TypeBTreeLeaf, 30), ExpectedLSN: 11},
		{Page: batchPage(3, page.TypeBTreeLeaf, 31), New: true},
		{Page: batchPage(4, page.TypeFree, 32), ExpectedLSN: 12},
		{Page: batchPage(1, page.TypeTreeControl, 33), ExpectedLSN: 10},
	}
	if err := state.pool.PublishBatch(changes); err != nil {
		t.Fatal(err)
	}
	changes[0].Page.Payload[0] = 'X'
	for _, handle := range pinned {
		if err := handle.Release(); err != nil {
			t.Fatal(err)
		}
	}
	pinned = nil
	if got := state.pool.Stats(); got.Frames != 4 || got.Dirty != 4 {
		t.Fatalf("Stats = %+v", got)
	}
	for _, change := range changes {
		handle := mustFetch(t, state.pool, Key{
			SpaceID: change.Page.Header.SpaceID,
			PageID:  change.Page.Header.PageID,
		})
		if change.Page.Header.Type == page.TypeTreeControl {
			if err := handle.Inspect(func(value page.Page) error {
				if value.Header.LSN != change.Page.Header.LSN {
					t.Fatalf("control LSN = %d", value.Header.LSN)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		} else {
			assertPage(t, handle, change.Page.Header.LSN, "committed")
		}
		if err := handle.Release(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := state.pool.FlushDirty(10); err != nil {
		t.Fatal(err)
	}
	if got := state.writer.attemptIDs(); !equalUint64(got, []uint64{2, 3, 4, 1}) {
		t.Fatalf("flush order = %v", got)
	}
}

func TestPublishBatchRejectsInvalidOrConflictingBatchAtomically(t *testing.T) {
	injected := errors.New("durability fault")
	tests := []struct {
		name   string
		mutate func(*dirtyHarness, *[]BatchChange)
		want   error
	}{
		{"empty", func(_ *dirtyHarness, changes *[]BatchChange) { *changes = nil }, ErrInvalid},
		{"missing control", func(_ *dirtyHarness, changes *[]BatchChange) {
			*changes = (*changes)[:len(*changes)-1]
		}, ErrInvalid},
		{"control not last", func(_ *dirtyHarness, changes *[]BatchChange) {
			(*changes)[0], (*changes)[3] = (*changes)[3], (*changes)[0]
		}, ErrInvalid},
		{"duplicate", func(_ *dirtyHarness, changes *[]BatchChange) {
			(*changes)[1].Page.Header.PageID = 2
		}, ErrInvalid},
		{"space", func(_ *dirtyHarness, changes *[]BatchChange) {
			(*changes)[1].Page.Header.SpaceID++
		}, ErrInvalid},
		{"generation", func(_ *dirtyHarness, changes *[]BatchChange) {
			(*changes)[1].Page.Header.Generation++
		}, ErrInvalid},
		{"zero generation", func(_ *dirtyHarness, changes *[]BatchChange) {
			for index := range *changes {
				(*changes)[index].Page.Header.Generation = 0
			}
		}, ErrInvalid},
		{"zero LSN", func(_ *dirtyHarness, changes *[]BatchChange) {
			(*changes)[1].Page.Header.LSN = 0
		}, ErrInvalid},
		{"type", func(_ *dirtyHarness, changes *[]BatchChange) {
			(*changes)[1].Page.Header.Type = page.TypeData
		}, ErrInvalid},
		{"durability", func(state *dirtyHarness, _ *[]BatchChange) {
			state.setDurable(29)
		}, ErrWALNotDurable},
		{"durability fault", func(state *dirtyHarness, _ *[]BatchChange) {
			state.setDurabilityError(injected)
		}, injected},
		{"expected LSN", func(_ *dirtyHarness, changes *[]BatchChange) {
			(*changes)[0].ExpectedLSN++
		}, ErrPublishConflict},
		{"missing existing", func(_ *dirtyHarness, changes *[]BatchChange) {
			(*changes)[0].Page.Header.PageID = 8
		}, ErrPublishConflict},
		{"duplicate new identity", func(_ *dirtyHarness, changes *[]BatchChange) {
			(*changes)[1].Page.Header.PageID = 4
		}, ErrInvalid},
		{"new expected LSN", func(_ *dirtyHarness, changes *[]BatchChange) {
			(*changes)[1].ExpectedLSN = 1
		}, ErrPublishConflict},
		{"new is resident", func(state *dirtyHarness, _ *[]BatchChange) {
			handle := mustFetch(t, state.pool, evictionKey(3))
			_ = handle.Release()
		}, ErrPublishConflict},
		{"existing is loading", func(state *dirtyHarness, _ *[]BatchChange) {
			state.pool.mu.Lock()
			state.pool.frames[evictionKey(2)].loading = true
			state.pool.mu.Unlock()
		}, ErrPublishConflict},
		{"existing type", func(state *dirtyHarness, _ *[]BatchChange) {
			state.pool.mu.Lock()
			state.pool.frames[evictionKey(2)].value.Header.Type = page.TypeBTreeInternal
			state.pool.mu.Unlock()
		}, ErrPublishConflict},
		{"stale new LSN", func(_ *dirtyHarness, changes *[]BatchChange) {
			(*changes)[0].Page.Header.LSN = 11
		}, ErrPublishConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state, handles := loadedBatchHarness(t, Config{Capacity: 5, OldFrames: 5})
			defer releaseHandles(handles)
			changes := validBatchChanges()
			test.mutate(state, &changes)
			before := batchSnapshot(state.pool)
			if err := state.pool.PublishBatch(changes); !errors.Is(err, test.want) {
				t.Fatalf("PublishBatch() error = %v, want %v", err, test.want)
			}
			after := batchSnapshot(state.pool)
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("failed publish mutated Pool\nbefore=%#v\nafter=%#v", before, after)
			}
		})
	}
}

func TestPublishBatchRequiresWritablePool(t *testing.T) {
	pool := mustNewConfiguredPool(t, LoaderFunc(func(key Key) (page.Page, error) {
		return batchPage(key.PageID, page.TypeBTreeLeaf, 1), nil
	}), Config{Capacity: 2, OldFrames: 1})
	if err := pool.PublishBatch(validBatchChanges()); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("PublishBatch() error = %v", err)
	}
}

func TestPublishBatchCapacityEvictionIsPreflighted(t *testing.T) {
	t.Run("clean victim", func(t *testing.T) {
		state, handles := loadedBatchHarness(t, Config{Capacity: 4, OldFrames: 4})
		victim := mustFetch(t, state.pool, evictionKey(9))
		_ = victim.Release()
		releaseHandles(handles)
		if err := state.pool.PublishBatch(validBatchChanges()); err != nil {
			t.Fatal(err)
		}
		if got := state.pool.Stats(); got.Frames != 4 || got.Evictions != 1 {
			t.Fatalf("Stats = %+v", got)
		}
		state.pool.mu.Lock()
		_, victimExists := state.pool.frames[evictionKey(9)]
		state.pool.mu.Unlock()
		if victimExists {
			t.Fatal("clean victim remains resident")
		}
	})

	t.Run("pinned victim", func(t *testing.T) {
		state, handles := loadedBatchHarness(t, Config{Capacity: 4, OldFrames: 4})
		defer releaseHandles(handles)
		victim := mustFetch(t, state.pool, evictionKey(9))
		defer func() { _ = victim.Release() }()
		before := batchSnapshot(state.pool)
		if err := state.pool.PublishBatch(validBatchChanges()); !errors.Is(err, ErrPoolFull) {
			t.Fatalf("PublishBatch() error = %v", err)
		}
		if after := batchSnapshot(state.pool); !reflect.DeepEqual(after, before) {
			t.Fatalf("capacity failure mutated Pool: %#v", after)
		}
	})
}

func TestPublishBatchBarrierBlocksPageOperations(t *testing.T) {
	operations := map[string]func(*dirtyHarness, []*Handle) <-chan error{
		"Inspect": func(_ *dirtyHarness, handles []*Handle) <-chan error {
			done := make(chan error, 1)
			go func() {
				done <- handles[1].Inspect(func(page.Page) error { return nil })
			}()
			return done
		},
		"Modify": func(_ *dirtyHarness, handles []*Handle) <-chan error {
			done := make(chan error, 1)
			go func() {
				done <- handles[1].Modify(20, func(value *page.Page) error {
					value.Payload = []byte("modified")
					return nil
				})
			}()
			return done
		},
		"Fetch": func(state *dirtyHarness, _ []*Handle) <-chan error {
			done := make(chan error, 1)
			go func() {
				handle, err := state.pool.Fetch(evictionKey(9))
				if err == nil {
					err = handle.Release()
				}
				done <- err
			}()
			return done
		},
		"Flush": func(state *dirtyHarness, _ []*Handle) <-chan error {
			done := make(chan error, 1)
			go func() { done <- state.pool.Flush(evictionKey(2)) }()
			return done
		},
	}
	for name, operation := range operations {
		t.Run(name, func(t *testing.T) {
			state, handles := loadedBatchHarness(t, Config{Capacity: 5, OldFrames: 5})
			defer releaseHandles(handles)
			state.pool.publishMu.Lock()
			done := operation(state, handles)
			assertBlocked(t, done, name+" crossed publish barrier")
			state.pool.publishMu.Unlock()
			if err := receive(t, done); err != nil {
				t.Fatal(err)
			}
		})
	}

	t.Run("Publish", func(t *testing.T) {
		state, handles := loadedBatchHarness(t, Config{Capacity: 5, OldFrames: 5})
		defer releaseHandles(handles)
		state.pool.publishMu.RLock()
		done := make(chan error, 1)
		go func() { done <- state.pool.PublishBatch(validBatchChanges()) }()
		assertBlocked(t, done, "PublishBatch crossed reader barrier")
		state.pool.publishMu.RUnlock()
		if err := receive(t, done); err != nil {
			t.Fatal(err)
		}
	})
}

type batchFrameState struct {
	Value page.Page
	Dirty bool
	Queue frameQueue
}

type batchPoolState struct {
	Frames    map[Key]batchFrameState
	Dirty     []Key
	Young     []Key
	Old       []Key
	Stats     Stats
	Evictions uint64
}

func batchSnapshot(pool *Pool) batchPoolState {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	result := batchPoolState{
		Frames: make(map[Key]batchFrameState, len(pool.frames)),
		Stats:  pool.statsLocked(),
	}
	for key, current := range pool.frames {
		result.Frames[key] = batchFrameState{
			Value: clonePage(current.value), Dirty: current.dirty, Queue: current.queue,
		}
	}
	for element := pool.dirty.Front(); element != nil; element = element.Next() {
		result.Dirty = append(result.Dirty, element.Value.(*frame).key)
	}
	for element := pool.young.Front(); element != nil; element = element.Next() {
		result.Young = append(result.Young, element.Value.(*frame).key)
	}
	for element := pool.old.Front(); element != nil; element = element.Next() {
		result.Old = append(result.Old, element.Value.(*frame).key)
	}
	return result
}

func (pool *Pool) statsLocked() Stats {
	state := Stats{
		Frames: uint64(len(pool.frames)), Young: uint64(pool.young.Len()),
		Old: uint64(pool.old.Len()), Evictions: pool.evictions,
		Dirty: uint64(pool.dirty.Len()),
	}
	for _, current := range pool.frames {
		state.Pins += current.pins
		if current.loading {
			state.Loading++
		}
	}
	return state
}

func validBatchChanges() []BatchChange {
	return []BatchChange{
		{Page: batchPage(2, page.TypeBTreeLeaf, 30), ExpectedLSN: 11},
		{Page: batchPage(3, page.TypeBTreeLeaf, 31), New: true},
		{Page: batchPage(4, page.TypeFree, 32), ExpectedLSN: 12},
		{Page: batchPage(1, page.TypeTreeControl, 33), ExpectedLSN: 10},
	}
}

func loadedBatchHarness(
	t *testing.T,
	config Config,
) (*dirtyHarness, []*Handle) {
	t.Helper()
	state := newBatchHarness(t, 40, config)
	handles := make([]*Handle, 0, 3)
	for _, pageID := range []uint64{1, 2, 4} {
		handles = append(handles, mustFetch(t, state.pool, evictionKey(pageID)))
	}
	return state, handles
}

func releaseHandles(handles []*Handle) {
	for _, handle := range handles {
		_ = handle.Release()
	}
}

func newBatchHarness(
	t *testing.T,
	durableLSN uint64,
	config Config,
) *dirtyHarness {
	t.Helper()
	harness := &dirtyHarness{durableLSN: durableLSN}
	harness.writer = &recordingPageWriter{harness: harness}
	pages := map[uint64]page.Page{
		1: batchPage(1, page.TypeTreeControl, 10),
		2: batchPage(2, page.TypeBTreeLeaf, 11),
		4: batchPage(4, page.TypeBTreeLeaf, 12),
	}
	loader := LoaderFunc(func(key Key) (page.Page, error) {
		if value, exists := pages[key.PageID]; exists {
			return clonePage(value), nil
		}
		return batchPage(key.PageID, page.TypeBTreeLeaf, 13), nil
	})
	config.Writer = harness.writer
	config.Durability = WALDurabilityFunc(func() (uint64, error) {
		harness.mu.Lock()
		defer harness.mu.Unlock()
		harness.events = append(harness.events, "wal")
		return harness.durableLSN, harness.durableErr
	})
	harness.pool = mustNewConfiguredPool(t, loader, config)
	return harness
}

func batchPage(pageID uint64, pageType page.Type, lsn uint64) page.Page {
	if pageType == page.TypeTreeControl {
		value, err := treecontrol.Encode(treecontrol.State{
			SpaceID: 7, Generation: 3, Revision: lsn,
			RootPageID: 2, NextPageID: 5, LSN: lsn,
		})
		if err != nil {
			panic(err)
		}
		return value
	}
	return page.Page{
		Header: page.Header{
			Type: pageType, SpaceID: 7, PageID: pageID,
			Generation: 3, LSN: lsn,
		},
		Payload: []byte("committed"),
	}
}
