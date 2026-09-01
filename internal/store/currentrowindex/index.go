package currentrowindex

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"

	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/store/btree"
	"github.com/HW-Yue/Memora/internal/store/page"
	"github.com/HW-Yue/Memora/internal/store/treecommit"
	"github.com/HW-Yue/Memora/internal/store/treecontrol"
	"github.com/HW-Yue/Memora/internal/store/wal"
)

type Update struct {
	ExpectedRevision uint64
	Locator          Locator
}

type Receipt struct {
	Changed bool
	State   treecontrol.State
	WAL     wal.Receipt
}

type Index struct {
	mu      sync.RWMutex
	runtime *treecommit.Runtime
}

type preparedUpdate struct {
	expected uint64
	key      []byte
	value    []byte
	locator  Locator
}

func Open(runtime *treecommit.Runtime) (*Index, error) {
	if runtime == nil || runtime.State().SpaceID == 0 {
		return nil, fmt.Errorf("%w: durable Tree Runtime", ErrInvalid)
	}
	return &Index{runtime: runtime}, nil
}

// Bootstrap creates a complete Current Row authority from final locators.
// Unlike Apply, it deliberately does not replay intermediate Row transitions;
// the durable Tree must still be empty.
// Bootstrap creates a Table's Tree from its final locators.
//
// rowIDCounter seeds the Table's Row ID counter. A rebuild has to carry it
// forward: starting again from zero would hand out IDs the rebuilt Rows are
// already using. The caller computes it because the shape of a Row ID is the
// Row layer's business, not this index's.
func (index *Index) Bootstrap(
	transactionID uint64, locators []Locator, rowIDCounter uint64,
) (Receipt, error) {
	if index == nil || index.runtime == nil || transactionID == 0 {
		return Receipt{}, fmt.Errorf("%w: Bootstrap request", ErrInvalid)
	}
	updates := make([]Update, len(locators))
	for position, locator := range locators {
		updates[position] = Update{Locator: locator}
	}
	prepared, err := prepareUpdates(updates)
	if err != nil {
		return Receipt{}, err
	}

	index.mu.Lock()
	defer index.mu.Unlock()
	state := index.runtime.State()
	if state.RootPageID != 0 {
		return Receipt{}, fmt.Errorf("%w: Current Row authority is not empty", ErrConflict)
	}
	plan, err := planBootstrap(state, prepared, rowIDCounter)
	if err != nil {
		return Receipt{}, err
	}
	committed, err := index.runtime.Commit(transactionID, plan)
	if err != nil {
		return Receipt{}, err
	}
	return Receipt{Changed: true, State: committed.State, WAL: committed.WAL}, nil
}

func (index *Index) Apply(transactionID uint64, updates []Update) (Receipt, error) {
	return index.ApplyWithRowIDCounter(transactionID, updates, 0)
}

// ApplyWithRowIDCounter is Apply plus an advance of the Table's Row ID counter.
//
// The two are one commit because they are one fact: an allocated ID is only
// allocated if the Row that took it is durable.
//
// rowIDCounter is a floor. Callers pass the highest number among the Rows they
// are writing; the counter moves up to it and never down, so a write to an
// older Row leaves it alone. Zero means the same thing as any value already
// reached: nothing to do.
func (index *Index) ApplyWithRowIDCounter(
	transactionID uint64, updates []Update, rowIDCounter uint64,
) (Receipt, error) {
	if index == nil || index.runtime == nil || transactionID == 0 || len(updates) == 0 {
		return Receipt{}, fmt.Errorf("%w: Apply request", ErrInvalid)
	}
	prepared, err := prepareUpdates(updates)
	if err != nil {
		return Receipt{}, err
	}

	index.mu.Lock()
	defer index.mu.Unlock()
	plan, changed, err := index.planApplyLocked(prepared, rowIDCounter)
	if err != nil {
		return Receipt{}, err
	}
	if !changed {
		return Receipt{State: index.runtime.State()}, nil
	}
	committed, err := index.runtime.Commit(transactionID, plan)
	if err != nil {
		return Receipt{}, err
	}
	return Receipt{Changed: true, State: committed.State, WAL: committed.WAL}, nil
}

// StageApply validates and plans an Apply and enrols the Tree in group, so it
// can be committed in one WAL transaction together with other Trees.
//
// On success the Index's write lock is held until the group commit finishes —
// the group owns releasing it. An Apply with nothing to do adds no member and
// releases immediately.
func (index *Index) StageApply(group *treecommit.Group, updates []Update) error {
	return index.StageApplyWithRowIDCounter(group, updates, 0)
}

