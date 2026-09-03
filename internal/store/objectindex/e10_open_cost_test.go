package objectindex

import (
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/store/page"
	"github.com/HW-Yue/Memora/internal/store/treecommit"
	"github.com/HW-Yue/Memora/internal/store/wal"
)

// countingStore counts the Pages read through it, so the test can assert what
// opening a Tree costs rather than how long it takes.
type countingStore struct {
	inner *page.Manager
	reads int
}

func (store *countingStore) Read(pageID uint64) (page.Page, error) {
	store.reads++
	return store.inner.Read(pageID)
}

func (store *countingStore) Write(value page.Page) error { return store.inner.Write(value) }
func (store *countingStore) Sync() error                 { return store.inner.Sync() }

// checkpointBarrier is the durability barrier a checkpoint needs, so the
// fixture can leave the redo log in the state production leaves it in: rolled,
// checkpointed and reclaimed. Without it the log still holds every Record ever
// written and reopening replays all of them, which would hide what this test
// measures behind a second, unrelated linear cost.
type checkpointBarrier struct {
	runtime *treecommit.Runtime
	manager *page.Manager
}

func (barrier checkpointBarrier) FlushThrough(recoveryLSN uint64) error {
	report, err := barrier.runtime.FlushDirtyThrough(math.MaxUint64, recoveryLSN)
	if err != nil {
		return err
	}
	if report.Remaining != 0 {
		return fmt.Errorf("%d dirty Pages remain at checkpoint", report.Remaining)
	}
	return barrier.manager.Sync()
}

// TestOpeningATreeDoesNotReadEveryPage is the gate for the last full scan left
// in the storage engine.
//
// treecommit.OpenRuntime rebuilt the set of reusable Pages by reading every
// Page in the space, from FirstDataPageID to NextPageID, and classifying each
// one. That is a full scan of the Page file on every open of every Tree — the
// four generation Trees, the change index, and anything else built on a Runtime
// all pay it — and it grows with the file rather than with the data.
//
// It is usually pure waste as well: a Tree that has only grown has no free
// Pages, so the scan reads the whole file to build an empty set. Measured on
// this fixture at 1785 Pages, it was a third of the cost of opening the Tree.
//
// The reusable set is Tree state and belongs on disk with the rest of it.
// Opening must read the control Page and stop.
//
// The test counts Page reads rather than timing the open because the count is
// the property: a bounded number of reads is what "not a scan" means.
func TestOpeningATreeDoesNotReadEveryPage(t *testing.T) {
	const records = 4000
	directory := t.TempDir()
	walPath := filepath.Join(directory, "wal")
	pagePath := filepath.Join(directory, "objects.pages")

	set, manager, runtime, index := openTestIndex(t, walPath, pagePath, false)
	body := strings.Repeat("x", 400)
	transaction := uint64(1)
	for base := 0; base < records; base += 100 {
		batch := make([]Record, 0, 100)
		for step := 0; step < 100 && base+step < records; step++ {
			batch = append(batch, record(kindRoute, fmt.Sprintf("id-%08d", base+step), body))
		}
		if _, err := index.Put(transaction, batch); err != nil {
			t.Fatal(err)
		}
		transaction++
		if _, err := runtime.FlushDirty(1 << 20); err != nil {
			t.Fatal(err)
		}
	}
	if err := manager.Sync(); err != nil {
		t.Fatal(err)
	}
	// Leave the log as production leaves it: rolled, checkpointed, reclaimed.
	if _, err := set.Roll(); err != nil {
		t.Fatal(err)
	}
	if _, err := set.PublishCheckpoint(checkpointBarrier{runtime: runtime, manager: manager}); err != nil {
		t.Fatal(err)
	}
	if _, err := set.Reclaim(); err != nil {
		t.Fatal(err)
	}
	pages, err := manager.PageCount()
	if err != nil {
		t.Fatal(err)
	}
	if pages < 100 {
		t.Fatalf("fixture grew the Tree to %d Pages, too few to tell a scan from a lookup", pages)
	}
	_ = set.Close()
	_ = manager.Close()

	reopenSet, err := wal.OpenSegmentSet(walPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopenSet.Close() }()
	reopenManager, err := page.Open(pagePath, testSpaceID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopenManager.Close() }()

	counter := &countingStore{inner: reopenManager}
	reopened, _, err := treecommit.OpenRuntime(reopenSet, counter, treecommit.RuntimeConfig{
		SpaceID: testSpaceID, Capacity: 128, OldFrames: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(reopened.FreePageIDs()); got != 0 {
		t.Fatalf("FreePageIDs() = %d, want none for a Tree that only ever grew", got)
	}
	t.Logf("%d 页的树，重开读了 %d 页", pages, counter.reads)
	if uint64(counter.reads) >= pages {
		t.Fatalf(
			"opening a %d-Page Tree read %d Pages; opening must not scale with the file",
			pages, counter.reads,
		)
	}
}
