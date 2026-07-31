package buffer

import (
	"errors"
	"sync"
	"testing"

	"github.com/HW-Yue/Memora/internal/store/page"
)

func TestWritablePoolConfigurationIsAllOrNothing(t *testing.T) {
	loader := LoaderFunc(func(key Key) (page.Page, error) { return testPage(key, "x"), nil })
	writer := PageWriterFunc(func(page.Page) error { return nil })
	durability := WALDurabilityFunc(func() (uint64, error) { return 10, nil })
	for _, config := range []Config{
		{Capacity: 2, OldFrames: 1, Writer: writer},
		{Capacity: 2, OldFrames: 1, Durability: durability},
	} {
		if _, err := New(loader, config); !errors.Is(err, ErrInvalid) {
			t.Fatalf("New(partial config) error = %v, want ErrInvalid", err)
		}
	}
	readOnly := mustNewConfiguredPool(t, loader, Config{Capacity: 2, OldFrames: 1})
	handle := mustFetch(t, readOnly, evictionKey(1))
	if err := handle.Modify(1, func(*page.Page) error { return nil }); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("Modify() error = %v, want ErrReadOnly", err)
	}
	if err := readOnly.Flush(evictionKey(1)); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("Flush() error = %v, want ErrReadOnly", err)
	}
	_ = handle.Release()
}

