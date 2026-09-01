package rowversionindex

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/store/btree"
	"github.com/HW-Yue/Memora/internal/store/page"
	"github.com/HW-Yue/Memora/internal/store/treecommit"
	"github.com/HW-Yue/Memora/internal/store/wal"
)

const testSpaceID = uint64(61)

func TestBootstrapCreatesEmptyOrCompleteHistoricalAuthority(t *testing.T) {
	_, _, emptyRuntime, empty := newTestIndex(t)
	receipt, err := empty.Bootstrap(1, nil, 0)
	if err != nil || !receipt.Changed || emptyRuntime.State().RootPageID == 0 {
		t.Fatalf("Bootstrap(empty) = %+v, %v", receipt, err)
	}
	if highWater, err := empty.HighWater(); err != nil || highWater != 0 {
		t.Fatalf("empty high-water = %d, %v", highWater, err)
	}

	_, _, _, index := newTestIndex(t)
	first := locator("row_one", 1, 0, row.StateLive)
	second := locator("row_one", 2, 9, row.StateDeleted)
	receipt, err = index.Bootstrap(1, []Locator{first, second}, 0)
	if err != nil || !receipt.Changed {
		t.Fatalf("Bootstrap(history) = %+v, %v", receipt, err)
	}
	assertLookup(t, first, func() (Locator, error) { return index.ByRevision(first.RowID, 1) })
	assertLookup(t, second, func() (Locator, error) { return index.ByRevision(second.RowID, 2) })
	if highWater, err := index.HighWater(); err != nil || highWater != second.CommitSequence {
		t.Fatalf("history high-water = %d, %v", highWater, err)
	}
	if _, err := index.Bootstrap(2, nil, 0); !errors.Is(err, ErrConflict) {
		t.Fatalf("Bootstrap(non-empty) error = %v", err)
	}
}

func TestLookupByRevisionCommitAndAsOfFloor(t *testing.T) {
	_, _, _, index := newTestIndex(t)
	versions := []Locator{
		locator("row_one", 1, 10, row.StateLive),
		locator("row_one", 2, 20, row.StateDeleted),
		locator("row_one", 3, 20, row.StateLive),
		locator("row_one", 4, 30, row.StateSuperseded),
	}
	if receipt, err := index.Append(1, versions); err != nil ||
		!receipt.Changed || receipt.State.Revision != 1 {
		t.Fatalf("Append() = %+v, %v", receipt, err)
	}
	assertLookup(t, versions[1], func() (Locator, error) {
		return index.ByRevision("row_one", 2)
	})
	assertLookup(t, versions[2], func() (Locator, error) {
		return index.ByCommit("row_one", 20)
	})
	tests := []struct {
		sequence uint64
		want     Locator
		found    bool
	}{
		{9, Locator{}, false},
		{10, versions[0], true},
		{19, versions[0], true},
		{20, versions[2], true},
		{29, versions[2], true},
		{30, versions[3], true},
		{999, versions[3], true},
	}
	for _, test := range tests {
		got, err := index.AsOfCommit("row_one", test.sequence)
		if !test.found {
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("AsOfCommit(%d) error = %v", test.sequence, err)
			}
			continue
		}
		if err != nil || got != test.want {
			t.Fatalf("AsOfCommit(%d) = %+v, %v; want %+v", test.sequence, got, err, test.want)
		}
	}
	if _, err := index.ByRevision("row_missing", 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing revision error = %v", err)
	}
	if _, err := index.ByCommit("row_one", 11); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing exact commit error = %v", err)
	}
}

