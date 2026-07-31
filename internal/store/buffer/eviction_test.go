package buffer

import (
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/HW-Yue/Memora/internal/store/page"
)

func TestNewRejectsInvalidEvictionConfig(t *testing.T) {
	loader := LoaderFunc(func(key Key) (page.Page, error) { return testPage(key, "x"), nil })
	for _, config := range []Config{
		{},
		{Capacity: 1},
		{Capacity: 1, OldFrames: 2},
	} {
		if _, err := New(loader, config); !errors.Is(err, ErrInvalid) {
			t.Fatalf("New(%+v) error = %v, want ErrInvalid", config, err)
		}
	}
	if _, err := New(loader, Config{Capacity: 1, OldFrames: 1}); err != nil {
		t.Fatalf("one-Frame Pool error = %v", err)
	}
}

func TestOldOnlyModeUsesExactLRU(t *testing.T) {
	loader := newCountingLoader(func(key Key, _ int) (page.Page, error) {
		return testPage(key, "value"), nil
	})
	pool := mustNewConfiguredPool(t, loader, Config{Capacity: 3, OldFrames: 3})
	keys := evictionKeys(1, 2, 3, 1, 4)
	for _, key := range keys {
		fetchAndRelease(t, pool, key)
	}
	if got := pool.Stats(); got.Frames != 3 || got.Old != 3 || got.Young != 0 || got.Evictions != 1 {
		t.Fatalf("Stats() = %+v, want 3 old Frames and one eviction", got)
	}
	if got := loader.Calls(keys[0]); got != 1 {
		t.Fatalf("MRU key Load calls = %d, want 1", got)
	}
	fetchAndRelease(t, pool, keys[1])
	if got := loader.Calls(keys[1]); got != 2 {
		t.Fatalf("LRU key Load calls = %d, want reload", got)
	}
}

func TestOldOnlyLRUMatchesReferenceResidentSequence(t *testing.T) {
	const capacity = 4
	loader := newCountingLoader(func(key Key, _ int) (page.Page, error) {
		return testPage(key, "value"), nil
	})
	pool := mustNewConfiguredPool(t, loader, Config{Capacity: capacity, OldFrames: capacity})
	trace := []uint64{1, 2, 3, 4, 2, 5, 1, 6, 2, 7, 3, 4, 8, 2}
	reference := make([]uint64, 0, capacity)
	var evictions uint64
	for step, pageID := range trace {
		fetchAndRelease(t, pool, evictionKey(pageID))
		index := slices.Index(reference, pageID)
		if index >= 0 {
			reference = append(reference[:index], reference[index+1:]...)
		} else if len(reference) == capacity {
			reference = reference[:len(reference)-1]
			evictions++
		}
		reference = append([]uint64{pageID}, reference...)

		actual := residentPageIDs(pool)
		slices.Sort(actual)
		want := slices.Clone(reference)
		slices.Sort(want)
		if !slices.Equal(actual, want) {
			t.Fatalf("step %d resident = %v, want %v", step, actual, want)
		}
		if got := pool.Stats().Evictions; got != evictions {
			t.Fatalf("step %d evictions = %d, want %d", step, got, evictions)
		}
	}
}

func TestSequentialScanOnlyChurnsOldAndPreservesYoungHotPage(t *testing.T) {
	hot := Key{SpaceID: 7, PageID: 1}
	loader := newCountingLoader(func(key Key, _ int) (page.Page, error) {
		return testPage(key, "value"), nil
	})
	pool := mustNewConfiguredPool(t, loader, Config{Capacity: 4, OldFrames: 1})
	fetchAndRelease(t, pool, hot)
	fetchAndRelease(t, pool, hot)
	for pageID := uint64(2); pageID <= 20; pageID++ {
		fetchAndRelease(t, pool, Key{SpaceID: 7, PageID: pageID})
	}
	if got := pool.Stats(); got.Frames != 2 || got.Young != 1 || got.Old != 1 || got.Evictions != 18 {
		t.Fatalf("Stats() after scan = %+v, want one hot and one probation Frame", got)
	}
	fetchAndRelease(t, pool, hot)
	if got := loader.Calls(hot); got != 1 {
		t.Fatalf("hot Page Load calls = %d, want 1", got)
	}
}

