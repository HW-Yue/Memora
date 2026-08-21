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
	locator Locator
	// secondary is locator without its Body, which is what the identity, commit
	// and legacy keys store and therefore what a read of those keys compares to.
	secondary      Locator
	value          []byte
	clusteredValue []byte
	revisionKey    []byte
	commitKey      []byte
	identityKey    []byte
	legacyKey      []byte
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

// Bootstrap creates the complete immutable Row history and snapshot marker in
// one empty Tree. Later changes must use Append and its sealed-history rules.
func (index *Index) Bootstrap(transactionID uint64, locators []Locator) (Receipt, error) {
	if index == nil || index.runtime == nil || transactionID == 0 {
		return Receipt{}, fmt.Errorf("%w: Bootstrap request", ErrInvalid)
	}
	prepared, err := prepareLocators(locators)
	if err != nil {
		return Receipt{}, err
	}

	index.mu.Lock()
	defer index.mu.Unlock()
	state := index.runtime.State()
	if state.RootPageID != 0 {
		return Receipt{}, fmt.Errorf("%w: Row Version authority is not empty", ErrConflict)
	}
	active, err := index.validateAppend(state, prepared)
	if err != nil {
		return Receipt{}, err
	}
	sort.Slice(active, func(left, right int) bool {
		return bytes.Compare(active[left].key, active[right].key) < 0
	})
	plan, err := planBootstrap(state, active)
	if err != nil {
		return Receipt{}, err
	}
	committed, err := index.runtime.Commit(transactionID, plan)
	if err != nil {
		return Receipt{}, err
	}
	return Receipt{Changed: true, State: committed.State, WAL: committed.WAL}, nil
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

const maxRevisionsPageSize = 1000

// RevisionsPage is one bounded step of a walk over a single Row's revisions,
// newest first. Truncated says another step remains; NextBeforeRevision is where
// it resumes.
type RevisionsPage struct {
	Locators           []Locator
	NextBeforeRevision uint64
	Truncated          bool
}

// RevisionsPage walks one Row's revisions newest first. Revisions of a Row are
// adjacent in key order, so this is a range scan along the leaf chain: it costs
// the revisions it returns, not the revisions that exist, and it never touches
// another Row. It replaces reading every history record in the Database.
func (index *Index) RevisionsPage(rowID string, beforeRevision uint64, limit int) (RevisionsPage, error) {
	if index == nil || index.runtime == nil || limit < 1 || limit > maxRevisionsPageSize {
		return RevisionsPage{}, fmt.Errorf("%w: RevisionsPage request", ErrInvalid)
	}
	prefix, err := rowPrefix(keyRevision, rowID)
	if err != nil {
		return RevisionsPage{}, err
	}
	end, err := prefixSuccessor(prefix)
	if err != nil {
		return RevisionsPage{}, err
	}
	if beforeRevision != 0 {
		// Revisions sort ascending, so "newest first" reads the range and reverses
		// it. Resuming below a revision means ending the range there, exclusively.
		if end, err = revisionKey(rowID, beforeRevision); err != nil {
			return RevisionsPage{}, err
		}
	}

	index.mu.RLock()
	defer index.mu.RUnlock()
	state := index.runtime.State()
	if state.RootPageID == 0 {
		return RevisionsPage{}, nil
	}
	searcher, err := btree.NewSearcher(state.SpaceID, state.RootPageID, index.runtime)
	if err != nil {
		return RevisionsPage{}, err
	}
	collected, err := tailLocators(searcher, prefix, end, limit+1)
	if err != nil {
		return RevisionsPage{}, err
	}
	result := RevisionsPage{Locators: make([]Locator, 0, limit)}
	// collected is oldest-first and holds at most limit+1 entries ending at the
	// newest. Reverse it, then trim: the extra entry is the oldest one, which is
	// exactly the evidence that another page remains.
	for position := len(collected) - 1; position >= 0; position-- {
		if len(result.Locators) == limit {
			result.Truncated = true
			break
		}
		result.Locators = append(result.Locators, collected[position])
	}
	if result.Truncated {
		result.NextBeforeRevision = result.Locators[len(result.Locators)-1].Revision
	}
	return result, nil
}

// tailLocators returns the last `keep` entries of a key range in ascending order.
// The cursor only walks forwards, so reaching the newest revisions means walking
// the range while holding a sliding window of the most recent entries seen.
func tailLocators(searcher *btree.Searcher, start, end []byte, keep int) ([]Locator, error) {
	cursor, err := searcher.NewCursor(start, end)
	if err != nil {
		return nil, err
	}
	window := make([]Locator, 0, keep)
	for {
		batch, err := cursor.Next(uint64(keep))
		if err != nil {
			return nil, classifyTreeError(err)
		}
		for _, found := range batch.Entries {
			locator, err := decodeLocator(found.Value)
			if err != nil {
				return nil, err
			}
			if len(window) == keep {
				window = append(window[1:], locator)
				continue
			}
			window = append(window, locator)
		}
		if batch.Done {
			return window, nil
		}
	}
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

// HighWater returns the largest commit sequence whose complete Version batch
// is published. An empty tree has the baseline sequence zero.
func (index *Index) HighWater() (uint64, error) {
	if index == nil || index.runtime == nil {
		return 0, fmt.Errorf("%w: lookup Index", ErrInvalid)
	}
	index.mu.RLock()
	defer index.mu.RUnlock()
	state := index.runtime.State()
	if state.RootPageID == 0 {
		return 0, nil
	}
	searcher, err := btree.NewSearcher(state.SpaceID, state.RootPageID, index.runtime)
	if err != nil {
		return 0, err
	}
	sequence, found, err := highWaterWithSearcher(searcher)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, fmt.Errorf("%w: snapshot high-water is missing", ErrCorrupt)
	}
	return sequence, nil
}

// VisibleAt returns the latest sequenced revision not newer than the snapshot.
// If none exists, the highest legacy sequence-zero revision remains visible.
func (index *Index) VisibleAt(rowID string, sequence uint64) (Locator, error) {
	if sequence != 0 {
		locator, err := index.AsOfCommit(rowID, sequence)
		if err == nil {
			return locator, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return Locator{}, err
		}
	}
	key, err := legacyKey(rowID)
	if err != nil {
		return Locator{}, err
	}
	locator, err := index.point(key)
	if err != nil {
		return Locator{}, err
	}
	if locator.RowID != rowID || locator.CommitSequence != 0 {
		return Locator{}, fmt.Errorf("%w: legacy locator key mismatch", ErrCorrupt)
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
		// The clustered value carries the Row; the secondary value is the same
		// Locator without it. Only the (rowID, revision) key gets the Row.
		clustered, err := encodeLocator(locator)
		if err != nil {
			return nil, err
		}
		secondary := locator
		secondary.Body, secondary.History = "", ""
		value, err := encodeLocator(secondary)
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
		var legacy []byte
		if locator.CommitSequence != 0 {
			commit, err = commitKey(locator.RowID, locator.CommitSequence, locator.Revision)
			if err != nil {
				return nil, err
			}
		} else {
			legacy, err = legacyKey(locator.RowID)
			if err != nil {
				return nil, err
			}
		}
		result = append(result, preparedLocator{
			locator: locator, secondary: secondary,
			value: value, clusteredValue: clustered, revisionKey: revision,
			commitKey: commit, identityKey: identity, legacyKey: legacy,
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
	legacy := make(map[string]preparedLocator)
	highWater, markerFound, err := highWaterWithSearcher(searcher)
	if err != nil {
		return nil, err
	}
	if searcher != nil && !markerFound {
		return nil, fmt.Errorf("%w: snapshot high-water is missing", ErrCorrupt)
	}
	desiredHighWater := highWater
	for _, prepared := range locators {
		if prepared.locator.CommitSequence > desiredHighWater {
			desiredHighWater = prepared.locator.CommitSequence
		}
		if len(prepared.legacyKey) != 0 {
			if current, exists := legacy[prepared.locator.RowID]; !exists || prepared.locator.Revision > current.locator.Revision {
				legacy[prepared.locator.RowID] = prepared
			}
		}
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
				if !found || commit != prepared.secondary {
					return nil, fmt.Errorf("%w: revision/commit key mismatch", ErrCorrupt)
				}
			}
			continue
		}
		if markerFound && len(prepared.legacyKey) != 0 {
			return nil, fmt.Errorf("%w: legacy sequence-zero import is sealed", ErrConflict)
		}
		if markerFound && prepared.locator.CommitSequence <= highWater {
			return nil, fmt.Errorf("%w: committed snapshot history is sealed", ErrConflict)
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
		active = append(active, entry{key: prepared.revisionKey, value: prepared.clusteredValue})
		if len(prepared.commitKey) != 0 {
			active = append(active, entry{key: prepared.commitKey, value: prepared.value})
		}
	}
	legacyIDs := make([]string, 0, len(legacy))
	for rowID := range legacy {
		legacyIDs = append(legacyIDs, rowID)
	}
	sort.Strings(legacyIDs)
	for _, rowID := range legacyIDs {
		candidate := legacy[rowID]
		current, found, err := get(searcher, candidate.legacyKey)
		if err != nil {
			return nil, err
		}
		if found {
			if current.RowID != rowID || current.CommitSequence != 0 ||
				current.DatabaseID != candidate.locator.DatabaseID ||
				current.TableID != candidate.locator.TableID {
				return nil, fmt.Errorf("%w: legacy Row anchor", ErrCorrupt)
			}
			if current.Revision >= candidate.locator.Revision {
				continue
			}
		}
		active = append(active, entry{key: candidate.legacyKey, value: candidate.value})
	}
	if !markerFound || desiredHighWater > highWater {
		active = append(active, entry{key: snapshotHighWaterKey(), value: encodeHighWater(desiredHighWater)})
	}
	return active, nil
}

func highWaterWithSearcher(searcher *btree.Searcher) (uint64, bool, error) {
	if searcher == nil {
		return 0, false, nil
	}
	encoded, err := searcher.Get(snapshotHighWaterKey())
	if errors.Is(err, btree.ErrNotFound) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, classifyTreeError(err)
	}
	sequence, err := decodeHighWater(encoded)
	if err != nil {
		return 0, false, err
	}
	return sequence, true, nil
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
		index.runtime.FreePageIDs()...,
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
