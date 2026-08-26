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

	// Pool, when set, is a buffer pool shared with the other Trees of the same
	// generation, and Capacity/OldFrames are then the shared pool's business
	// rather than this Runtime's. Leaving it nil gives this Runtime a private
	// pool of its own, which is what a lone Tree wants.
	//
	// A shared pool must be built over a buffer.Router that already has this
	// SpaceID registered; otherwise every read of this Tree fails to route.
	Pool *buffer.Pool
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

// OpenRuntime recovers the log into the single space it is given, then attaches
// a Runtime to it.
//
// Use it only when the log belongs to that one space. A log shared by several
// spaces must be recovered once, by the caller, with wal.RecoverSegmentSet over
// every space: recovery routes each Record by SpaceID and fails with
// ErrMissingSpace on a Record whose space is absent from the map, so recovering
// per space would make each pass reject the other spaces' Records. Those
// callers use AttachRuntime instead.
func OpenRuntime(
	set *wal.SegmentSet,
	store wal.PageStore,
	config RuntimeConfig,
) (*Runtime, wal.RecoveryReport, error) {
	if err := validateRuntimeInputs(set, store, config); err != nil {
		return nil, wal.RecoveryReport{}, err
	}
	report, err := wal.RecoverSegmentSet(
		set,
		map[uint64]wal.PageStore{config.SpaceID: store},
	)
	if err != nil {
		return nil, report, fmt.Errorf("recover Tree Runtime: %w", err)
	}
	runtime, err := AttachRuntime(set, store, config)
	if err != nil {
		return nil, report, err
	}
	return runtime, report, nil
}