func TestYoungLRUDemotesIntoOldBeforeEviction(t *testing.T) {
	loader := newCountingLoader(func(key Key, _ int) (page.Page, error) {
		return testPage(key, "value"), nil
	})
	pool := mustNewConfiguredPool(t, loader, Config{Capacity: 3, OldFrames: 1})
	a, b, c, d := evictionKey(1), evictionKey(2), evictionKey(3), evictionKey(4)
	for _, key := range []Key{a, a, b, b, c, c} {
		fetchAndRelease(t, pool, key)
	}
	if got := pool.Stats(); got.Frames != 3 || got.Young != 2 || got.Old != 1 {
		t.Fatalf("Stats() after promotions = %+v", got)
	}
	fetchAndRelease(t, pool, d)
	if got := loader.Calls(a); got != 1 {
		t.Fatalf("demoted key prematurely reloaded: %d", got)
	}
	fetchAndRelease(t, pool, a)
	if got := loader.Calls(a); got != 2 {
		t.Fatalf("demoted LRU Load calls = %d, want 2", got)
	}
	for _, protected := range []Key{b, c} {
		fetchAndRelease(t, pool, protected)
		if got := loader.Calls(protected); got != 1 {
			t.Fatalf("young key %+v Load calls = %d, want 1", protected, got)
		}
	}
}

