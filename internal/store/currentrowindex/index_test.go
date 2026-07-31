package currentrowindex

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

const testSpaceID = uint64(51)

func TestBootstrapAcceptsFinalLocatorsAndCreatesAnEmptyAuthority(t *testing.T) {
	_, _, runtime, index := newTestIndex(t)
	final := locator("row_one", 7, 11, row.StateDeleted)
	receipt, err := index.Bootstrap(1, []Locator{final})
	if err != nil || !receipt.Changed || runtime.State().RootPageID == 0 {
		t.Fatalf("Bootstrap(final) = %+v, %v", receipt, err)
	}
	assertLookup(t, final, func() (Locator, error) {
		return index.Lookup(final.TableID, final.RowID)
	})
	if _, err := index.Bootstrap(2, []Locator{final}); !errors.Is(err, ErrConflict) {
		t.Fatalf("Bootstrap(non-empty) error = %v", err)
	}

	_, _, emptyRuntime, empty := newTestIndex(t)
	receipt, err = empty.Bootstrap(1, nil)
	if err != nil || !receipt.Changed || emptyRuntime.State().RootPageID == 0 {
		t.Fatalf("Bootstrap(empty) = %+v, %v", receipt, err)
	}
}

func TestLookupTracksCurrentRevisionStateAndScope(t *testing.T) {
	_, _, _, index := newTestIndex(t)
	live := locator("row_one", 1, 1, row.StateLive)
	receipt, err := index.Apply(1, []Update{{Locator: live}})
	if err != nil || !receipt.Changed || receipt.State.Revision != 1 {
		t.Fatalf("Apply(insert) = %+v, %v", receipt, err)
	}
	assertLookup(t, live, func() (Locator, error) {
		return index.Lookup(live.TableID, live.RowID)
	})
	if _, err := index.Lookup("tbl_other", live.RowID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong-scope lookup error = %v", err)
	}

	deleted := live
	deleted.Revision = 2
	deleted.CommitSequence = 2
	deleted.State = row.StateDeleted
	if _, err := index.Apply(2, []Update{{
		ExpectedRevision: 1, Locator: deleted,
	}}); err != nil {
		t.Fatal(err)
	}
	assertLookup(t, deleted, func() (Locator, error) {
		return index.Lookup(deleted.TableID, deleted.RowID)
	})

	superseded := deleted
	superseded.Revision = 3
	superseded.CommitSequence = 3
	superseded.State = row.StateSuperseded
	if _, err := index.Apply(3, []Update{{
		ExpectedRevision: 2, Locator: superseded,
	}}); err != nil {
		t.Fatal(err)
	}
	assertLookup(t, superseded, func() (Locator, error) {
		return index.Lookup(superseded.TableID, superseded.RowID)
	})
	if _, err := index.Lookup(live.TableID, "row_missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing lookup error = %v", err)
	}
}

