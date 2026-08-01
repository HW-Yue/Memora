package treecommit

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/HW-Yue/Memora/internal/store/btree"
	"github.com/HW-Yue/Memora/internal/store/buffer"
	"github.com/HW-Yue/Memora/internal/store/page"
	"github.com/HW-Yue/Memora/internal/store/treecontrol"
	"github.com/HW-Yue/Memora/internal/store/wal"
)

var ErrRuntimePoisoned = errors.New("Tree commit Runtime requires reopen recovery")

type RuntimeConfig struct {
	SpaceID   uint64
	Capacity  uint64
	OldFrames uint64
}

type CommitReceipt struct {
	WAL   wal.Receipt
	State treecontrol.State
}

type commitLog interface {
	CommitTransaction(uint64, []wal.Record) (wal.CommittedTransaction, error)
}

type Runtime struct {
	mu       sync.Mutex
	log      commitLog
	pool     *buffer.Pool
	spaceID  uint64
	state    treecontrol.State
	free     map[uint64]struct{}
	poisoned bool
}

type batchRecipe struct {
	recordIndex uint64
	expectedLSN uint64
	newPage     bool
	reusedPage  bool
	inferLSN    bool
}

func OpenRuntime(
	set *wal.SegmentSet,
	store wal.PageStore,
	config RuntimeConfig,
) (*Runtime, wal.RecoveryReport, error) {
	if set == nil || store == nil || config.SpaceID == 0 || config.Capacity == 0 ||
		config.OldFrames == 0 || config.OldFrames > config.Capacity {
		return nil, wal.RecoveryReport{}, fmt.Errorf("%w: Runtime configuration", buffer.ErrInvalid)
	}
	report, err := wal.RecoverSegmentSet(
		set,
		map[uint64]wal.PageStore{config.SpaceID: store},
	)
	if err != nil {
		return nil, report, fmt.Errorf("recover Tree Runtime: %w", err)
	}
	controlPage, err := store.Read(treecontrol.PageID)
	if errors.Is(err, page.ErrNotFound) {
		controlPage = treecontrol.EncodeBootstrap(config.SpaceID)
		if err := store.Write(controlPage); err != nil {
			return nil, report, fmt.Errorf("write bootstrap Tree control: %w", err)
		}
	} else if err != nil {
		return nil, report, fmt.Errorf("read Tree control: %w", err)
	}
	// Sync even when bootstrap was left visible by an earlier failed Sync. This
	// makes a retry converge instead of trusting an outcome-unknown first write.
	if err := store.Sync(); err != nil {
		return nil, report, fmt.Errorf("sync Tree control on open: %w", err)
	}
	state, err := treecontrol.Decode(controlPage, config.SpaceID)
	if err != nil {
		return nil, report, fmt.Errorf("decode Tree control: %w", err)
	}
	free, err := scanFreePages(store, state)
	if err != nil {
		return nil, report, err
	}
	pool, err := buffer.New(
		buffer.LoaderFunc(func(key buffer.Key) (page.Page, error) {
			if key.SpaceID != config.SpaceID {
				return page.Page{}, fmt.Errorf("%w: Runtime space", buffer.ErrInvalid)
			}
			return store.Read(key.PageID)
		}),
		buffer.Config{
			Capacity:   config.Capacity,
			OldFrames:  config.OldFrames,
			Writer:     buffer.PageWriterFunc(store.Write),
			Durability: set,
		},
	)
	if err != nil {
		return nil, report, err
	}
	runtime := &Runtime{log: set, pool: pool, spaceID: config.SpaceID, state: state, free: free}
	loaded, err := runtime.Read(treecontrol.PageID)
	if err != nil {
		return nil, report, fmt.Errorf("load Tree control: %w", err)
	}
	loadedState, err := treecontrol.Decode(loaded, config.SpaceID)
	if err != nil || loadedState != state {
		return nil, report, fmt.Errorf("%w: loaded Tree control mismatch", treecontrol.ErrCorrupt)
	}
	return runtime, report, nil
}

func scanFreePages(store wal.PageStore, state treecontrol.State) (map[uint64]struct{}, error) {
	result := make(map[uint64]struct{})
	for pageID := treecontrol.FirstDataPageID; pageID < state.NextPageID; pageID++ {
		value, err := store.Read(pageID)
		if err != nil {
			return nil, fmt.Errorf("scan reusable Page %d: %w", pageID, err)
		}
		if value.Header.Type == page.TypeFree {
			result[pageID] = struct{}{}
			continue
		}
		if value.Header.Type != page.TypeBTreeInternal && value.Header.Type != page.TypeBTreeLeaf {
			return nil, fmt.Errorf("%w: reusable Page scan type", treecontrol.ErrCorrupt)
		}
	}
	return result, nil
}