// AttachRuntime builds a Runtime over a log the caller has already recovered.
// It is the second half of OpenRuntime, split out so that several spaces can
// share one log behind a single recovery pass.
func AttachRuntime(
	set *wal.SegmentSet,
	store wal.PageStore,
	config RuntimeConfig,
) (*Runtime, error) {
	if err := validateRuntimeInputs(set, store, config); err != nil {
		return nil, err
	}
	controlPage, err := store.Read(treecontrol.PageID)
	if errors.Is(err, page.ErrNotFound) {
		controlPage, err = treecontrol.EncodeBootstrap(config.SpaceID)
		if err != nil {
			return nil, fmt.Errorf("encode bootstrap Tree control: %w", err)
		}
		if err := store.Write(controlPage); err != nil {
			return nil, fmt.Errorf("write bootstrap Tree control: %w", err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("read Tree control: %w", err)
	}
	// Sync even when bootstrap was left visible by an earlier failed Sync. This
	// makes a retry converge instead of trusting an outcome-unknown first write.
	if err := store.Sync(); err != nil {
		return nil, fmt.Errorf("sync Tree control on open: %w", err)
	}
	state, err := treecontrol.Decode(controlPage, config.SpaceID)
	if err != nil {
		return nil, fmt.Errorf("decode Tree control: %w", err)
	}
	free, err := scanFreePages(store, state)
	if err != nil {
		return nil, err
	}
	pool := config.Pool
	if pool == nil {
		pool, err = buffer.New(
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
			return nil, err
		}
	}
	runtime := &Runtime{log: set, pool: pool, spaceID: config.SpaceID, state: state, free: free}
	loaded, err := runtime.Read(treecontrol.PageID)
	if err != nil {
		return nil, fmt.Errorf("load Tree control: %w", err)
	}
	loadedState, err := treecontrol.Decode(loaded, config.SpaceID)
	if err != nil || loadedState != state {
		return nil, fmt.Errorf("%w: loaded Tree control mismatch", treecontrol.ErrCorrupt)
	}
	return runtime, nil
}

func validateRuntimeInputs(
	set *wal.SegmentSet,
	store wal.PageStore,
	config RuntimeConfig,
) error {
	if set == nil || store == nil || config.SpaceID == 0 {
		return fmt.Errorf("%w: Runtime configuration", buffer.ErrInvalid)
	}
	// Capacity is this Runtime's own only when it builds its own pool. With a
	// shared pool the sizing was decided once, by whoever built it.
	if config.Pool == nil &&
		(config.Capacity == 0 || config.OldFrames == 0 || config.OldFrames > config.Capacity) {
		return fmt.Errorf("%w: Runtime configuration", buffer.ErrInvalid)
	}
	return nil
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

// Pool reports the buffer pool this Runtime reads and writes through.
//
// It is exposed so a caller that opens several Trees can verify they share one
// pool — the property that keeps resident memory bounded by a single budget
// instead of growing with the number of Trees.
func (runtime *Runtime) Pool() *buffer.Pool {
	if runtime == nil {
		return nil
	}
	return runtime.pool
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

// GroupMember names one Tree's contribution to a group commit.
type GroupMember struct {
	Runtime *Runtime
	Plan    btree.MutationPlan
}

// stagedMember is one member's commit prepared up to, but not including, the
// WAL append.
type stagedMember struct {
	offset   int
	prepared []wal.Record
	recipes  []batchRecipe
}

// Commit writes one Tree in its own WAL transaction.
func (runtime *Runtime) Commit(
	transactionID uint64,
	plan btree.MutationPlan,
) (CommitReceipt, error) {
	receipts, err := CommitGroup(transactionID, []GroupMember{{Runtime: runtime, Plan: plan}})
	if err != nil {
		return CommitReceipt{}, err
	}
	return receipts[0], nil
}

// Group collects the Trees taking part in one WAL transaction.
//
// Each Tree is added while its Index holds its own write lock, and that lock is
// released only after the commit. Without that a reader could observe one Tree
// of the group updated and another not — which is the very thing the single
// transaction exists to prevent.
type Group struct {
	members  []GroupMember
	releases []func()
}

// Add enrolls one Tree's planned mutation. release, if not nil, runs after the
// group commit finishes, in reverse order of Add.
func (group *Group) Add(runtime *Runtime, plan btree.MutationPlan, release func()) {
	if group == nil {
		return
	}
	group.members = append(group.members, GroupMember{Runtime: runtime, Plan: plan})
	group.releases = append(group.releases, release)
}

// Len reports how many Trees the group holds.
func (group *Group) Len() int {
	if group == nil {
		return 0
	}
	return len(group.members)
}

func (group *Group) release() {
	for index := len(group.releases) - 1; index >= 0; index-- {
		if group.releases[index] != nil {
			group.releases[index]()
		}
	}
	group.releases = nil
}

// CommitGroupFunc builds a group with collect, commits every Tree it enrolled
// in one WAL transaction, then releases the collected locks in reverse order.
//
// collect is where each Index validates and plans. An Index that finds nothing
// to do simply does not add itself; a group that ends up empty commits nothing
// and is not an error. The releases run even when collect fails, so a partly
// built group never strands a lock.
func CommitGroupFunc(transactionID uint64, collect func(*Group) error) error {
	if collect == nil {
		return fmt.Errorf("%w: commit group collector", buffer.ErrInvalid)
	}
	group := &Group{}
	defer group.release()
	if err := collect(group); err != nil {
		return err
	}
	if len(group.members) == 0 {
		return nil
	}
	_, err := CommitGroup(transactionID, group.members)
	return err
}

// CommitGroup writes several Trees in ONE WAL transaction.
//
// That single commit Record is the whole point: recovery replays a transaction
// or it does not, so a write spanning several Trees can no longer land in some
// of them and not the others. Every Runtime therefore has to share one log —
// three commits into three logs is exactly the gap this closes.
//
// Receipts come back in the caller's order, not the internal commit order.
func CommitGroup(transactionID uint64, members []GroupMember) ([]CommitReceipt, error) {
	if len(members) == 0 {
		return nil, fmt.Errorf("%w: empty commit group", buffer.ErrInvalid)
	}
	// Commit in Space ID order. Two things need it: locking in a fixed order
	// means two overlapping groups cannot deadlock, and it keeps one Tree's
	// Records contiguous in the transaction, which is the grouping recovery
	// parses each Tree's metadata by.
	order := make([]int, len(members))
	for index := range order {
		order[index] = index
	}
	sort.SliceStable(order, func(left, right int) bool {
		return spaceOf(members[order[left]].Runtime) < spaceOf(members[order[right]].Runtime)
	})

	var log commitLog
	previousSpace := uint64(0)
	for position, index := range order {
		runtime := members[index].Runtime
		if runtime == nil || runtime.log == nil || runtime.pool == nil {
			return nil, fmt.Errorf("%w: Runtime", buffer.ErrInvalid)
		}
		if runtime.spaceID == previousSpace {
			return nil, fmt.Errorf("%w: space %d twice in one group", buffer.ErrInvalid, runtime.spaceID)
		}
		previousSpace = runtime.spaceID
		if position == 0 {
			log = runtime.log
		} else if runtime.log != log {
			return nil, fmt.Errorf("%w: commit group spans more than one log", buffer.ErrInvalid)
		}
		runtime.mu.Lock()
		defer runtime.mu.Unlock()
	}

	staged := make([]stagedMember, len(members))
	var records []wal.Record
	for _, index := range order {
		runtime, plan := members[index].Runtime, members[index].Plan
		if runtime.poisoned {
			return nil, ErrRuntimePoisoned
		}
		for _, pageID := range plan.Reused {
			if _, reusable := runtime.free[pageID]; !reusable {
				return nil, fmt.Errorf("%w: Page %d is not reusable", ErrInvalidPlan, pageID)
			}
		}
		prepared, err := Prepare(runtime.state, plan)
		if err != nil {
			return nil, err
		}
		recipes, err := runtime.preflight(plan, prepared.Records)
		if err != nil {
			return nil, err
		}
		staged[index] = stagedMember{
			offset: len(records), prepared: prepared.Records, recipes: recipes,
		}
		records = append(records, prepared.Records...)
	}

	transaction, err := log.CommitTransaction(transactionID, records)
	if err != nil {
		if errors.Is(err, wal.ErrOutcomeUnknown) || errors.Is(err, wal.ErrPoisoned) {
			poisonGroup(members)
		}
		return nil, err
	}

	receipts := make([]CommitReceipt, len(members))
	for _, index := range order {
		runtime, plan := members[index].Runtime, members[index].Plan
		entry := staged[index]
		// Each member sees only its own slice of the transaction. The Records
		// are already grouped by space, so the slice is contiguous and the
		// recipes' Record indexes stay relative to it.
		member := wal.CommittedTransaction{
			Receipt: transaction.Receipt,
			Records: transaction.Records[entry.offset : entry.offset+len(entry.prepared)],
		}
		member.Receipt.RecordCount = uint32(len(entry.prepared))
		batch, next, err := materializeBatch(runtime.state, plan, entry.prepared, member, entry.recipes)
		if err != nil {
			// The transaction is already durable, so a failure here leaves the
			// log ahead of memory for EVERY member, not just this one — the
			// others may have published already. All of them need a reopen.
			poisonGroup(members)
			return nil, fmt.Errorf("%w: committed WAL batch: %v", ErrRuntimePoisoned, err)
		}
		if err := runtime.pool.PublishBatch(batch); err != nil {
			poisonGroup(members)
			return nil, err
		}
		runtime.state = next
		for _, pageID := range plan.Reused {
			delete(runtime.free, pageID)
		}
		for _, pageID := range plan.Retired {
			runtime.free[pageID] = struct{}{}
		}
		receipts[index] = CommitReceipt{WAL: transaction.Receipt, State: next}
	}
	return receipts, nil
}

func spaceOf(runtime *Runtime) uint64 {
	if runtime == nil {
		return 0
	}
	return runtime.spaceID
}

func poisonGroup(members []GroupMember) {
	for _, member := range members {
		if member.Runtime != nil {
			member.Runtime.poisoned = true
		}
	}
}

func (runtime *Runtime) FlushDirty(limit uint64) (buffer.FlushReport, error) {
	if runtime == nil || runtime.pool == nil {
		return buffer.FlushReport{}, fmt.Errorf("%w: Runtime", buffer.ErrInvalid)
	}
	return runtime.pool.FlushDirty(limit)
}

// FlushDirtyThrough flushes against a durable WAL LSN the caller already knows.
// A checkpoint barrier runs inside the log's own lock and must use this rather
// than FlushDirty, which would call back into the log and deadlock.
func (runtime *Runtime) FlushDirtyThrough(limit, durableLSN uint64) (buffer.FlushReport, error) {
	if runtime == nil || runtime.pool == nil {
		return buffer.FlushReport{}, fmt.Errorf("%w: Runtime", buffer.ErrInvalid)
	}
	return runtime.pool.FlushDirtyThrough(limit, durableLSN)
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