// StageApplyWithRowIDCounter is StageApply plus an advance of the Table's Row
// ID counter, in the same group commit. See ApplyWithRowIDCounter.
func (index *Index) StageApplyWithRowIDCounter(
	group *treecommit.Group, updates []Update, rowIDCounter uint64,
) error {
	if index == nil || index.runtime == nil || group == nil || len(updates) == 0 {
		return fmt.Errorf("%w: Apply request", ErrInvalid)
	}
	prepared, err := prepareUpdates(updates)
	if err != nil {
		return err
	}
	// Through Group.Stage: it claims this Tree before taking the lock, so a
	// second stage of it in this group is refused instead of waiting for a lock
	// the group cannot release until this call returns.
	return group.Stage(index.runtime, &index.mu, func() (btree.MutationPlan, bool, error) {
		return index.planApplyLocked(prepared, rowIDCounter)
	})
}

// planApplyLocked builds the mutation plan for an Apply. The caller holds the
// write lock; changed is false when the updates are already in the Tree.
func (index *Index) planApplyLocked(
	prepared []preparedUpdate, rowIDCounter uint64,
) (btree.MutationPlan, bool, error) {
	state := index.runtime.State()
	active, err := index.validateTransitions(state, prepared)
	if err != nil {
		return btree.MutationPlan{}, false, err
	}
	if len(active) == 0 && rowIDCounter == 0 {
		return btree.MutationPlan{}, false, nil
	}
	if rowIDCounter != 0 {
		stored, err := index.rowIDCounterLocked(state)
		if err != nil {
			return btree.MutationPlan{}, false, err
		}
		// The argument is a floor, not an assignment: callers pass the highest
		// number among the Rows they are writing, and a write to an old Row is
		// a low number that must leave the counter where it is. Anything at or
		// below the stored value is therefore nothing to do — the counter only
		// ever moves up.
		if rowIDCounter <= stored {
			rowIDCounter = 0
		}
	}
	if len(active) == 0 && rowIDCounter == 0 {
		return btree.MutationPlan{}, false, nil
	}
	plan, err := index.plan(state, active, rowIDCounter)
	if err != nil {
		return btree.MutationPlan{}, false, err
	}
	return plan, true, nil
}

// RowIDCounter reports the highest Row ID number this Table has handed out.
// Zero means none: the next insert takes 1.
func (index *Index) RowIDCounter() (uint64, error) {
	if index == nil || index.runtime == nil {
		return 0, fmt.Errorf("%w: lookup Index", ErrInvalid)
	}
	index.mu.RLock()
	defer index.mu.RUnlock()
	return index.rowIDCounterLocked(index.runtime.State())
}

func (index *Index) rowIDCounterLocked(state treecontrol.State) (uint64, error) {
	if state.RootPageID == 0 {
		return 0, nil
	}
	searcher, err := btree.NewSearcher(state.SpaceID, state.RootPageID, index.runtime)
	if err != nil {
		return 0, err
	}
	encoded, err := searcher.Get(rowIDCounterKey())
	if errors.Is(err, btree.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, classifyTreeError(err)
	}
	return decodeRowIDCounter(encoded)
}

func (index *Index) Lookup(rowID string) (Locator, error) {
	if index == nil || index.runtime == nil {
		return Locator{}, fmt.Errorf("%w: lookup Index", ErrInvalid)
	}
	key, err := encodeKey(rowID)
	if err != nil {
		return Locator{}, err
	}
	index.mu.RLock()
	defer index.mu.RUnlock()
	state := index.runtime.State()
	if state.RootPageID == 0 {
		return Locator{}, ErrNotFound
	}
	searcher, err := btree.NewSearcher(state.SpaceID, state.RootPageID, index.runtime)
	if err != nil {
		return Locator{}, err
	}
	encoded, err := searcher.Get(key)
	if errors.Is(err, btree.ErrNotFound) {
		return Locator{}, ErrNotFound
	}
	if err != nil {
		return Locator{}, classifyTreeError(err)
	}
	locator, err := decodeLocator(encoded)
	if err != nil {
		return Locator{}, err
	}
	if locator.RowID != rowID {
		return Locator{}, fmt.Errorf("%w: locator does not match key scope", ErrCorrupt)
	}
	return locator, nil
}

func prepareUpdates(updates []Update) ([]preparedUpdate, error) {
	result := make([]preparedUpdate, 0, len(updates))
	seen := make(map[string]struct{}, len(updates))
	for _, update := range updates {
		key, err := encodeKey(update.Locator.RowID)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[string(key)]; duplicate {
			return nil, fmt.Errorf("%w: duplicate Row key in batch", ErrConflict)
		}
		seen[string(key)] = struct{}{}
		value, err := encodeLocator(update.Locator)
		if err != nil {
			return nil, err
		}
		result = append(result, preparedUpdate{
			expected: update.ExpectedRevision,
			key:      key,
			value:    value,
			locator:  update.Locator,
		})
	}
	sort.Slice(result, func(left, right int) bool {
		return bytes.Compare(result[left].key, result[right].key) < 0
	})
	return result, nil
}