func TestSnapshotHighWaterAndLegacyFloorAdvanceAtomically(t *testing.T) {
	set, _, _, index := newTestIndex(t)
	if got, err := index.HighWater(); err != nil || got != 0 {
		t.Fatalf("empty HighWater() = %d, %v", got, err)
	}
	legacyOne := locator("row_legacy", 1, 0, row.StateLive)
	legacyTwo := locator("row_legacy", 2, 0, row.StateLive)
	if _, err := index.Append(1, []Locator{legacyOne, legacyTwo}); err != nil {
		t.Fatal(err)
	}
	if got, err := index.HighWater(); err != nil || got != 0 {
		t.Fatalf("legacy HighWater() = %d, %v", got, err)
	}
	assertLookup(t, legacyTwo, func() (Locator, error) {
		return index.VisibleAt(legacyTwo.RowID, 0)
	})

	sequenced := locator("row_legacy", 3, 5, row.StateDeleted)
	other := locator("row_other", 1, 7, row.StateLive)
	if _, err := index.Append(2, []Locator{sequenced, other}); err != nil {
		t.Fatal(err)
	}
	if got, err := index.HighWater(); err != nil || got != 7 {
		t.Fatalf("sequenced HighWater() = %d, %v", got, err)
	}
	assertLookup(t, legacyTwo, func() (Locator, error) {
		return index.VisibleAt(legacyTwo.RowID, 4)
	})
	assertLookup(t, sequenced, func() (Locator, error) {
		return index.VisibleAt(sequenced.RowID, 5)
	})

	historical := locator("row_historical", 1, 3, row.StateLive)
	if _, err := index.Append(3, []Locator{historical}); !errors.Is(err, ErrConflict) {
		t.Fatalf("late historical append error = %v", err)
	}
	if got, err := index.HighWater(); err != nil || got != 7 {
		t.Fatalf("HighWater after late historical conflict = %d, %v", got, err)
	}
	if _, err := index.ByRevision(historical.RowID, historical.Revision); !errors.Is(err, ErrNotFound) {
		t.Fatalf("late historical revision leaked: %v", err)
	}
	receipt, err := index.Append(4, []Locator{sequenced})
	if err != nil || receipt.Changed {
		t.Fatalf("idempotent Append() = %+v, %v", receipt, err)
	}
	transactions, err := set.ScanCommitted()
	if err != nil || len(transactions) != 2 {
		t.Fatalf("snapshot metadata transactions = %d, %v", len(transactions), err)
	}
}

func TestLegacyZeroSequenceOnlyHasRevisionKey(t *testing.T) {
	set, _, _, index := newTestIndex(t)
	legacy := locator("row_legacy", 1, 0, row.StateLive)
	if _, err := index.Append(1, []Locator{legacy}); err != nil {
		t.Fatal(err)
	}
	assertLookup(t, legacy, func() (Locator, error) {
		return index.ByRevision(legacy.RowID, legacy.Revision)
	})
	if _, err := index.ByCommit(legacy.RowID, 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ByCommit(sequence 0) error = %v", err)
	}
	if _, err := index.AsOfCommit(legacy.RowID, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("legacy AsOfCommit error = %v", err)
	}
	lateLegacy := locator("row_late", 1, 0, row.StateLive)
	if _, err := index.Append(2, []Locator{lateLegacy}); !errors.Is(err, ErrConflict) {
		t.Fatalf("late legacy import error = %v", err)
	}
	if _, err := index.ByRevision(lateLegacy.RowID, lateLegacy.Revision); !errors.Is(err, ErrNotFound) {
		t.Fatalf("late legacy revision leaked: %v", err)
	}
	if highWater, err := index.HighWater(); err != nil || highWater != 0 {
		t.Fatalf("HighWater after late legacy conflict = %d, %v", highWater, err)
	}
	next := legacy
	next.Revision = 2
	next.CommitSequence = 5
	if _, err := index.Append(3, []Locator{next}); err != nil {
		t.Fatal(err)
	}
	assertLookup(t, next, func() (Locator, error) {
		return index.AsOfCommit(next.RowID, 5)
	})
	transactions, err := set.ScanCommitted()
	if err != nil || len(transactions) != 2 {
		t.Fatalf("transactions after sealed legacy conflict = %d, %v", len(transactions), err)
	}
}

