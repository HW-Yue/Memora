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
func (index *Index) Bootstrap(transactionID uint64, locators []Locator) (Receipt, error) {
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
	plan, err := planBootstrap(state, prepared)
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
	if index == nil || index.runtime == nil || transactionID == 0 || len(updates) == 0 {
		return Receipt{}, fmt.Errorf("%w: Apply request", ErrInvalid)
	}
	prepared, err := prepareUpdates(updates)
	if err != nil {
		return Receipt{}, err
	}

	index.mu.Lock()
	defer index.mu.Unlock()
	state := index.runtime.State()
	active, err := index.validateTransitions(state, prepared)
	if err != nil {
		return Receipt{}, err
	}
	if len(active) == 0 {
		return Receipt{State: state}, nil
	}
	plan, err := index.plan(state, active)
	if err != nil {
		return Receipt{}, err
	}
	committed, err := index.runtime.Commit(transactionID, plan)
	if err != nil {
		return Receipt{}, err
	}
	return Receipt{Changed: true, State: committed.State, WAL: committed.WAL}, nil
}

func (index *Index) Lookup(tableID, rowID string) (Locator, error) {
	if index == nil || index.runtime == nil {
		return Locator{}, fmt.Errorf("%w: lookup Index", ErrInvalid)
	}
	key, err := encodeKey(tableID, rowID)
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
	if locator.TableID != tableID || locator.RowID != rowID {
		return Locator{}, fmt.Errorf("%w: locator does not match key scope", ErrCorrupt)
	}
	return locator, nil
}

func prepareUpdates(updates []Update) ([]preparedUpdate, error) {
	result := make([]preparedUpdate, 0, len(updates))
	seen := make(map[string]struct{}, len(updates))
	for _, update := range updates {
		key, err := encodeKey(update.Locator.TableID, update.Locator.RowID)
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
) (btree.MutationPlan, error) {
	if state.RootPageID == 0 {
		return planBootstrap(state, updates)
	}
	planner, err := btree.NewMutationPlanner(
		state.SpaceID, state.Generation, state.RootPageID, state.NextPageID, index.runtime,
		index.runtime.FreePageIDs()...,
	)
	if err != nil {
		return btree.MutationPlan{}, err
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
