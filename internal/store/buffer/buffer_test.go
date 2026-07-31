package buffer

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/store/page"
)

func TestFetchSingleFlightPinsEveryCallerAndCachesPage(t *testing.T) {
	const callers = 64
	key := Key{SpaceID: 7, PageID: 11}
	entered := make(chan Key, callers)
	releaseLoad := make(chan struct{})
	loader := newCountingLoader(func(got Key, _ int) (page.Page, error) {
		entered <- got
		<-releaseLoad
		return testPage(got, "loaded"), nil
	})
	pool := mustNewPool(t, loader)

	start := make(chan struct{})
	ready := sync.WaitGroup{}
	ready.Add(callers)
	type result struct {
		handle *Handle
		err    error
	}
	results := make(chan result, callers)
	for range callers {
		go func() {
			ready.Done()
			<-start
			handle, err := pool.Fetch(key)
			results <- result{handle: handle, err: err}
		}()
	}
	ready.Wait()
	close(start)
	if got := receive(t, entered); got != key {
		t.Fatalf("Load key = %+v, want %+v", got, key)
	}
	waitForStats(t, pool, Stats{Frames: 1, Loading: 1, Pins: callers})
	close(releaseLoad)

	handles := make([]*Handle, 0, callers)
	for range callers {
		result := receive(t, results)
		if result.err != nil {
			t.Fatalf("Fetch() error = %v", result.err)
		}
		handles = append(handles, result.handle)
	}
	if got := loader.Calls(key); got != 1 {
		t.Fatalf("Load calls = %d, want 1", got)
	}
	if got := pool.Stats(); got != (Stats{Frames: 1, Pins: callers, Old: 1}) {
		t.Fatalf("Stats() = %+v, want one Frame and %d pins", got, callers)
	}
	for _, handle := range handles {
		if err := handle.Release(); err != nil {
			t.Fatalf("Release() error = %v", err)
		}
	}
	if got := pool.Stats(); got != (Stats{Frames: 1, Old: 1}) {
		t.Fatalf("Stats() after Release = %+v, want cached unpinned Frame", got)
	}

	hit := mustFetch(t, pool, key)
	if got := loader.Calls(key); got != 1 {
		t.Fatalf("cache hit Load calls = %d, want 1", got)
	}
	if err := hit.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestFetchAllowsDifferentKeysToLoadConcurrently(t *testing.T) {
	first := Key{SpaceID: 7, PageID: 1}
	second := Key{SpaceID: 7, PageID: 2}
	entered := make(chan Key, 2)
	release := make(chan struct{})
	pool := mustNewPool(t, newCountingLoader(func(key Key, _ int) (page.Page, error) {
		entered <- key
		<-release
		return testPage(key, fmt.Sprint(key.PageID)), nil
	}))

	results := make(chan *Handle, 2)
	errors := make(chan error, 2)
	go fetchTo(pool, first, results, errors)
	if got := receive(t, entered); got != first {
		t.Fatalf("first Load = %+v", got)
	}
	go fetchTo(pool, second, results, errors)
	if got := receive(t, entered); got != second {
		t.Fatalf("second Load = %+v; Pool serialized different keys", got)
	}
	close(release)
	for range 2 {
		if err := receive(t, errors); err != nil {
			t.Fatal(err)
		}
		if err := receive(t, results).Release(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFetchBroadcastsLoadFailureThenRetries(t *testing.T) {
	const callers = 32
	key := Key{SpaceID: 8, PageID: 3}
	wantErr := errors.New("read failed")
	release := make(chan struct{})
	loader := newCountingLoader(func(key Key, call int) (page.Page, error) {
		if call == 1 {
			<-release
			return page.Page{}, wantErr
		}
		return testPage(key, "retry"), nil
	})
	pool := mustNewPool(t, loader)
	start := make(chan struct{})
	results := make(chan error, callers)
	for range callers {
		go func() {
			<-start
			_, err := pool.Fetch(key)
			results <- err
		}()
	}
	close(start)
	waitForStats(t, pool, Stats{Frames: 1, Loading: 1, Pins: callers})
	close(release)
	for range callers {
		if err := receive(t, results); !errors.Is(err, wantErr) {
			t.Fatalf("Fetch() error = %v, want %v", err, wantErr)
		}
	}
	if got := pool.Stats(); got != (Stats{}) {
		t.Fatalf("Stats() after failed load = %+v, want empty", got)
	}

	handle := mustFetch(t, pool, key)
	if got := loader.Calls(key); got != 2 {
		t.Fatalf("retry Load calls = %d, want 2", got)
	}
	if err := handle.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestFetchRejectsWrongIdentityWithoutCaching(t *testing.T) {
	key := Key{SpaceID: 7, PageID: 4}
	loader := newCountingLoader(func(_ Key, _ int) (page.Page, error) {
		return testPage(Key{SpaceID: 99, PageID: 4}, "wrong"), nil
	})
	pool := mustNewPool(t, loader)
	for attempt := 1; attempt <= 2; attempt++ {
		if _, err := pool.Fetch(key); !errors.Is(err, ErrIdentityMismatch) {
			t.Fatalf("Fetch() error = %v, want ErrIdentityMismatch", err)
		}
		if got := loader.Calls(key); got != attempt {
			t.Fatalf("Load calls = %d, want %d", got, attempt)
		}
	}
	if got := pool.Stats(); got != (Stats{}) {
		t.Fatalf("Stats() = %+v, want empty", got)
	}
}

func TestHandleReleaseIsExactlyOnce(t *testing.T) {
	key := Key{SpaceID: 7, PageID: 5}
	pool := mustNewPool(t, LoaderFunc(func(key Key) (page.Page, error) {
		return testPage(key, "value"), nil
	}))
	handle := mustFetch(t, pool, key)
	if err := handle.Release(); err != nil {
		t.Fatal(err)
	}
	if err := handle.Release(); !errors.Is(err, ErrReleased) {
		t.Fatalf("second Release() error = %v, want ErrReleased", err)
	}
	if err := handle.Inspect(func(page.Page) error { return nil }); !errors.Is(err, ErrReleased) {
		t.Fatalf("Inspect() after Release error = %v, want ErrReleased", err)
	}
	if err := handle.InspectExclusive(func(page.Page) error { return nil }); !errors.Is(err, ErrReleased) {
		t.Fatalf("InspectExclusive() after Release error = %v, want ErrReleased", err)
	}
	if got := pool.Stats().Pins; got != 0 {
		t.Fatalf("pins = %d, want 0", got)
	}
}

func TestFrameLatchAndHandleReleaseUseControlledScheduling(t *testing.T) {
	firstKey := Key{SpaceID: 7, PageID: 6}
	secondKey := Key{SpaceID: 7, PageID: 7}
	pool := mustNewPool(t, LoaderFunc(func(key Key) (page.Page, error) {
		return testPage(key, "value"), nil
	}))
	readerOne := mustFetch(t, pool, firstKey)
	readerTwo := mustFetch(t, pool, firstKey)
	writer := mustFetch(t, pool, firstKey)
	other := mustFetch(t, pool, secondKey)

	readEntered := make(chan struct{}, 2)
	releaseReads := make(chan struct{})
	readDone := make(chan error, 2)
	for _, handle := range []*Handle{readerOne, readerTwo} {
		go func(handle *Handle) {
			readDone <- handle.Inspect(func(page.Page) error {
				readEntered <- struct{}{}
				<-releaseReads
				return nil
			})
		}(handle)
	}
	receive(t, readEntered)
	receive(t, readEntered)

	writeStarted := make(chan struct{})
	writeEntered := make(chan struct{})
	releaseWrite := make(chan struct{})
	writeDone := make(chan error, 1)
	go func() {
		close(writeStarted)
		writeDone <- writer.InspectExclusive(func(page.Page) error {
			close(writeEntered)
			<-releaseWrite
			return nil
		})
	}()
	<-writeStarted
	assertBlocked(t, writeEntered, "exclusive callback entered while readers held latch")

	otherEntered := make(chan struct{})
	otherDone := make(chan error, 1)
	go func() {
		otherDone <- other.InspectExclusive(func(page.Page) error {
			close(otherEntered)
			return nil
		})
	}()
	receive(t, otherEntered)
	if err := receive(t, otherDone); err != nil {
		t.Fatal(err)
	}

	close(releaseReads)
	for range 2 {
		if err := receive(t, readDone); err != nil {
			t.Fatal(err)
		}
	}
	receive(t, writeEntered)

	releaseReturned := make(chan error, 1)
	go func() { releaseReturned <- writer.Release() }()
	assertBlocked(t, releaseReturned, "Release returned while Handle callback was running")
	close(releaseWrite)
	if err := receive(t, writeDone); err != nil {
		t.Fatal(err)
	}
	if err := receive(t, releaseReturned); err != nil {
		t.Fatal(err)
	}
	for _, handle := range []*Handle{readerOne, readerTwo, other} {
		if err := handle.Release(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPageValuesAreDeepCopiedAtEveryBoundary(t *testing.T) {
	key := Key{SpaceID: 7, PageID: 8}
	original := testPage(key, "original")
	pool := mustNewPool(t, LoaderFunc(func(Key) (page.Page, error) {
		return original, nil
	}))
	handle := mustFetch(t, pool, key)
	original.Payload[0] = 'X'
	if err := handle.Inspect(func(value page.Page) error {
		if got := string(value.Payload); got != "original" {
			t.Fatalf("cached payload = %q, want original", got)
		}
		value.Payload[0] = 'Y'
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := handle.InspectExclusive(func(value page.Page) error {
		if got := string(value.Payload); got != "original" {
			t.Fatalf("cached payload after callback mutation = %q", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := handle.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestPoolRejectsInvalidInputs(t *testing.T) {
	if _, err := New(nil, Config{Capacity: 4, OldFrames: 1}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("New(nil) error = %v, want ErrInvalid", err)
	}
	pool := mustNewPool(t, LoaderFunc(func(key Key) (page.Page, error) {
		return testPage(key, "value"), nil
	}))
	if _, err := pool.Fetch(Key{SpaceID: 7}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Fetch(zero PageID) error = %v, want ErrInvalid", err)
	}
	handle := mustFetch(t, pool, Key{SpaceID: 7, PageID: 9})
	if err := handle.Inspect(nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Inspect(nil) error = %v, want ErrInvalid", err)
	}
	if err := handle.InspectExclusive(nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("InspectExclusive(nil) error = %v, want ErrInvalid", err)
	}
	if err := handle.Release(); err != nil {
		t.Fatal(err)
	}
}

type countingLoader struct {
	mu    sync.Mutex
	calls map[Key]int
	load  func(Key, int) (page.Page, error)
}

func newCountingLoader(load func(Key, int) (page.Page, error)) *countingLoader {
	return &countingLoader{calls: make(map[Key]int), load: load}
}

func (loader *countingLoader) Load(key Key) (page.Page, error) {
	loader.mu.Lock()
	loader.calls[key]++
	call := loader.calls[key]
	loader.mu.Unlock()
	return loader.load(key, call)
}

func (loader *countingLoader) Calls(key Key) int {
	loader.mu.Lock()
	defer loader.mu.Unlock()
	return loader.calls[key]
}

func mustNewPool(t *testing.T, loader Loader) *Pool {
	t.Helper()
	pool, err := New(loader, Config{Capacity: 1024, OldFrames: 256})
	if err != nil {
		t.Fatal(err)
	}
	return pool
}

func mustFetch(t *testing.T, pool *Pool, key Key) *Handle {
	t.Helper()
	handle, err := pool.Fetch(key)
	if err != nil {
		t.Fatal(err)
	}
	return handle
}

func fetchTo(pool *Pool, key Key, handles chan<- *Handle, errs chan<- error) {
	handle, err := pool.Fetch(key)
	handles <- handle
	errs <- err
}

func testPage(key Key, payload string) page.Page {
	return page.Page{
		Header:  page.Header{Type: page.TypeData, SpaceID: key.SpaceID, PageID: key.PageID},
		Payload: []byte(payload),
	}
}

func receive[T any](t *testing.T, channel <-chan T) T {
	t.Helper()
	select {
	case value := <-channel:
		return value
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for controlled schedule")
		var zero T
		return zero
	}
}

func assertBlocked[T any](t *testing.T, channel <-chan T, message string) {
	t.Helper()
	select {
	case <-channel:
		t.Fatal(message)
	case <-time.After(25 * time.Millisecond):
	}
}

func waitForStats(t *testing.T, pool *Pool, want Stats) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		if pool.Stats() == want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("Stats() did not reach %+v; got %+v", want, pool.Stats())
		default:
		}
	}
}