func TestAppendRejectsImmutableAndIdentityConflictBeforeWAL(t *testing.T) {
	set, _, _, index := newTestIndex(t)
	first := locator("row_one", 1, 1, row.StateLive)
	if _, err := index.Append(1, []Locator{first}); err != nil {
		t.Fatal(err)
	}
	conflicting := first
	conflicting.CommitSequence = 2
	second := first
	second.Revision = 2
	second.CommitSequence = 3
	if _, err := index.Append(2, []Locator{conflicting, second}); !errors.Is(err, ErrConflict) {
		t.Fatalf("immutable conflict error = %v", err)
	}
	if _, err := index.ByRevision(first.RowID, 2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("batch leaked second revision: %v", err)
	}
	moved := second
	moved.TableID = "tbl_other"
	if _, err := index.Append(3, []Locator{moved}); !errors.Is(err, ErrConflict) {
		t.Fatalf("identity drift error = %v", err)
	}
	if _, err := index.Append(4, []Locator{second, second}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate batch error = %v", err)
	}
	transactions, err := set.ScanCommitted()
	if err != nil || len(transactions) != 1 {
		t.Fatalf("transactions after conflicts = %d, %v", len(transactions), err)
	}
	if highWater, err := index.HighWater(); err != nil || highWater != first.CommitSequence {
		t.Fatalf("HighWater after conflicts = %d, %v", highWater, err)
	}
	assertLookup(t, first, func() (Locator, error) {
		return index.ByRevision(first.RowID, first.Revision)
	})
}

func TestAppendIsIdempotentWithoutAnotherWALTransaction(t *testing.T) {
	set, _, _, index := newTestIndex(t)
	value := locator("row_one", 1, 1, row.StateLive)
	if _, err := index.Append(1, []Locator{value}); err != nil {
		t.Fatal(err)
	}
	receipt, err := index.Append(2, []Locator{value})
	if err != nil || receipt.Changed {
		t.Fatalf("idempotent Append() = %+v, %v", receipt, err)
	}
	transactions, err := set.ScanCommitted()
	if err != nil || len(transactions) != 1 {
		t.Fatalf("idempotent transaction count = %d, %v", len(transactions), err)
	}
}

func TestLargeVersionIndexMatchesFloorReferenceModelAndSplits(t *testing.T) {
	_, _, runtime, index := newTestIndex(t)
	locators := make([]Locator, 0, 1200)
	model := make(map[string][]Locator)
	for rowNumber := 0; rowNumber < 60; rowNumber++ {
		rowID := fmt.Sprintf("row_%03d", rowNumber)
		for revision := uint64(1); revision <= 20; revision++ {
			sequence := revision * 3
			if revision%5 == 0 {
				sequence--
			}
			value := locator(rowID, revision, sequence, row.StateLive)
			locators = append(locators, value)
			model[rowID] = append(model[rowID], value)
		}
	}
	if _, err := index.Append(1, locators); err != nil {
		t.Fatal(err)
	}
	root, err := runtime.Read(runtime.State().RootPageID)
	if err != nil {
		t.Fatal(err)
	}
	node, err := btree.Decode(root)
	if err != nil || node.Kind != btree.KindInternal {
		t.Fatalf("root after split = %+v, %v", node, err)
	}
	for rowID, versions := range model {
		for _, target := range []uint64{1, 3, 14, 15, 29, 60, 100} {
			var want Locator
			found := false
			for _, value := range versions {
				if value.CommitSequence <= target &&
					(!found || value.CommitSequence > want.CommitSequence ||
						(value.CommitSequence == want.CommitSequence && value.Revision > want.Revision)) {
					want, found = value, true
				}
			}
			got, err := index.AsOfCommit(rowID, target)
			if !found {
				if !errors.Is(err, ErrNotFound) {
					t.Fatalf("AsOfCommit(%s,%d) error = %v", rowID, target, err)
				}
				continue
			}
			if err != nil || got != want {
				t.Fatalf("AsOfCommit(%s,%d) = %+v, %v; want %+v", rowID, target, got, err, want)
			}
		}
	}
}