func (index *Index) validateTransitions(
	state treecontrol.State,
	updates []preparedUpdate,
) ([]preparedUpdate, error) {
	var searcher *btree.Searcher
	var err error
	if state.RootPageID != 0 {
		searcher, err = btree.NewSearcher(state.SpaceID, state.RootPageID, index.runtime)
		if err != nil {
			return nil, err
		}
	}
	active := make([]preparedUpdate, 0, len(updates))
	for _, update := range updates {
		current, found, err := lookupWithSearcher(searcher, update.key)
		if err != nil {
			return nil, err
		}
		if found && (current.TableID != update.locator.TableID ||
			current.RowID != update.locator.RowID) {
			return nil, fmt.Errorf("%w: current locator key scope", ErrCorrupt)
		}
		if found && current == update.locator {
			continue
		}
		if update.expected == 0 {
			if found || update.locator.Revision != 1 || update.locator.State != row.StateLive {
				return nil, fmt.Errorf("%w: initial Row transition", ErrConflict)
			}
			active = append(active, update)
			continue
		}
		if !found ||
			current.DatabaseID != update.locator.DatabaseID ||
			current.TableID != update.locator.TableID ||
			current.RowID != update.locator.RowID ||
			current.Revision != update.expected ||
			update.expected == math.MaxUint64 ||
			update.locator.Revision != update.expected+1 ||
			update.locator.CommitSequence <= current.CommitSequence ||
			update.locator.SchemaRevision < current.SchemaRevision {
			return nil, fmt.Errorf("%w: stale or invalid Row transition", ErrConflict)
		}
		active = append(active, update)
	}
	return active, nil
}

func lookupWithSearcher(searcher *btree.Searcher, key []byte) (Locator, bool, error) {
	if searcher == nil {
		return Locator{}, false, nil
	}
	encoded, err := searcher.Get(key)
	if errors.Is(err, btree.ErrNotFound) {
		return Locator{}, false, nil
	}
	if err != nil {
		return Locator{}, false, classifyTreeError(err)
	}
	locator, err := decodeLocator(encoded)
	if err != nil {
		return Locator{}, false, err
	}
	return locator, true, nil
}

func (index *Index) plan(
	state treecontrol.State,
	updates []preparedUpdate,
	rowIDCounter uint64,
) (btree.MutationPlan, error) {
	if state.RootPageID == 0 {
		return planBootstrap(state, updates, rowIDCounter)
	}
	planner, err := btree.NewMutationPlanner(
		state.SpaceID, state.Generation, state.RootPageID, state.NextPageID, index.runtime,
		index.runtime.FreePageIDs()...,
	)
	if err != nil {
		return btree.MutationPlan{}, err
	}
	if rowIDCounter != 0 {
		if err := planner.Upsert(rowIDCounterKey(), encodeRowIDCounter(rowIDCounter)); err != nil {
			return btree.MutationPlan{}, classifyTreeError(err)
		}
	}
	for _, update := range updates {
		if err := planner.Upsert(update.key, update.value); err != nil {
			return btree.MutationPlan{}, classifyTreeError(err)
		}
	}
	return planner.Plan(), nil
}

func planBootstrap(
	state treecontrol.State,
	updates []preparedUpdate,
	rowIDCounter uint64,
) (btree.MutationPlan, error) {
	rootID := treecontrol.FirstDataPageID
	root, err := btree.Encode(page.Header{
		Type: page.TypeBTreeLeaf, SpaceID: state.SpaceID, PageID: rootID,
		Generation: state.Generation,
	}, btree.Node{Kind: btree.KindLeaf})
	if err != nil {
		return btree.MutationPlan{}, err
	}
	reader := btree.ReaderFunc(func(pageID uint64) (page.Page, error) {
		if pageID == rootID {
			return root, nil
		}
		return page.Page{}, page.ErrNotFound
	})
	planner, err := btree.NewMutationPlanner(
		state.SpaceID, state.Generation, rootID, rootID+1, reader,
	)
	if err != nil {
		return btree.MutationPlan{}, err
	}
	if rowIDCounter != 0 {
		if err := planner.Upsert(rowIDCounterKey(), encodeRowIDCounter(rowIDCounter)); err != nil {
			return btree.MutationPlan{}, err
		}
	}
	for _, update := range updates {
		if err := planner.Upsert(update.key, update.value); err != nil {
			return btree.MutationPlan{}, err
		}
	}
	plan := planner.Plan()
	if len(plan.Changes) == 0 {
		plan.Changes = []btree.PageChange{{Page: root}}
	}
	plan.Allocated = append([]uint64{rootID}, plan.Allocated...)
	return plan, nil
}

func classifyTreeError(err error) error {
	if errors.Is(err, btree.ErrCorrupt) || errors.Is(err, page.ErrCorrupt) {
		return fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	return err
}
