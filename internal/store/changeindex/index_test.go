package changeindex

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/HW-Yue/Memora/internal/store/btree"
	"github.com/HW-Yue/Memora/internal/store/page"
	"github.com/HW-Yue/Memora/internal/store/treecommit"
	"github.com/HW-Yue/Memora/internal/store/wal"
)

const testSpaceID = uint64(0x4d454d434847)

func TestBootstrapCreatesSequenceAndTransactionAuthority(t *testing.T) {
	_, _, runtime, index := newTestIndex(t)
	want := locators(1, 7)
	receipt, err := index.Bootstrap(1, want)
	if err != nil || !receipt.Changed || runtime.State().RootPageID == 0 {
		t.Fatalf("Bootstrap() = %+v, %v", receipt, err)
	}
	highWater, err := index.HighWater()
	if err != nil || highWater != 7 {
		t.Fatalf("HighWater() = %d, %v", highWater, err)
	}
	for _, locator := range want {
		assertLookup(t, locator, func() (Locator, error) {
			return index.LookupSequence(locator.CommitSequence)
		})
		assertLookup(t, locator, func() (Locator, error) {
			return index.LookupTransaction(locator.TransactionID)
		})
	}
	got, err := index.Range(2, 7, 3)
	if err != nil || !reflect.DeepEqual(got, want[2:5]) {
		t.Fatalf("Range(2, 7, 3) = %+v, %v", got, err)
	}
	if _, err := index.Bootstrap(2, want); !errors.Is(err, ErrConflict) {
		t.Fatalf("second Bootstrap error = %v", err)
	}
}

func TestBootstrapEmptyCreatesZeroHighWater(t *testing.T) {
	_, _, runtime, index := newTestIndex(t)
	receipt, err := index.Bootstrap(1, nil)
	if err != nil || !receipt.Changed || runtime.State().RootPageID == 0 {
		t.Fatalf("Bootstrap(empty) = %+v, %v", receipt, err)
	}
	highWater, err := index.HighWater()
	if err != nil || highWater != 0 {
		t.Fatalf("HighWater() = %d, %v", highWater, err)
	}
	got, err := index.Range(0, 0, 1)
	if err != nil || len(got) != 0 {
		t.Fatalf("Range(empty) = %+v, %v", got, err)
	}
}

func TestAppendIsContiguousAtomicAndIdempotent(t *testing.T) {
	set, _, _, index := newTestIndex(t)
	if _, err := index.Bootstrap(1, locators(1, 2)); err != nil {
		t.Fatal(err)
	}
	added := locators(3, 5)
	receipt, err := index.Append(2, added)
	if err != nil || !receipt.Changed {
		t.Fatalf("Append() = %+v, %v", receipt, err)
	}
	receipt, err = index.Append(3, added)
	if err != nil || receipt.Changed {
		t.Fatalf("idempotent Append() = %+v, %v", receipt, err)
	}
	transactions, err := set.ScanCommitted()
	if err != nil || len(transactions) != 2 {
		t.Fatalf("committed transactions = %d, %v", len(transactions), err)
	}

	gap := locator(7)
	if _, err := index.Append(4, []Locator{gap}); !errors.Is(err, ErrConflict) {
		t.Fatalf("gap Append error = %v", err)
	}
	duplicateTransaction := locator(6)
	duplicateTransaction.TransactionID = locator(2).TransactionID
	if _, err := index.Append(5, []Locator{duplicateTransaction}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate transaction Append error = %v", err)
	}
	changedIdentity := locator(5)
	changedIdentity.Checksum = fmt.Sprintf("%064x", 999)
	if _, err := index.Append(6, []Locator{changedIdentity}); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed identity Append error = %v", err)
	}
	highWater, err := index.HighWater()
	if err != nil || highWater != 5 {
		t.Fatalf("HighWater after rejected batches = %d, %v", highWater, err)
	}
}