func TestConcurrentSameRevisionOnlyOneLocatorCommits(t *testing.T) {
	set, _, _, index := newTestIndex(t)
	var succeeded atomic.Uint64
	var conflicted atomic.Uint64
	var wait sync.WaitGroup
	for number := 0; number < 32; number++ {
		wait.Add(1)
		go func(number int) {
			defer wait.Done()
			value := locator("row_one", 1, uint64(number+1), row.StateLive)
			_, err := index.Append(uint64(number+1), []Locator{value})
			switch {
			case err == nil:
				succeeded.Add(1)
			case errors.Is(err, ErrConflict):
				conflicted.Add(1)
			default:
				t.Errorf("Append(%d) error = %v", number, err)
			}
		}(number)
	}
	wait.Wait()
	if succeeded.Load() != 1 || conflicted.Load() != 31 {
		t.Fatalf("same-revision results = success %d conflict %d", succeeded.Load(), conflicted.Load())
	}
	transactions, err := set.ScanCommitted()
	if err != nil || len(transactions) != 1 {
		t.Fatalf("same-revision transactions = %d, %v", len(transactions), err)
	}
	highWater, err := index.HighWater()
	if err != nil || highWater == 0 {
		t.Fatalf("concurrent HighWater() = %d, %v", highWater, err)
	}
	committed, err := index.ByRevision("row_one", 1)
	if err != nil || committed.CommitSequence != highWater {
		t.Fatalf("committed locator/high-water = %+v/%d, %v", committed, highWater, err)
	}
}

func TestCrashBeforeFlushReopensVersionIndex(t *testing.T) {
	directory := t.TempDir()
	walPath := filepath.Join(directory, "wal")
	pagePath := filepath.Join(directory, "versions.pages")
	set, manager, runtime, index := openTestIndex(t, walPath, pagePath, false)
	value := locator("row_one", 7, 11, row.StateDeleted)
	if _, err := index.Append(1, []Locator{value}); err != nil {
		t.Fatal(err)
	}
	if count, err := manager.PageCount(); err != nil || count != 1 {
		t.Fatalf("pre-crash Page count = %d, %v", count, err)
	}
	committed := runtime.State()
	if err := set.Close(); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	reopenedSet, reopenedManager, reopenedRuntime, reopened := openTestIndex(t, walPath, pagePath, true)
	defer func() { _ = reopenedSet.Close(); _ = reopenedManager.Close() }()
	if reopenedRuntime.State() != committed {
		t.Fatalf("reopened state = %+v, want %+v", reopenedRuntime.State(), committed)
	}
	assertLookup(t, value, func() (Locator, error) {
		return reopened.ByRevision(value.RowID, value.Revision)
	})
	assertLookup(t, value, func() (Locator, error) {
		return reopened.AsOfCommit(value.RowID, value.CommitSequence)
	})
	if highWater, err := reopened.HighWater(); err != nil || highWater != value.CommitSequence {
		t.Fatalf("reopened HighWater() = %d, %v", highWater, err)
	}
}

func TestLookupRejectsCorruptTreeWithoutFallback(t *testing.T) {
	directory := t.TempDir()
	walPath := filepath.Join(directory, "wal")
	pagePath := filepath.Join(directory, "versions.pages")
	set, manager, runtime, index := openTestIndex(t, walPath, pagePath, false)
	value := locator("row_one", 1, 1, row.StateLive)
	if _, err := index.Append(1, []Locator{value}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.FlushDirty(16); err != nil {
		t.Fatal(err)
	}
	rootID := runtime.State().RootPageID
	root, err := manager.Read(rootID)
	if err != nil {
		t.Fatal(err)
	}
	root.Payload[0] ^= 0xff
	if err := manager.Write(root); err != nil {
		t.Fatal(err)
	}
	if err := manager.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := set.Close(); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	reopenedSet, reopenedManager, _, reopened := openTestIndex(t, walPath, pagePath, true)
	defer func() { _ = reopenedSet.Close(); _ = reopenedManager.Close() }()
	if _, err := reopened.ByRevision(value.RowID, value.Revision); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("corrupt lookup error = %v", err)
	}
	if _, err := reopened.HighWater(); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("corrupt HighWater error = %v", err)
	}
}