func TestPinnedFramesAreNeverEvictedAndReleaseEnablesRetry(t *testing.T) {
	loader := newCountingLoader(func(key Key, _ int) (page.Page, error) {
		return testPage(key, "value"), nil
	})
	pool := mustNewConfiguredPool(t, loader, Config{Capacity: 2, OldFrames: 1})
	a, b, c := evictionKey(1), evictionKey(2), evictionKey(3)
	aHandle := mustFetch(t, pool, a)
	bHandle := mustFetch(t, pool, b)
	if _, err := pool.Fetch(c); !errors.Is(err, ErrPoolFull) {
		t.Fatalf("Fetch() error = %v, want ErrPoolFull", err)
	}
	if got := loader.Calls(c); got != 0 {
		t.Fatalf("full Pool called Loader %d times", got)
	}
	if err := aHandle.Release(); err != nil {
		t.Fatal(err)
	}
	cHandle := mustFetch(t, pool, c)
	if got := pool.Stats(); got.Frames != 2 || got.Pins != 2 {
		t.Fatalf("Stats() = %+v, want bounded pinned Frames", got)
	}
	if err := bHandle.Inspect(func(page.Page) error { return nil }); err != nil {
		t.Fatalf("pinned Frame was evicted: %v", err)
	}
	if err := bHandle.Release(); err != nil {
		t.Fatal(err)
	}
	if err := cHandle.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadingFrameConsumesCapacityAndCannotBeEvicted(t *testing.T) {
	a, b := evictionKey(1), evictionKey(2)
	entered := make(chan struct{})
	release := make(chan struct{})
	loader := newCountingLoader(func(key Key, _ int) (page.Page, error) {
		if key == a {
			close(entered)
			<-release
		}
		return testPage(key, "value"), nil
	})
	pool := mustNewConfiguredPool(t, loader, Config{Capacity: 1, OldFrames: 1})
	type fetchResult struct {
		handle *Handle
		err    error
	}
	result := make(chan fetchResult, 1)
	go func() {
		handle, err := pool.Fetch(a)
		result <- fetchResult{handle: handle, err: err}
	}()
	receive(t, entered)
	if got := pool.Stats(); got.Frames != 1 || got.Loading != 1 || got.Pins != 1 {
		t.Fatalf("Stats() during load = %+v", got)
	}
	if _, err := pool.Fetch(b); !errors.Is(err, ErrPoolFull) {
		t.Fatalf("Fetch while loading error = %v, want ErrPoolFull", err)
	}
	close(release)
	loaded := receive(t, result)
	if loaded.err != nil {
		t.Fatal(loaded.err)
	}
	if err := loaded.handle.Release(); err != nil {
		t.Fatal(err)
	}
	fetchAndRelease(t, pool, b)
	if got := loader.Calls(b); got != 1 {
		t.Fatalf("Load calls for retry key = %d, want 1", got)
	}
}

func TestConcurrentMissesNeverExceedCapacity(t *testing.T) {
	const (
		capacity = 8
		callers  = 64
	)
	entered := make(chan Key, callers)
	release := make(chan struct{})
	loader := newCountingLoader(func(key Key, _ int) (page.Page, error) {
		entered <- key
		<-release
		return testPage(key, "value"), nil
	})
	pool := mustNewConfiguredPool(t, loader, Config{Capacity: capacity, OldFrames: 2})
	start := make(chan struct{})
	type result struct {
		handle *Handle
		err    error
	}
	results := make(chan result, callers)
	ready := sync.WaitGroup{}
	ready.Add(callers)
	for index := range callers {
		go func(pageID int) {
			ready.Done()
			<-start
			handle, err := pool.Fetch(Key{SpaceID: 7, PageID: uint64(pageID + 1)})
			results <- result{handle: handle, err: err}
		}(index)
	}
	ready.Wait()
	close(start)
	for range capacity {
		receive(t, entered)
	}
	waitForStats(t, pool, Stats{Frames: capacity, Loading: capacity, Pins: capacity})
	close(release)

	var succeeded, full int
	handles := make([]*Handle, 0, capacity)
	for range callers {
		result := receive(t, results)
		switch {
		case result.err == nil:
			succeeded++
			handles = append(handles, result.handle)
		case errors.Is(result.err, ErrPoolFull):
			full++
		default:
			t.Fatalf("Fetch() error = %v", result.err)
		}
	}
	if succeeded != capacity || full != callers-capacity {
		t.Fatalf("success/full = %d/%d, want %d/%d", succeeded, full, capacity, callers-capacity)
	}
	if got := pool.Stats().Frames; got > capacity {
		t.Fatalf("Frames = %d, exceeds %d", got, capacity)
	}
	for _, handle := range handles {
		if err := handle.Release(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFailedLoadAfterVictimSelectionLeavesCapacityEmptyForRetry(t *testing.T) {
	a, b := evictionKey(1), evictionKey(2)
	wantErr := errors.New("load failed")
	loader := newCountingLoader(func(key Key, call int) (page.Page, error) {
		if key == b && call == 1 {
			return page.Page{}, wantErr
		}
		return testPage(key, "value"), nil
	})
	pool := mustNewConfiguredPool(t, loader, Config{Capacity: 1, OldFrames: 1})
	fetchAndRelease(t, pool, a)
	if _, err := pool.Fetch(b); !errors.Is(err, wantErr) {
		t.Fatalf("Fetch() error = %v, want load failure", err)
	}
	if got := pool.Stats(); got.Frames != 0 || got.Evictions != 1 {
		t.Fatalf("Stats() after failed replacement = %+v", got)
	}
	fetchAndRelease(t, pool, b)
	if got := loader.Calls(b); got != 2 {
		t.Fatalf("retry Load calls = %d, want 2", got)
	}
}

func mustNewConfiguredPool(t *testing.T, loader Loader, config Config) *Pool {
	t.Helper()
	pool, err := New(loader, config)
	if err != nil {
		t.Fatal(err)
	}
	return pool
}

func fetchAndRelease(t *testing.T, pool *Pool, key Key) {
	t.Helper()
	handle := mustFetch(t, pool, key)
	if err := handle.Release(); err != nil {
		t.Fatal(err)
	}
}

func evictionKey(pageID uint64) Key {
	return Key{SpaceID: 7, PageID: pageID}
}

func evictionKeys(pageIDs ...uint64) []Key {
	keys := make([]Key, len(pageIDs))
	for index, pageID := range pageIDs {
		keys[index] = evictionKey(pageID)
	}
	return keys
}

func residentPageIDs(pool *Pool) []uint64 {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	pageIDs := make([]uint64, 0, len(pool.frames))
	for key := range pool.frames {
		pageIDs = append(pageIDs, key.PageID)
	}
	return pageIDs
}