func TestApplyRejectsStaleBatchBeforeWALAndPreservesAllRows(t *testing.T) {
	set, _, _, index := newTestIndex(t)
	one := locator("row_one", 1, 1, row.StateLive)
	two := locator("row_two", 1, 1, row.StateLive)
	if _, err := index.Apply(1, []Update{{Locator: one}, {Locator: two}}); err != nil {
		t.Fatal(err)
	}
	oneNext := one
	oneNext.Revision, oneNext.CommitSequence = 2, 2
	twoNext := two
	twoNext.Revision, twoNext.CommitSequence = 2, 2
	if _, err := index.Apply(2, []Update{
		{ExpectedRevision: 1, Locator: oneNext},
		{ExpectedRevision: 99, Locator: twoNext},
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("Apply(stale batch) error = %v", err)
	}
	transactions, err := set.ScanCommitted()
	if err != nil || len(transactions) != 1 {
		t.Fatalf("transactions after stale batch = %d, %v", len(transactions), err)
	}
	assertLookup(t, one, func() (Locator, error) { return index.Lookup(one.TableID, one.RowID) })
	assertLookup(t, two, func() (Locator, error) { return index.Lookup(two.TableID, two.RowID) })
}

func TestApplyRejectsDuplicateAndInvalidTransition(t *testing.T) {
	set, _, _, index := newTestIndex(t)
	value := locator("row_one", 1, 1, row.StateLive)
	if _, err := index.Apply(1, []Update{{Locator: value}, {Locator: value}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate batch error = %v", err)
	}
	if transactions, err := set.ScanCommitted(); err != nil || len(transactions) != 0 {
		t.Fatalf("transactions after duplicate = %d, %v", len(transactions), err)
	}
	if _, err := index.Apply(2, []Update{{Locator: value}}); err != nil {
		t.Fatal(err)
	}
	jumped := value
	jumped.Revision = 3
	jumped.CommitSequence = 2
	if _, err := index.Apply(3, []Update{{
		ExpectedRevision: 1, Locator: jumped,
	}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("revision jump error = %v", err)
	}
	regressed := value
	regressed.Revision = 2
	if _, err := index.Apply(4, []Update{{
		ExpectedRevision: 1, Locator: regressed,
	}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("commit sequence regression error = %v", err)
	}
	schemaRegressed := value
	schemaRegressed.Revision = 2
	schemaRegressed.CommitSequence = 2
	schemaRegressed.SchemaRevision = 2
	if _, err := index.Apply(5, []Update{{
		ExpectedRevision: 1, Locator: schemaRegressed,
	}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("Schema revision regression error = %v", err)
	}
}

func TestApplyIsIdempotentWithoutAnotherWALTransaction(t *testing.T) {
	set, _, _, index := newTestIndex(t)
	value := locator("row_one", 1, 1, row.StateLive)
	if _, err := index.Apply(1, []Update{{Locator: value}}); err != nil {
		t.Fatal(err)
	}
	receipt, err := index.Apply(2, []Update{{Locator: value}})
	if err != nil || receipt.Changed {
		t.Fatalf("idempotent Apply() = %+v, %v", receipt, err)
	}
	transactions, err := set.ScanCommitted()
	if err != nil || len(transactions) != 1 {
		t.Fatalf("idempotent transaction count = %d, %v", len(transactions), err)
	}
}

func TestLegacyZeroCommitSequenceCanBeIndexedButNextRevisionMustAdvance(t *testing.T) {
	_, _, _, index := newTestIndex(t)
	legacy := locator("row_legacy", 1, 0, row.StateLive)
	if _, err := index.Apply(1, []Update{{Locator: legacy}}); err != nil {
		t.Fatalf("Apply(legacy) error = %v", err)
	}
	assertLookup(t, legacy, func() (Locator, error) {
		return index.Lookup(legacy.TableID, legacy.RowID)
	})
	invalid := legacy
	invalid.Revision = 2
	if _, err := index.Apply(2, []Update{{
		ExpectedRevision: 1, Locator: invalid,
	}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("zero-sequence mutation error = %v", err)
	}
	next := invalid
	next.CommitSequence = 1
	if _, err := index.Apply(3, []Update{{
		ExpectedRevision: 1, Locator: next,
	}}); err != nil {
		t.Fatalf("Apply(first sequenced revision) error = %v", err)
	}
	assertLookup(t, next, func() (Locator, error) {
		return index.Lookup(next.TableID, next.RowID)
	})
}

func TestLargeCurrentIndexMatchesReferenceModelAndSplits(t *testing.T) {
	_, _, runtime, index := newTestIndex(t)
	updates := make([]Update, 0, 700)
	model := make(map[string]Locator, 700)
	for number := 0; number < 700; number++ {
		value := locator(fmt.Sprintf("row_%04d", number), 1, uint64(number+1), row.StateLive)
		updates = append(updates, Update{Locator: value})
		model[value.RowID] = value
	}
	if _, err := index.Apply(1, updates); err != nil {
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

	updates = updates[:0]
	for number := 0; number < 700; number += 3 {
		id := fmt.Sprintf("row_%04d", number)
		value := model[id]
		value.Revision++
		value.CommitSequence += 1000
		if number%2 == 0 {
			value.State = row.StateDeleted
		}
		model[id] = value
		updates = append(updates, Update{ExpectedRevision: 1, Locator: value})
	}
	if _, err := index.Apply(2, updates); err != nil {
		t.Fatal(err)
	}
	for id, want := range model {
		id, want := id, want
		assertLookup(t, want, func() (Locator, error) {
			return index.Lookup(want.TableID, id)
		})
	}
}

func TestConcurrentSameBaseOnlyOneUpdateCommits(t *testing.T) {
	set, _, _, index := newTestIndex(t)
	initial := locator("row_one", 1, 1, row.StateLive)
	if _, err := index.Apply(1, []Update{{Locator: initial}}); err != nil {
		t.Fatal(err)
	}
	var succeeded atomic.Uint64
	var conflicted atomic.Uint64
	var wait sync.WaitGroup
	for number := 0; number < 32; number++ {
		wait.Add(1)
		go func(number int) {
			defer wait.Done()
			next := initial
			next.Revision = 2
			next.CommitSequence = uint64(100 + number)
			_, err := index.Apply(uint64(2+number), []Update{{
				ExpectedRevision: 1, Locator: next,
			}})
			switch {
			case err == nil:
				succeeded.Add(1)
			case errors.Is(err, ErrConflict):
				conflicted.Add(1)
			default:
				t.Errorf("Apply(%d) error = %v", number, err)
			}
		}(number)
	}
	wait.Wait()
	if succeeded.Load() != 1 || conflicted.Load() != 31 {
		t.Fatalf("same-base results = success %d conflict %d", succeeded.Load(), conflicted.Load())
	}
	transactions, err := set.ScanCommitted()
	if err != nil || len(transactions) != 2 {
		t.Fatalf("same-base transactions = %d, %v", len(transactions), err)
	}
}

func TestCrashBeforeFlushReopensCurrentIndex(t *testing.T) {
	directory := t.TempDir()
	walPath := filepath.Join(directory, "wal")
	pagePath := filepath.Join(directory, "current.pages")
	set, manager, runtime, index := openTestIndex(t, walPath, pagePath, false)
	value := locator("row_one", 1, 9, row.StateLive)
	if _, err := index.Apply(1, []Update{{Locator: value}}); err != nil {
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
		return reopened.Lookup(value.TableID, value.RowID)
	})
}

func TestLookupRejectsCorruptTreeWithoutFallback(t *testing.T) {
	directory := t.TempDir()
	walPath := filepath.Join(directory, "wal")
	pagePath := filepath.Join(directory, "current.pages")
	set, manager, runtime, index := openTestIndex(t, walPath, pagePath, false)
	value := locator("row_one", 1, 1, row.StateLive)
	if _, err := index.Apply(1, []Update{{Locator: value}}); err != nil {
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
	if _, err := reopened.Lookup(value.TableID, value.RowID); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("corrupt lookup error = %v", err)
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
}

func locator(id string, revision, commit uint64, state row.State) Locator {
	return Locator{
		DatabaseID: "db_memory", TableID: "tbl_notes", RowID: id,
		SchemaRevision: 3, Revision: revision, CommitSequence: commit, State: state,
	}
}

func newTestIndex(t *testing.T) (*wal.SegmentSet, *page.Manager, *treecommit.Runtime, *Index) {
	t.Helper()
	directory := t.TempDir()
	set, manager, runtime, index := openTestIndex(
		t, filepath.Join(directory, "wal"), filepath.Join(directory, "current.pages"), false,
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
