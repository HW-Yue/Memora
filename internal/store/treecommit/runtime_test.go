package treecommit

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"sync"
	"testing"

	"github.com/HW-Yue/Memora/internal/store/btree"
	"github.com/HW-Yue/Memora/internal/store/buffer"
	"github.com/HW-Yue/Memora/internal/store/page"
	"github.com/HW-Yue/Memora/internal/store/treecontrol"
	"github.com/HW-Yue/Memora/internal/store/wal"
)

func TestRuntimeCommitPublishesDurablePagesAndReopenRecovers(t *testing.T) {
	directory := t.TempDir()
	walPath := filepath.Join(directory, "wal")
	pagePath := filepath.Join(directory, "tree.pages")

	set, err := wal.CreateSegmentSet(walPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := page.Create(pagePath, 7)
	if err != nil {
		t.Fatal(err)
	}
	runtime, report, err := OpenRuntime(set, manager, RuntimeConfig{
		SpaceID: 7, Capacity: 4, OldFrames: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report != (wal.RecoveryReport{}) {
		t.Fatalf("initial recovery = %+v", report)
	}
	if got := runtime.State(); got != treecontrol.Bootstrap(7) {
		t.Fatalf("bootstrap state = %+v", got)
	}
	if count, err := manager.PageCount(); err != nil || count != 1 {
		t.Fatalf("bootstrap Page count = %d, %v", count, err)
	}

	receipt, err := runtime.Commit(1, btree.MutationPlan{
		RootPageID: 2,
		NextPageID: 3,
		Changes: []btree.PageChange{{
			Page: commitNodePage(t, 7, 1, 2, btree.KindLeaf),
		}},
		Allocated: []uint64{2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.WAL.TransactionID != 1 || receipt.WAL.DurableLSN == 0 ||
		receipt.State.Revision != 1 || receipt.State.RootPageID != 2 ||
		receipt.State.NextPageID != 3 {
		t.Fatalf("receipt = %+v", receipt)
	}
	root, err := runtime.Read(2)
	if err != nil {
		t.Fatal(err)
	}
	if root.Header.LSN != receipt.WAL.FirstLSN ||
		receipt.State.LSN <= root.Header.LSN ||
		receipt.State.LSN >= receipt.WAL.CommitLSN {
		t.Fatalf("root/control/WAL LSN = %d/%d/%+v", root.Header.LSN, receipt.State.LSN, receipt.WAL)
	}

	// Simulate a crash before dirty Buffer frames reach the Page Store. Only the
	// bootstrap control is present on disk; reopen must converge from durable WAL.
	if count, err := manager.PageCount(); err != nil || count != 1 {
		t.Fatalf("pre-crash Page count = %d, %v", count, err)
	}
	if err := set.Close(); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}

	reopenedSet, err := wal.OpenSegmentSet(walPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopenedSet.Close() }()
	reopenedManager, err := page.Open(pagePath, 7)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopenedManager.Close() }()
	reopened, report, err := OpenRuntime(reopenedSet, reopenedManager, RuntimeConfig{
		SpaceID: 7, Capacity: 4, OldFrames: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Transactions != 1 || report.ControlWrites != 1 || report.PageWrites != 1 {
		t.Fatalf("recovery report = %+v", report)
	}
	if got := reopened.State(); got != receipt.State {
		t.Fatalf("reopened state = %+v, want %+v", got, receipt.State)
	}
	recoveredRoot, err := reopened.Read(2)
	if err != nil || recoveredRoot.Header.LSN != root.Header.LSN {
		t.Fatalf("recovered root = %+v, %v", recoveredRoot.Header, err)
	}

	nextPage := commitNodePage(t, 7, 1, 2, btree.KindLeaf)
	next, err := reopened.Commit(2, btree.MutationPlan{
		RootPageID: 2,
		NextPageID: 3,
		Changes: []btree.PageChange{{
			Page: nextPage, ExpectedLSN: recoveredRoot.Header.LSN,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if next.State.Revision != 2 || next.State.LSN <= receipt.State.LSN {
		t.Fatalf("continued state = %+v", next.State)
	}
}

func TestRuntimePoisonsAfterDurableWALCannotPublish(t *testing.T) {
	directory := t.TempDir()
	walPath := filepath.Join(directory, "wal")
	pagePath := filepath.Join(directory, "tree.pages")
	set, err := wal.CreateSegmentSet(walPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := page.Create(pagePath, 9)
	if err != nil {
		t.Fatal(err)
	}
	runtime, _, err := OpenRuntime(set, manager, RuntimeConfig{
		SpaceID: 9, Capacity: 1, OldFrames: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	plan := btree.MutationPlan{
		RootPageID: 2,
		NextPageID: 3,
		Changes: []btree.PageChange{{
			Page: commitNodePage(t, 9, 1, 2, btree.KindLeaf),
		}},
		Allocated: []uint64{2},
	}
	if _, err := runtime.Commit(1, plan); !errors.Is(err, buffer.ErrPoolFull) {
		t.Fatalf("Commit() error = %v, want ErrPoolFull", err)
	}
	if got := runtime.State(); got != treecontrol.Bootstrap(9) {
		t.Fatalf("poisoned runtime published state %+v", got)
	}
	if _, err := runtime.Commit(2, plan); !errors.Is(err, ErrRuntimePoisoned) {
		t.Fatalf("Commit(after poison) error = %v", err)
	}
	transactions, err := set.ScanCommitted()
	if err != nil || len(transactions) != 1 {
		t.Fatalf("durable transactions = %d, %v", len(transactions), err)
	}

	if err := set.Close(); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	reopenedSet, err := wal.OpenSegmentSet(walPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopenedSet.Close() }()
	reopenedManager, err := page.Open(pagePath, 9)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopenedManager.Close() }()
	reopened, report, err := OpenRuntime(reopenedSet, reopenedManager, RuntimeConfig{
		SpaceID: 9, Capacity: 2, OldFrames: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Transactions != 1 || reopened.State().Revision != 1 {
		t.Fatalf("reopen did not converge: report=%+v state=%+v", report, reopened.State())
	}
}

func TestRuntimeRejectsBeforeWALWithoutPoisoning(t *testing.T) {
	directory := t.TempDir()
	set, err := wal.CreateSegmentSet(filepath.Join(directory, "wal"), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = set.Close() }()
	manager, err := page.Create(filepath.Join(directory, "tree.pages"), 11)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = manager.Close() }()
	runtime, _, err := OpenRuntime(set, manager, RuntimeConfig{
		SpaceID: 11, Capacity: 3, OldFrames: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Commit(1, btree.MutationPlan{}); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("invalid Commit() error = %v", err)
	}
	if transactions, err := set.ScanCommitted(); err != nil || len(transactions) != 0 {
		t.Fatalf("invalid plan wrote WAL: transactions=%d err=%v", len(transactions), err)
	}
	valid := btree.MutationPlan{
		RootPageID: 2,
		NextPageID: 3,
		Changes: []btree.PageChange{{
			Page: commitNodePage(t, 11, 1, 2, btree.KindLeaf),
		}},
		Allocated: []uint64{2},
	}
	if _, err := runtime.Commit(1, valid); err != nil {
		t.Fatalf("Commit(after pre-WAL rejection) error = %v", err)
	}
	current, err := runtime.Read(2)
	if err != nil {
		t.Fatal(err)
	}
	currentPlan := btree.MutationPlan{
		RootPageID: 2,
		NextPageID: 3,
		Changes: []btree.PageChange{{
			Page:        commitNodePage(t, 11, 1, 2, btree.KindLeaf),
			ExpectedLSN: current.Header.LSN,
		}},
	}
	if _, err := runtime.Commit(1, currentPlan); !errors.Is(err, wal.ErrDuplicateTransaction) {
		t.Fatalf("duplicate Commit() error = %v", err)
	}
	if _, err := runtime.Commit(2, currentPlan); err != nil {
		t.Fatalf("Commit(after duplicate) error = %v", err)
	}
}

func TestRuntimeWALUncertaintyPoisonsUntilReopen(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"outcome unknown", fmt.Errorf("%w: injected", wal.ErrOutcomeUnknown)},
		{"WAL poisoned", fmt.Errorf("%w: injected", wal.ErrPoisoned)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			set, err := wal.CreateSegmentSet(filepath.Join(directory, "wal"), 0)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = set.Close() }()
			manager, err := page.Create(filepath.Join(directory, "tree.pages"), 13)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = manager.Close() }()
			runtime, _, err := OpenRuntime(set, manager, RuntimeConfig{
				SpaceID: 13, Capacity: 3, OldFrames: 1,
			})
			if err != nil {
				t.Fatal(err)
			}
			log := &errorCommitLog{err: test.err}
			runtime.log = log
			plan := btree.MutationPlan{
				RootPageID: 2,
				NextPageID: 3,
				Changes: []btree.PageChange{{
					Page: commitNodePage(t, 13, 1, 2, btree.KindLeaf),
				}},
				Allocated: []uint64{2},
			}
			if _, err := runtime.Commit(1, plan); !errors.Is(err, test.err) {
				t.Fatalf("Commit() error = %v, want %v", err, test.err)
			}
			if log.calls != 1 {
				t.Fatalf("WAL calls = %d", log.calls)
			}
			if _, err := runtime.Commit(2, plan); !errors.Is(err, ErrRuntimePoisoned) {
				t.Fatalf("Commit(after uncertainty) error = %v", err)
			}
			if log.calls != 1 {
				t.Fatalf("poisoned Runtime called WAL again %d times", log.calls)
			}
		})
	}
}

func TestRuntimeConcurrentStalePlansDoNotInterleaveWAL(t *testing.T) {
	directory := t.TempDir()
	set, err := wal.CreateSegmentSet(filepath.Join(directory, "wal"), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = set.Close() }()
	manager, err := page.Create(filepath.Join(directory, "tree.pages"), 15)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = manager.Close() }()
	runtime, _, err := OpenRuntime(set, manager, RuntimeConfig{
		SpaceID: 15, Capacity: 4, OldFrames: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	plan := btree.MutationPlan{
		RootPageID: 2,
		NextPageID: 3,
		Changes: []btree.PageChange{{
			Page: commitNodePage(t, 15, 1, 2, btree.KindLeaf),
		}},
		Allocated: []uint64{2},
	}
	start := make(chan struct{})
	results := make(chan error, 64)
	var ready sync.WaitGroup
	ready.Add(64)
	for index := range 64 {
		go func(transactionID uint64) {
			ready.Done()
			<-start
			_, err := runtime.Commit(transactionID, plan)
			results <- err
		}(uint64(index + 1))
	}
	ready.Wait()
	close(start)
	succeeded, stale := 0, 0
	for range 64 {
		err := <-results
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrInvalidPlan):
			stale++
		default:
			t.Fatalf("concurrent Commit() error = %v", err)
		}
	}
	if succeeded != 1 || stale != 63 || runtime.State().Revision != 1 {
		t.Fatalf("concurrent results success=%d stale=%d state=%+v", succeeded, stale, runtime.State())
	}
	transactions, err := set.ScanCommitted()
	if err != nil || len(transactions) != 1 {
		t.Fatalf("WAL transactions = %d, %v", len(transactions), err)
	}
}

func TestRuntimeNewPageConflictIsRejectedBeforeWAL(t *testing.T) {
	directory := t.TempDir()
	set, err := wal.CreateSegmentSet(filepath.Join(directory, "wal"), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = set.Close() }()
	manager, err := page.Create(filepath.Join(directory, "tree.pages"), 17)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = manager.Close() }()
	if err := manager.Write(treecontrol.EncodeBootstrap(17)); err != nil {
		t.Fatal(err)
	}
	collision := commitNodePage(t, 17, 1, 2, btree.KindLeaf)
	collision.Header.LSN = 1
	if err := manager.Write(collision); err != nil {
		t.Fatal(err)
	}
	if err := manager.Sync(); err != nil {
		t.Fatal(err)
	}
	runtime, _, err := OpenRuntime(set, manager, RuntimeConfig{
		SpaceID: 17, Capacity: 4, OldFrames: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Commit(1, btree.MutationPlan{
		RootPageID: 2,
		NextPageID: 3,
		Changes: []btree.PageChange{{
			Page: commitNodePage(t, 17, 1, 2, btree.KindLeaf),
		}},
		Allocated: []uint64{2},
	}); !errors.Is(err, buffer.ErrPublishConflict) {
		t.Fatalf("Commit(collision) error = %v", err)
	}
	transactions, err := set.ScanCommitted()
	if err != nil || len(transactions) != 0 {
		t.Fatalf("collision wrote WAL: transactions=%d err=%v", len(transactions), err)
	}
}

func TestRuntimeFlushFaultRetriesWithoutPoisoning(t *testing.T) {
	directory := t.TempDir()
	set, err := wal.CreateSegmentSet(filepath.Join(directory, "wal"), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = set.Close() }()
	store := newRuntimePageStore()
	runtime, _, err := OpenRuntime(set, store, RuntimeConfig{
		SpaceID: 19, Capacity: 4, OldFrames: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Commit(1, btree.MutationPlan{
		RootPageID: 2,
		NextPageID: 3,
		Changes: []btree.PageChange{{
			Page: commitNodePage(t, 19, 1, 2, btree.KindLeaf),
		}},
		Allocated: []uint64{2},
	}); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("page write fault")
	store.setWriteError(injected)
	report, err := runtime.FlushDirty(8)
	if !errors.Is(err, injected) || report.Attempted != 2 ||
		report.Flushed != 0 || report.Remaining != 2 {
		t.Fatalf("faulted flush report=%+v err=%v", report, err)
	}
	store.setWriteError(nil)
	report, err = runtime.FlushDirty(8)
	if err != nil || report.Flushed != 2 || report.Remaining != 0 {
		t.Fatalf("retry flush report=%+v err=%v", report, err)
	}
	root, err := runtime.Read(2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Commit(2, btree.MutationPlan{
		RootPageID: 2,
		NextPageID: 3,
		Changes: []btree.PageChange{{
			Page:        commitNodePage(t, 19, 1, 2, btree.KindLeaf),
			ExpectedLSN: root.Header.LSN,
		}},
	}); err != nil {
		t.Fatalf("Commit(after flush retry) error = %v", err)
	}
}

func TestRuntimeOpenBootstrapSyncFailureReturnsNoRuntimeAndRetries(t *testing.T) {
	directory := t.TempDir()
	set, err := wal.CreateSegmentSet(filepath.Join(directory, "wal"), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = set.Close() }()
	store := newRuntimePageStore()
	injected := errors.New("bootstrap sync fault")
	store.setSyncError(injected)
	runtime, _, err := OpenRuntime(set, store, RuntimeConfig{
		SpaceID: 21, Capacity: 2, OldFrames: 1,
	})
	if runtime != nil || !errors.Is(err, injected) {
		t.Fatalf("OpenRuntime(sync fault) = %v, %v", runtime, err)
	}
	store.setSyncError(nil)
	runtime, _, err = OpenRuntime(set, store, RuntimeConfig{
		SpaceID: 21, Capacity: 2, OldFrames: 1,
	})
	if err != nil || runtime.State() != treecontrol.Bootstrap(21) {
		t.Fatalf("OpenRuntime(retry) state=%+v err=%v", runtime.State(), err)
	}
}

func TestRuntimeRetiresPageWithAssignedWALLSN(t *testing.T) {
	directory := t.TempDir()
	set, err := wal.CreateSegmentSet(filepath.Join(directory, "wal"), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = set.Close() }()
	manager, err := page.Create(filepath.Join(directory, "tree.pages"), 23)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = manager.Close() }()
	control, err := treecontrol.Encode(treecontrol.State{
		SpaceID: 23, Generation: 1, Revision: 1,
		RootPageID: 2, NextPageID: 4, LSN: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	root := commitNodePage(t, 23, 1, 2, btree.KindLeaf)
	root.Header.LSN = 1
	retired := commitNodePage(t, 23, 1, 3, btree.KindLeaf)
	retired.Header.LSN = 1
	for _, value := range []page.Page{control, root, retired} {
		if err := manager.Write(value); err != nil {
			t.Fatal(err)
		}
	}
	if err := manager.Sync(); err != nil {
		t.Fatal(err)
	}
	runtime, _, err := OpenRuntime(set, manager, RuntimeConfig{
		SpaceID: 23, Capacity: 4, OldFrames: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := runtime.Commit(1, btree.MutationPlan{
		RootPageID: 2,
		NextPageID: 4,
		Retired:    []uint64{3},
	})
	if err != nil {
		t.Fatal(err)
	}
	freed, err := runtime.Read(3)
	if err != nil {
		t.Fatal(err)
	}
	if freed.Header.Type != page.TypeFree ||
		freed.Header.LSN != receipt.WAL.FirstLSN || receipt.State.Revision != 2 {
		t.Fatalf("retired Page=%+v receipt=%+v", freed.Header, receipt)
	}
}

func TestRuntimeReopensAndReusesDurableFreePage(t *testing.T) {
	directory := t.TempDir()
	walPath := filepath.Join(directory, "wal")
	pagePath := filepath.Join(directory, "tree.pages")
	set, err := wal.CreateSegmentSet(walPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := page.Create(pagePath, 29)
	if err != nil {
		t.Fatal(err)
	}
	control, err := treecontrol.Encode(treecontrol.State{
		SpaceID: 29, Generation: 1, Revision: 1, RootPageID: 2, NextPageID: 4, LSN: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	large := bytes.Repeat([]byte("v"), 6_000)
	root, err := btree.Encode(page.Header{
		Type: page.TypeBTreeLeaf, SpaceID: 29, PageID: 2, Generation: 1, LSN: 1,
	}, btree.Node{Kind: btree.KindLeaf, LeafEntries: []btree.LeafEntry{
		{Key: []byte("a"), Value: large}, {Key: []byte("c"), Value: large},
	}})
	if err != nil {
		t.Fatal(err)
	}
	spare := commitNodePage(t, 29, 1, 3, btree.KindLeaf)
	spare.Header.LSN = 1
	for _, value := range []page.Page{control, root, spare} {
		if err := manager.Write(value); err != nil {
			t.Fatal(err)
		}
	}
	if err := manager.Sync(); err != nil {
		t.Fatal(err)
	}
	runtime, _, err := OpenRuntime(set, manager, RuntimeConfig{SpaceID: 29, Capacity: 8, OldFrames: 4})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Commit(1, btree.MutationPlan{RootPageID: 2, NextPageID: 4, Retired: []uint64{3}}); err != nil {
		t.Fatal(err)
	}
	if report, err := runtime.FlushDirty(math.MaxUint64); err != nil || report.Remaining != 0 {
		t.Fatalf("FlushDirty() = %+v, %v", report, err)
	}
	if err := set.Close(); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}

	set, err = wal.OpenSegmentSet(walPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = set.Close() }()
	manager, err = page.Open(pagePath, 29)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = manager.Close() }()
	runtime, _, err = OpenRuntime(set, manager, RuntimeConfig{SpaceID: 29, Capacity: 8, OldFrames: 4})
	if err != nil {
		t.Fatal(err)
	}
	if free := runtime.FreePageIDs(); len(free) != 1 || free[0] != 3 {
		t.Fatalf("reopened free Pages = %v", free)
	}
	planner, err := btree.NewMutationPlanner(29, 1, 2, 4, runtime, runtime.FreePageIDs()...)
	if err != nil {
		t.Fatal(err)
	}
	if err := planner.Upsert([]byte("b"), large); err != nil {
		t.Fatal(err)
	}
	plan := planner.Plan()
	if len(plan.Reused) != 1 || plan.Reused[0] != 3 || len(plan.Allocated) != 1 || plan.Allocated[0] != 4 {
		t.Fatalf("reuse plan = %+v", plan)
	}
	if _, err := runtime.Commit(2, plan); err != nil {
		t.Fatal(err)
	}
	if free := runtime.FreePageIDs(); len(free) != 0 {
		t.Fatalf("free Pages after reuse = %v", free)
	}
	searcher, err := btree.NewSearcher(29, runtime.State().RootPageID, runtime)
	if err != nil {
		t.Fatal(err)
	}
	if value, err := searcher.Get([]byte("b")); err != nil || !bytes.Equal(value, large) {
		t.Fatalf("reused tree lookup bytes=%d err=%v", len(value), err)
	}
}

type errorCommitLog struct {
	err   error
	calls int
}

func (log *errorCommitLog) CommitTransaction(
	uint64,
	[]wal.Record,
) (wal.CommittedTransaction, error) {
	log.calls++
	return wal.CommittedTransaction{}, log.err
}

type runtimePageStore struct {
	mu       sync.Mutex
	pages    map[uint64]page.Page
	writeErr error
	syncErr  error
}

func newRuntimePageStore() *runtimePageStore {
	return &runtimePageStore{pages: make(map[uint64]page.Page)}
}

func (store *runtimePageStore) Read(pageID uint64) (page.Page, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	value, exists := store.pages[pageID]
	if !exists {
		return page.Page{}, fmt.Errorf("%w: Page %d", page.ErrNotFound, pageID)
	}
	return cloneRuntimePage(value), nil
}

func (store *runtimePageStore) Write(value page.Page) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.writeErr != nil {
		return store.writeErr
	}
	store.pages[value.Header.PageID] = cloneRuntimePage(value)
	return nil
}

func (store *runtimePageStore) Sync() error {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.syncErr
}

func (store *runtimePageStore) setWriteError(err error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.writeErr = err
}

func (store *runtimePageStore) setSyncError(err error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.syncErr = err
}

func cloneRuntimePage(value page.Page) page.Page {
	value.Payload = append([]byte(nil), value.Payload...)
	return value
}