func TestModifyPublishesCommittedPageAndRejectsInvalidChanges(t *testing.T) {
	state := newDirtyHarness(t, 10, Config{Capacity: 2, OldFrames: 1})
	key := evictionKey(1)
	handle := mustFetch(t, state.pool, key)
	if err := handle.Modify(5, func(value *page.Page) error {
		value.Payload = []byte("updated")
		value.Header.LSN = 999
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertPage(t, handle, 5, "updated")
	if got := state.pool.Stats().Dirty; got != 1 {
		t.Fatalf("Dirty = %d, want 1", got)
	}

	callbackErr := errors.New("abort")
	for name, test := range map[string]struct {
		lsn     uint64
		mutate  func(*page.Page) error
		wantErr error
	}{
		"stale":    {5, func(*page.Page) error { return nil }, ErrStalePageLSN},
		"callback": {6, func(*page.Page) error { return callbackErr }, callbackErr},
		"identity": {6, func(value *page.Page) error { value.Header.PageID = 99; return nil }, ErrIdentityMismatch},
		"invalid":  {6, func(value *page.Page) error { value.Payload = make([]byte, page.Size); return nil }, page.ErrInvalid},
	} {
		t.Run(name, func(t *testing.T) {
			err := handle.Modify(test.lsn, test.mutate)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Modify() error = %v, want %v", err, test.wantErr)
			}
			assertPage(t, handle, 5, "updated")
		})
	}
	_ = handle.Release()
}

func TestModifyRequiresDurableWALBeforePublishing(t *testing.T) {
	state := newDirtyHarness(t, 4, Config{Capacity: 2, OldFrames: 1})
	handle := mustFetch(t, state.pool, evictionKey(1))
	if err := handle.Modify(5, func(value *page.Page) error {
		value.Payload = []byte("forbidden")
		return nil
	}); !errors.Is(err, ErrWALNotDurable) {
		t.Fatalf("Modify() error = %v, want ErrWALNotDurable", err)
	}
	assertPage(t, handle, 0, "value")
	if got := state.pool.Stats().Dirty; got != 0 {
		t.Fatalf("Dirty = %d, want 0", got)
	}
	_ = handle.Release()
}

func TestFlushChecksWALBeforeWriteAndRetriesFaults(t *testing.T) {
	state := newDirtyHarness(t, 10, Config{Capacity: 2, OldFrames: 1})
	key := evictionKey(1)
	handle := mustFetch(t, state.pool, key)
	if err := handle.Modify(5, func(value *page.Page) error {
		value.Payload = []byte("dirty")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	state.setDurable(4)
	state.clearEvents()
	if err := state.pool.Flush(key); !errors.Is(err, ErrWALNotDurable) {
		t.Fatalf("Flush() error = %v, want ErrWALNotDurable", err)
	}
	if got := state.eventsCopy(); len(got) != 1 || got[0] != "wal" {
		t.Fatalf("events = %v, want WAL check only", got)
	}
	queryErr := errors.New("durability fault")
	state.setDurabilityError(queryErr)
	if err := state.pool.Flush(key); !errors.Is(err, queryErr) {
		t.Fatalf("Flush() error = %v, want durability fault", err)
	}
	state.setDurabilityError(nil)

	writeErr := errors.New("write fault")
	state.setDurable(5)
	state.writer.setFailure(writeErr, 0)
	if err := state.pool.Flush(key); !errors.Is(err, writeErr) {
		t.Fatalf("Flush() error = %v, want write fault", err)
	}
	if got := state.pool.Stats().Dirty; got != 1 {
		t.Fatalf("Dirty after fault = %d, want 1", got)
	}
	state.writer.setFailure(nil, 0)
	state.clearEvents()
	if err := state.pool.Flush(key); err != nil {
		t.Fatal(err)
	}
	if got := state.eventsCopy(); len(got) != 2 || got[0] != "wal" || got[1] != "write" {
		t.Fatalf("successful flush events = %v, want [wal write]", got)
	}
	if got := state.pool.Stats().Dirty; got != 0 {
		t.Fatalf("Dirty after retry = %d, want 0", got)
	}
	writes := state.writer.pagesCopy()
	if len(writes) != 1 || writes[0].Header.LSN != 5 || string(writes[0].Payload) != "dirty" {
		t.Fatalf("writes = %+v", writes)
	}
	writes[0].Payload[0] = 'X'
	assertPage(t, handle, 5, "dirty")
	_ = handle.Release()
}

func TestDirtyFrameCannotBeEvictedUntilFlush(t *testing.T) {
	state := newDirtyHarness(t, 10, Config{Capacity: 1, OldFrames: 1})
	a, b := evictionKey(1), evictionKey(2)
	handle := mustFetch(t, state.pool, a)
	if err := handle.Modify(5, func(value *page.Page) error { return nil }); err != nil {
		t.Fatal(err)
	}
	_ = handle.Release()
	if _, err := state.pool.Fetch(b); !errors.Is(err, ErrPoolFull) {
		t.Fatalf("Fetch() error = %v, want ErrPoolFull", err)
	}
	if err := state.pool.Flush(a); err != nil {
		t.Fatal(err)
	}
	fetchAndRelease(t, state.pool, b)
}

func TestFlushDirtyUsesFIFOAndContinuesAfterPartialFailure(t *testing.T) {
	state := newDirtyHarness(t, 20, Config{Capacity: 4, OldFrames: 4})
	handles := map[uint64]*Handle{}
	for pageID := uint64(1); pageID <= 3; pageID++ {
		handles[pageID] = mustFetch(t, state.pool, evictionKey(pageID))
	}
	for index, pageID := range []uint64{2, 1, 3} {
		if err := handles[pageID].Modify(uint64(index+5), func(*page.Page) error { return nil }); err != nil {
			t.Fatal(err)
		}
		_ = handles[pageID].Release()
	}
	state.writer.setFailure(nil, 2)
	report, err := state.pool.FlushDirty(2)
	if err == nil {
		t.Fatal("FlushDirty() error = nil, want partial failure")
	}
	if report != (FlushReport{Attempted: 2, Flushed: 1, Remaining: 2}) {
		t.Fatalf("report = %+v", report)
	}
	if got := state.writer.attemptIDs(); !equalUint64(got, []uint64{2, 1}) {
		t.Fatalf("attempt order = %v, want [2 1]", got)
	}
	state.writer.setFailure(nil, 0)
	report, err = state.pool.FlushDirty(10)
	if err != nil {
		t.Fatal(err)
	}
	if report != (FlushReport{Attempted: 2, Flushed: 2, Remaining: 0}) {
		t.Fatalf("retry report = %+v", report)
	}
}

func TestConcurrentFlushSingleFlightsAndBlocksModify(t *testing.T) {
	state := newDirtyHarness(t, 20, Config{Capacity: 2, OldFrames: 1})
	key := evictionKey(1)
	handle := mustFetch(t, state.pool, key)
	if err := handle.Modify(5, func(*page.Page) error { return nil }); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	state.writer.setBlock(entered, release)
	flushResults := make(chan error, 2)
	go func() { flushResults <- state.pool.Flush(key) }()
	receive(t, entered)
	go func() { flushResults <- state.pool.Flush(key) }()
	waitForStats(t, state.pool, Stats{Frames: 1, Pins: 3, Old: 1, Dirty: 1})
	close(release)
	for range 2 {
		if err := receive(t, flushResults); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(state.writer.pagesCopy()); got != 1 {
		t.Fatalf("Page writes = %d, want one effective flush", got)
	}

	if err := handle.Modify(6, func(value *page.Page) error {
		value.Payload = []byte("newer")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	secondEntered := make(chan struct{}, 1)
	secondRelease := make(chan struct{})
	state.writer.setBlock(secondEntered, secondRelease)
	flushDone := make(chan error, 1)
	go func() { flushDone <- state.pool.Flush(key) }()
	receive(t, secondEntered)
	modifyDone := make(chan error, 1)
	go func() {
		modifyDone <- handle.Modify(7, func(value *page.Page) error {
			value.Payload = []byte("newest")
			return nil
		})
	}()
	assertBlocked(t, modifyDone, "Modify completed during Page write")
	close(secondRelease)
	if err := receive(t, flushDone); err != nil {
		t.Fatal(err)
	}
	if err := receive(t, modifyDone); err != nil {
		t.Fatal(err)
	}
	if got := state.pool.Stats().Dirty; got != 1 {
		t.Fatalf("newer Modify Dirty = %d, want 1", got)
	}
	_ = handle.Release()
}

type dirtyHarness struct {
	pool       *Pool
	mu         sync.Mutex
	durableLSN uint64
	durableErr error
	events     []string
	writer     *recordingPageWriter
}

type recordingPageWriter struct {
	harness  *dirtyHarness
	mu       sync.Mutex
	pages    []page.Page
	attempts []uint64
	fail     error
	failPage uint64
	entered  chan struct{}
	release  chan struct{}
}

func newDirtyHarness(t *testing.T, durableLSN uint64, config Config) *dirtyHarness {
	t.Helper()
	harness := &dirtyHarness{durableLSN: durableLSN}
	harness.writer = &recordingPageWriter{harness: harness}
	config.Writer = harness.writer
	config.Durability = WALDurabilityFunc(func() (uint64, error) {
		harness.mu.Lock()
		defer harness.mu.Unlock()
		harness.events = append(harness.events, "wal")
		return harness.durableLSN, harness.durableErr
	})
	loader := LoaderFunc(func(key Key) (page.Page, error) { return testPage(key, "value"), nil })
	harness.pool = mustNewConfiguredPool(t, loader, config)
	return harness
}

func (writer *recordingPageWriter) Write(value page.Page) error {
	writer.harness.mu.Lock()
	writer.harness.events = append(writer.harness.events, "write")
	writer.harness.mu.Unlock()
	writer.mu.Lock()
	writer.attempts = append(writer.attempts, value.Header.PageID)
	entered, release := writer.entered, writer.release
	fail := writer.fail
	if writer.failPage == value.Header.PageID {
		fail = errors.New("selected write fault")
	}
	writer.mu.Unlock()
	if entered != nil {
		entered <- struct{}{}
		<-release
	}
	if fail != nil {
		return fail
	}
	writer.mu.Lock()
	writer.pages = append(writer.pages, clonePage(value))
	writer.mu.Unlock()
	return nil
}

func (state *dirtyHarness) setDurable(lsn uint64) {
	state.mu.Lock()
	state.durableLSN = lsn
	state.mu.Unlock()
}

func (state *dirtyHarness) setDurabilityError(err error) {
	state.mu.Lock()
	state.durableErr = err
	state.mu.Unlock()
}

func (state *dirtyHarness) clearEvents() {
	state.mu.Lock()
	state.events = nil
	state.mu.Unlock()
}

func (state *dirtyHarness) eventsCopy() []string {
	state.mu.Lock()
	defer state.mu.Unlock()
	return append([]string(nil), state.events...)
}

func (writer *recordingPageWriter) pagesCopy() []page.Page {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	result := make([]page.Page, len(writer.pages))
	for index := range writer.pages {
		result[index] = clonePage(writer.pages[index])
	}
	return result
}

func (writer *recordingPageWriter) attemptIDs() []uint64 {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return append([]uint64(nil), writer.attempts...)
}

func (writer *recordingPageWriter) setFailure(fail error, pageID uint64) {
	writer.mu.Lock()
	writer.fail = fail
	writer.failPage = pageID
	writer.mu.Unlock()
}

func (writer *recordingPageWriter) setBlock(entered chan struct{}, release chan struct{}) {
	writer.mu.Lock()
	writer.entered = entered
	writer.release = release
	writer.mu.Unlock()
}

func assertPage(t *testing.T, handle *Handle, lsn uint64, payload string) {
	t.Helper()
	if err := handle.Inspect(func(value page.Page) error {
		if value.Header.LSN != lsn || string(value.Payload) != payload {
			t.Fatalf("Page = LSN %d payload %q, want %d/%q", value.Header.LSN, value.Payload, lsn, payload)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func equalUint64(left, right []uint64) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