func (runtime *Runtime) State() treecontrol.State {
	if runtime == nil {
		return treecontrol.State{}
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.state
}

func (runtime *Runtime) FreePageIDs() []uint64 {
	if runtime == nil {
		return nil
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	result := make([]uint64, 0, len(runtime.free))
	for pageID := range runtime.free {
		result = append(result, pageID)
	}
	sort.Slice(result, func(left, right int) bool { return result[left] < result[right] })
	return result
}

func (runtime *Runtime) Read(pageID uint64) (page.Page, error) {
	if runtime == nil || runtime.pool == nil || pageID == 0 {
		return page.Page{}, fmt.Errorf("%w: Runtime read", buffer.ErrInvalid)
	}
	handle, err := runtime.pool.Fetch(buffer.Key{
		SpaceID: runtime.spaceID,
		PageID:  pageID,
	})
	if err != nil {
		return page.Page{}, err
	}
	defer func() { _ = handle.Release() }()
	var result page.Page
	if err := handle.Inspect(func(value page.Page) error {
		result = value
		return nil
	}); err != nil {
		return page.Page{}, err
	}
	return result, nil
}

func (runtime *Runtime) Commit(
	transactionID uint64,
	plan btree.MutationPlan,
) (CommitReceipt, error) {
	if runtime == nil || runtime.log == nil || runtime.pool == nil {
		return CommitReceipt{}, fmt.Errorf("%w: Runtime", buffer.ErrInvalid)
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.poisoned {
		return CommitReceipt{}, ErrRuntimePoisoned
	}
	for _, pageID := range plan.Reused {
		if _, reusable := runtime.free[pageID]; !reusable {
			return CommitReceipt{}, fmt.Errorf("%w: Page %d is not reusable", ErrInvalidPlan, pageID)
		}
	}
	prepared, err := Prepare(runtime.state, plan)
	if err != nil {
		return CommitReceipt{}, err
	}
	recipes, err := runtime.preflight(plan, prepared.Records)
	if err != nil {
		return CommitReceipt{}, err
	}
	transaction, err := runtime.log.CommitTransaction(transactionID, prepared.Records)
	if err != nil {
		if errors.Is(err, wal.ErrOutcomeUnknown) || errors.Is(err, wal.ErrPoisoned) {
			runtime.poisoned = true
		}
		return CommitReceipt{}, err
	}
	batch, next, err := materializeBatch(
		runtime.state,
		plan,
		prepared.Records,
		transaction,
		recipes,
	)
	if err != nil {
		runtime.poisoned = true
		return CommitReceipt{}, fmt.Errorf("%w: committed WAL batch: %v", ErrRuntimePoisoned, err)
	}
	if err := runtime.pool.PublishBatch(batch); err != nil {
		runtime.poisoned = true
		return CommitReceipt{}, err
	}
	runtime.state = next
	for _, pageID := range plan.Reused {
		delete(runtime.free, pageID)
	}
	for _, pageID := range plan.Retired {
		runtime.free[pageID] = struct{}{}
	}
	return CommitReceipt{WAL: transaction.Receipt, State: next}, nil
}

func (runtime *Runtime) FlushDirty(limit uint64) (buffer.FlushReport, error) {
	if runtime == nil || runtime.pool == nil {
		return buffer.FlushReport{}, fmt.Errorf("%w: Runtime", buffer.ErrInvalid)
	}
	return runtime.pool.FlushDirty(limit)
}

func (runtime *Runtime) preflight(
	plan btree.MutationPlan,
	records []wal.Record,
) ([]batchRecipe, error) {
	allocated := make(map[uint64]struct{}, len(plan.Allocated))
	for _, pageID := range plan.Allocated {
		allocated[pageID] = struct{}{}
	}
	reused := make(map[uint64]struct{}, len(plan.Reused))
	for _, pageID := range plan.Reused {
		reused[pageID] = struct{}{}
	}
	recipes := make([]batchRecipe, 0, len(plan.Changes)+len(plan.Retired))
	for index, change := range plan.Changes {
		_, newPage := allocated[change.Page.Header.PageID]
		_, reusedPage := reused[change.Page.Header.PageID]
		recipes = append(recipes, batchRecipe{
			recordIndex: uint64(index),
			expectedLSN: change.ExpectedLSN,
			newPage:     newPage,
			reusedPage:  reusedPage,
		})
	}
	for index := range plan.Retired {
		recipes = append(recipes, batchRecipe{
			recordIndex: uint64(len(plan.Changes) + index),
			inferLSN:    true,
		})
	}
	// Probe new identities first. A failed Fetch may evict a clean Frame, so all
	// existing batch Frames and the control are deliberately reloaded afterwards.
	for pass := 0; pass < 2; pass++ {
		for index := range recipes {
			recipe := &recipes[index]
			if recipe.newPage != (pass == 0) {
				continue
			}
			if recipe.recordIndex >= uint64(len(records)) {
				return nil, fmt.Errorf("%w: preflight Record index", ErrInvalidPlan)
			}
			image, err := page.Decode(records[recipe.recordIndex].Payload)
			if err != nil {
				return nil, fmt.Errorf("%w: preflight Page image", ErrInvalidPlan)
			}
			handle, fetchErr := runtime.pool.Fetch(buffer.Key{
				SpaceID: image.Header.SpaceID,
				PageID:  image.Header.PageID,
			})
			if recipe.newPage {
				if fetchErr == nil {
					_ = handle.Release()
					return nil, fmt.Errorf("%w: new Page %d is resident", buffer.ErrPublishConflict, image.Header.PageID)
				}
				if errors.Is(fetchErr, page.ErrNotFound) {
					continue
				}
				return nil, fmt.Errorf("preflight new Page %d: %w", image.Header.PageID, fetchErr)
			}
			if fetchErr != nil {
				return nil, fmt.Errorf("preflight existing Page %d: %w", image.Header.PageID, fetchErr)
			}
			var current page.Page
			inspectErr := handle.Inspect(func(value page.Page) error {
				current = value
				return nil
			})
			_ = handle.Release()
			if inspectErr != nil {
				return nil, inspectErr
			}
			if current.Header.Generation != runtime.state.Generation ||
				current.Header.SpaceID != image.Header.SpaceID ||
				current.Header.PageID != image.Header.PageID ||
				(!recipe.inferLSN && current.Header.LSN != recipe.expectedLSN) {
				return nil, fmt.Errorf("%w: existing Page %d state", buffer.ErrPublishConflict, image.Header.PageID)
			}
			if recipe.reusedPage && current.Header.Type != page.TypeFree {
				return nil, fmt.Errorf("%w: reusable Page %d type", buffer.ErrPublishConflict, image.Header.PageID)
			}
			if recipe.inferLSN {
				recipe.expectedLSN = current.Header.LSN
			}
		}
	}
	control, err := runtime.Read(treecontrol.PageID)
	if err != nil {
		return nil, fmt.Errorf("preflight Tree control: %w", err)
	}
	controlState, err := treecontrol.Decode(control, runtime.state.SpaceID)
	if err != nil || controlState != runtime.state {
		return nil, fmt.Errorf("%w: preflight Tree control", buffer.ErrPublishConflict)
	}
	return recipes, nil
}

func materializeBatch(
	base treecontrol.State,
	plan btree.MutationPlan,
	prepared []wal.Record,
	committed wal.CommittedTransaction,
	recipes []batchRecipe,
) ([]buffer.BatchChange, treecontrol.State, error) {
	if len(prepared) == 0 || len(committed.Records) != len(prepared) ||
		uint32(len(prepared)) != committed.Receipt.RecordCount {
		return nil, treecontrol.State{}, fmt.Errorf("WAL Record count")
	}
	for index := range prepared {
		left, right := prepared[index], committed.Records[index]
		if left.Type != right.Type || left.SpaceID != right.SpaceID ||
			left.PageID != right.PageID || !bytes.Equal(left.Payload, right.Payload) ||
			right.LSN == 0 || right.TransactionID != committed.Receipt.TransactionID {
			return nil, treecontrol.State{}, fmt.Errorf("WAL Record %d mismatch", index)
		}
	}
	batch := make([]buffer.BatchChange, 0, len(recipes)+1)
	for _, recipe := range recipes {
		record := committed.Records[recipe.recordIndex]
		image, err := page.Decode(record.Payload)
		if err != nil || image.Header.LSN != 0 {
			return nil, treecontrol.State{}, fmt.Errorf("decode committed Page image")
		}
		image.Header.LSN = record.LSN
		batch = append(batch, buffer.BatchChange{
			Page: image, ExpectedLSN: recipe.expectedLSN, New: recipe.newPage,
		})
	}
	root := committed.Records[len(committed.Records)-1]
	if root.Type != wal.TypeRoot || root.PageID != treecontrol.PageID {
		return nil, treecontrol.State{}, fmt.Errorf("root redo is not last")
	}
	next := treecontrol.State{
		SpaceID: base.SpaceID, Generation: base.Generation,
		Revision: base.Revision + 1, RootPageID: plan.RootPageID,
		NextPageID: plan.NextPageID, LSN: root.LSN,
	}
	control, err := treecontrol.Encode(next)
	if err != nil {
		return nil, treecontrol.State{}, err
	}
	batch = append(batch, buffer.BatchChange{
		Page: control, ExpectedLSN: base.LSN,
	})
	return batch, next, nil
}