func TestLocatorCodecRejectsCorruptionCorpus(t *testing.T) {
	want := locator("row_one", 7, 11, row.StateSuperseded)
	encoded, err := encodeLocator(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeLocator(encoded)
	if err != nil || got != want {
		t.Fatalf("locator round trip = %+v, %v", got, err)
	}
	corpus := [][]byte{
		encoded[:len(encoded)-1],
		append(append([]byte(nil), encoded...), 0),
		append([]byte(nil), encoded...),
		append([]byte(nil), encoded...),
		append([]byte(nil), encoded...),
	}
	corpus[2][8] = 99
	corpus[3][10] = 99
	corpus[4][12] = 1
	for number, value := range corpus {
		if _, err := decodeLocator(value); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("corpus[%d] error = %v", number, err)
		}
	}
	highWater := encodeHighWater(42)
	if got, err := decodeHighWater(highWater); err != nil || got != 42 {
		t.Fatalf("high-water round trip = %d, %v", got, err)
	}
	highWaterCorpus := [][]byte{
		highWater[:len(highWater)-1],
		append(append([]byte(nil), highWater...), 0),
		append([]byte(nil), highWater...),
		append([]byte(nil), highWater...),
	}
	highWaterCorpus[2][0] ^= 0xff
	highWaterCorpus[3][10] = 1
	for number, value := range highWaterCorpus {
		if _, err := decodeHighWater(value); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("high-water corpus[%d] error = %v", number, err)
		}
	}
}

func locator(id string, revision, sequence uint64, state row.State) Locator {
	return Locator{
		DatabaseID: "db_memory", TableID: "tbl_notes", RowID: id,
		SchemaRevision: 3, Revision: revision, CommitSequence: sequence, State: state,
	}
}

func newTestIndex(t *testing.T) (*wal.SegmentSet, *page.Manager, *treecommit.Runtime, *Index) {
	t.Helper()
	directory := t.TempDir()
	set, manager, runtime, index := openTestIndex(
		t, filepath.Join(directory, "wal"), filepath.Join(directory, "versions.pages"), false,
	)
	t.Cleanup(func() { _ = set.Close(); _ = manager.Close() })
	return set, manager, runtime, index
}

func openTestIndex(t *testing.T, walPath, pagePath string, reopen bool) (*wal.SegmentSet, *page.Manager, *treecommit.Runtime, *Index) {
	t.Helper()
	var (
		set     *wal.SegmentSet
		manager *page.Manager
		err     error
	)
	if reopen {
		set, err = wal.OpenSegmentSet(walPath, 0)
	} else {
		set, err = wal.CreateSegmentSet(walPath, 0)
	}
	if err != nil {
		t.Fatal(err)
	}
	if reopen {
		manager, err = page.Open(pagePath, testSpaceID)
	} else {
		manager, err = page.Create(pagePath, testSpaceID)
	}
	if err != nil {
		_ = set.Close()
		t.Fatal(err)
	}
	runtime, _, err := treecommit.OpenRuntime(set, manager, treecommit.RuntimeConfig{
		SpaceID: testSpaceID, Capacity: 128, OldFrames: 64,
	})
	if err != nil {
		_ = set.Close()
		_ = manager.Close()
		t.Fatal(err)
	}
	index, err := Open(runtime)
	if err != nil {
		_ = set.Close()
		_ = manager.Close()
		t.Fatal(err)
	}
	return set, manager, runtime, index
}

func assertLookup(t *testing.T, want Locator, lookup func() (Locator, error)) {
	t.Helper()
	got, err := lookup()
	if err != nil || got != want {
		t.Fatalf("lookup = %+v, %v; want %+v", got, err, want)
	}
}
