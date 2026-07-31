package rowversionindex

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/HW-Yue/Memora/internal/store/btree"
	"github.com/HW-Yue/Memora/internal/store/page"
	"github.com/HW-Yue/Memora/internal/store/treecommit"
	"github.com/HW-Yue/Memora/internal/store/treecontrol"
	"github.com/HW-Yue/Memora/internal/store/wal"
)

type Receipt struct {
	Changed bool
	State   treecontrol.State
	WAL     wal.Receipt
}

type Index struct {
	mu      sync.RWMutex
	runtime *treecommit.Runtime
}

type preparedLocator struct {
	locator     Locator
	value       []byte
	revisionKey []byte
	commitKey   []byte
	identityKey []byte
}

type entry struct {
	key   []byte
	value []byte
}

type identityStatus struct {
	found     bool
	locator   Locator
	scheduled bool
}

func Open(runtime *treecommit.Runtime) (*Index, error) {
	if runtime == nil || runtime.State().SpaceID == 0 {
		return nil, fmt.Errorf("%w: durable Tree Runtime", ErrInvalid)
	}
	return &Index{runtime: runtime}, nil
}

func (index *Index) Append(transactionID uint64, locators []Locator) (Receipt, error) {
	if index == nil || index.runtime == nil || transactionID == 0 || len(locators) == 0 {
		return Receipt{}, fmt.Errorf("%w: Append request", ErrInvalid)
	}
	prepared, err := prepareLocators(locators)
	if err != nil {
		return Receipt{}, err
	}

	index.mu.Lock()
	defer index.mu.Unlock()
	state := index.runtime.State()
	active, err := index.validateAppend(state, prepared)
	if err != nil {
		return Receipt{}, err
	}
	if len(active) == 0 {
		return Receipt{State: state}, nil
	}
	sort.Slice(active, func(left, right int) bool {
		return bytes.Compare(active[left].key, active[right].key) < 0
	})
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

func (index *Index) ByRevision(rowID string, revision uint64) (Locator, error) {
	key, err := revisionKey(rowID, revision)
	if err != nil {
		return Locator{}, err
	}
	locator, err := index.point(key)
	if err != nil {
		return Locator{}, err
	}
	if locator.RowID != rowID || locator.Revision != revision {
		return Locator{}, fmt.Errorf("%w: revision locator key mismatch", ErrCorrupt)
	}
	return locator, nil
}

func (index *Index) ByCommit(rowID string, sequence uint64) (Locator, error) {
	start, err := commitStart(rowID, sequence)
	if err != nil {
		return Locator{}, err
	}
	end, err := prefixSuccessor(start)
	if err != nil {
		return Locator{}, err
	}
	locator, err := index.first(start, end)
	if err != nil {
		return Locator{}, err
	}
	if locator.RowID != rowID || locator.CommitSequence != sequence {
		return Locator{}, fmt.Errorf("%w: commit locator key mismatch", ErrCorrupt)
	}
	return locator, nil
}

func (index *Index) AsOfCommit(rowID string, sequence uint64) (Locator, error) {
	start, err := commitStart(rowID, sequence)
	if err != nil {
		return Locator{}, err
	}
	prefix, err := commitRowPrefix(rowID)
	if err != nil {
		return Locator{}, err
	}
	end, err := prefixSuccessor(prefix)
	if err != nil {
		return Locator{}, err
	}
	locator, err := index.first(start, end)
	if err != nil {
		return Locator{}, err
	}
	if locator.RowID != rowID ||
		locator.CommitSequence == 0 ||
		locator.CommitSequence > sequence {
		return Locator{}, fmt.Errorf("%w: AS OF locator key mismatch", ErrCorrupt)
	}
	return locator, nil
}

func (index *Index) point(key []byte) (Locator, error) {
	if index == nil || index.runtime == nil {
		return Locator{}, fmt.Errorf("%w: lookup Index", ErrInvalid)
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
	value, found, err := get(searcher, key)
	if err != nil {
		return Locator{}, err
	}
	if !found {
		return Locator{}, ErrNotFound
	}
	return value, nil
}

func (index *Index) first(start, end []byte) (Locator, error) {
	if index == nil || index.runtime == nil {
		return Locator{}, fmt.Errorf("%w: lookup Index", ErrInvalid)
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
	cursor, err := searcher.NewCursor(start, end)
	if err != nil {
		return Locator{}, err
	}
	batch, err := cursor.Next(1)
	if err != nil {
		return Locator{}, classifyTreeError(err)
	}
	if len(batch.Entries) == 0 {
		return Locator{}, ErrNotFound
	}
	locator, err := decodeLocator(batch.Entries[0].Value)
	if err != nil {
		return Locator{}, err
	}
	return locator, nil
}

func prepareLocators(locators []Locator) ([]preparedLocator, error) {
	result := make([]preparedLocator, 0, len(locators))
	revisions := make(map[string]struct{}, len(locators))
	identities := make(map[string]Locator)
	for _, locator := range locators {
		value, err := encodeLocator(locator)
		if err != nil {
			return nil, err
		}
		revision, err := revisionKey(locator.RowID, locator.Revision)
		if err != nil {
			return nil, err
		}
		if _, duplicate := revisions[string(revision)]; duplicate {
			return nil, fmt.Errorf("%w: duplicate Row revision in batch", ErrConflict)
		}
		revisions[string(revision)] = struct{}{}
		identity, err := identityKey(locator.RowID)
		if err != nil {
			return nil, err
		}
		if previous, exists := identities[locator.RowID]; exists &&
			(previous.DatabaseID != locator.DatabaseID ||
				previous.TableID != locator.TableID) {
			return nil, fmt.Errorf("%w: Row identity drifts in batch", ErrConflict)
		}
		identities[locator.RowID] = locator
		var commit []byte
		if locator.CommitSequence != 0 {
			commit, err = commitKey(locator.RowID, locator.CommitSequence, locator.Revision)
			if err != nil {
				return nil, err
			}
		}
		result = append(result, preparedLocator{
			locator: locator, value: value, revisionKey: revision,
			commitKey: commit, identityKey: identity,
		})
	}
	sort.Slice(result, func(left, right int) bool {
		return bytes.Compare(result[left].revisionKey, result[right].revisionKey) < 0
	})
	return result, nil
}

func (index *Index) validateAppend(
	state treecontrol.State,
	locators []preparedLocator,
) ([]entry, error) {
	var searcher *btree.Searcher
	var err error
	if state.RootPageID != 0 {
		searcher, err = btree.NewSearcher(state.SpaceID, state.RootPageID, index.runtime)
		if err != nil {
			return nil, err
		}
	}
	active := make([]entry, 0, len(locators)*2)
	identities := make(map[string]*identityStatus)
	for _, prepared := range locators {
		status, exists := identities[prepared.locator.RowID]
		if !exists {
			status = &identityStatus{}
			identities[prepared.locator.RowID] = status
			if searcher != nil {
				identity, found, err := get(searcher, prepared.identityKey)
				if err != nil {
					return nil, err
				}
				status.found, status.locator = found, identity
			}
		}
		if status.found &&
			(status.locator.RowID != prepared.locator.RowID ||
				status.locator.DatabaseID != prepared.locator.DatabaseID ||
				status.locator.TableID != prepared.locator.TableID) {
			return nil, fmt.Errorf("%w: stable Row identity", ErrConflict)
		}

		current, revisionFound, err := get(searcher, prepared.revisionKey)
		if err != nil {
			return nil, err
		}
		if revisionFound {
			if !status.found {
				return nil, fmt.Errorf("%w: revision exists without identity", ErrCorrupt)
			}
			if current != prepared.locator {
				return nil, fmt.Errorf("%w: Row revision", ErrConflict)
			}
			if len(prepared.commitKey) != 0 {
				commit, found, err := get(searcher, prepared.commitKey)
				if err != nil {
					return nil, err
				}
				if !found || commit != prepared.locator {
					return nil, fmt.Errorf("%w: revision/commit key mismatch", ErrCorrupt)
				}
			}
			continue
		}
		if len(prepared.commitKey) != 0 {
			if _, found, err := get(searcher, prepared.commitKey); err != nil {
				return nil, err
			} else if found {
				return nil, fmt.Errorf("%w: commit exists without revision", ErrCorrupt)
			}
		}
		if !status.found && !status.scheduled {
			active = append(active, entry{key: prepared.identityKey, value: prepared.value})
			status.scheduled = true
		}
		active = append(active, entry{key: prepared.revisionKey, value: prepared.value})
		if len(prepared.commitKey) != 0 {
			active = append(active, entry{key: prepared.commitKey, value: prepared.value})
		}
	}
	return active, nil
}

func get(searcher *btree.Searcher, key []byte) (Locator, bool, error) {
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
	entries []entry,
) (btree.MutationPlan, error) {
	if state.RootPageID == 0 {
		return planBootstrap(state, entries)
	}
	planner, err := btree.NewMutationPlanner(
		state.SpaceID, state.Generation, state.RootPageID, state.NextPageID, index.runtime,
	)
	if err != nil {
		return btree.MutationPlan{}, err
	}
	for _, value := range entries {
		if err := planner.Upsert(value.key, value.value); err != nil {
			return btree.MutationPlan{}, classifyTreeError(err)
		}
	}
	return planner.Plan(), nil
}

func planBootstrap(
	state treecontrol.State,
	entries []entry,
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
	for _, value := range entries {
		if err := planner.Upsert(value.key, value.value); err != nil {
			return btree.MutationPlan{}, err
		}
	}
	plan := planner.Plan()
	plan.Allocated = append([]uint64{rootID}, plan.Allocated...)
	return plan, nil
}

func classifyTreeError(err error) error {
	if errors.Is(err, btree.ErrCorrupt) || errors.Is(err, page.ErrCorrupt) {
		return fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	return err
}