func TestLargeIndexSplitsAndMatchesRangeReference(t *testing.T) {
	_, _, runtime, index := newTestIndex(t)
	want := locators(1, 700)
	if _, err := index.Bootstrap(1, want); err != nil {
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
	for after := uint64(0); after < 700; after += 37 {
		through := after + 101
		if through > 700 {
			through = 700
		}
		got, err := index.Range(after, through, 29)
		end := after + uint64(29)
		if end > through {
			end = through
		}
		expected := want[after:end]
		if err != nil || !reflect.DeepEqual(got, expected) {
			t.Fatalf("Range(%d, %d) = %d entries, %v; want %d", after, through, len(got), err, len(expected))
		}
	}
}

func TestCrashBeforeFlushReopensIndexFromWAL(t *testing.T) {
	directory := t.TempDir()
	walPath := filepath.Join(directory, "wal")
	pagePath := filepath.Join(directory, "changes.pages")
	set, manager, runtime, index := openTestIndex(t, walPath, pagePath, false)
	want := locators(1, 32)
	if _, err := index.Bootstrap(1, want[:3]); err != nil {
		t.Fatal(err)
	}
	if _, err := index.Append(2, want[3:]); err != nil {
		t.Fatal(err)
	}
	committed := runtime.State()
	if count, err := manager.PageCount(); err != nil || count != 1 {
		t.Fatalf("PageCount before crash = %d, %v", count, err)
	}
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
	got, err := reopened.Range(0, 32, 32)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("reopened Range = %d entries, %v", len(got), err)
	}
}

func TestReadRejectsCorruptTreeWithoutFallback(t *testing.T) {
	directory := t.TempDir()
	walPath := filepath.Join(directory, "wal")
	pagePath := filepath.Join(directory, "changes.pages")
	set, manager, runtime, index := openTestIndex(t, walPath, pagePath, false)
	if _, err := index.Bootstrap(1, locators(1, 4)); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.FlushDirty(16); err != nil {
		t.Fatal(err)
	}
	root, err := manager.Read(runtime.State().RootPageID)
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
	if _, err := reopened.Range(0, 4, 4); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("corrupt Range error = %v", err)
	}
}

func TestConcurrentReadsObserveCommittedSnapshots(t *testing.T) {
	_, _, _, index := newTestIndex(t)
	if _, err := index.Bootstrap(1, locators(1, 8)); err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	for reader := 0; reader < 16; reader++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for number := 0; number < 50; number++ {
				highWater, err := index.HighWater()
				if err != nil || highWater < 8 || highWater > 16 {
					t.Errorf("concurrent HighWater = %d, %v", highWater, err)
					return
				}
				if _, err := index.Range(0, highWater, 16); err != nil {
					t.Errorf("concurrent Range error = %v", err)
					return
				}
			}
		}()
	}
	for sequence := uint64(9); sequence <= 16; sequence++ {
		if _, err := index.Append(sequence-7, []Locator{locator(sequence)}); err != nil {
			t.Fatal(err)
		}
	}
	wait.Wait()
}

func TestLocatorCodecRejectsNonCanonicalAndCorruptPayloads(t *testing.T) {
	want := locator(9)
	encoded, err := encodeLocator(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeLocator(encoded)
	if err != nil || got != want {
		t.Fatalf("codec round trip = %+v, %v", got, err)
	}
	corpus := [][]byte{
		encoded[:len(encoded)-1],
		append(append([]byte(nil), encoded...), 0),
		[]byte(fmt.Sprintf(`{"commit_sequence":9,"transaction_id":"%s","checksum":"%s","unknown":true}`, want.TransactionID, want.Checksum)),
		[]byte(fmt.Sprintf(`{ "commit_sequence":9,"transaction_id":"%s","checksum":"%s"}`, want.TransactionID, want.Checksum)),
	}
	for number, value := range corpus {
		if _, err := decodeLocator(value); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("corpus[%d] error = %v", number, err)
		}
	}
}

func locators(first, last uint64) []Locator {
	result := make([]Locator, 0, last-first+1)
	for sequence := first; sequence <= last; sequence++ {
		result = append(result, locator(sequence))
	}
	return result
}

func locator(sequence uint64) Locator {
	return Locator{
		CommitSequence: sequence,
		TransactionID:  fmt.Sprintf("txn_%032x", sequence),
		Checksum:       fmt.Sprintf("%064x", sequence),
	}
}

func newTestIndex(t *testing.T) (*wal.SegmentSet, *page.Manager, *treecommit.Runtime, *Index) {
	t.Helper()
	directory := t.TempDir()
	set, manager, runtime, index := openTestIndex(
		t, filepath.Join(directory, "wal"), filepath.Join(directory, "changes.pages"), false,
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
