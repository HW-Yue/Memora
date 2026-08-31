// Package objectindex is the clustered store for objects that keep one record
// per identity: Routes, Relations, memberships, configuration and the rest.
//
// The leaf holds the object itself, so resolving (kind, id) is a B+Tree descent
// and nothing more — no second file, and no process-resident directory of every
// record that ever existed. Rows are the one exception: their clustered index is
// the Row version tree, keyed by (rowID, revision).
package objectindex

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

const maxPageSize = 1000

type Receipt struct {
	Changed bool
	State   treecontrol.State
	WAL     wal.Receipt
}

// Page is one bounded step of a walk over a single kind. Truncated says another
// step remains; NextAfterID is where it resumes.
type Page struct {
	Records     []Record
	NextAfterID string
	Truncated   bool
}

type Index struct {
	mu      sync.RWMutex
	runtime *treecommit.Runtime
}

type entry struct {
	key      []byte
	value    []byte
	kind     uint16
	id       string
	revision uint64
	expected uint64
}

func Open(runtime *treecommit.Runtime) (*Index, error) {
	if runtime == nil || runtime.State().SpaceID == 0 {
		return nil, fmt.Errorf("%w: durable Tree Runtime", ErrInvalid)
	}
	return &Index{runtime: runtime}, nil
}

// Update publishes one revision of an object.
//
// ExpectedRevision is the revision this update succeeds: 0 for an object the
// Tree has never held. The Tree is derived from the append-only record log, and
// a derived structure that silently accepts a revision out of order stops being
// a copy of what it derives from — so an expectation that disagrees with what
// is stored is refused rather than applied.
type Update struct {
	Record           Record
	ExpectedRevision uint64
}

// Put publishes objects at revision 1.
//
// It is Apply for the objects that are written once and never revised, which is
// most of them: an object that exists is a conflict, and re-publishing identical
// bytes is the no-op that lets a retried publication converge.
func (index *Index) Put(transactionID uint64, records []Record) (Receipt, error) {
	updates := make([]Update, 0, len(records))
	for _, value := range records {
		value.Revision = 1
		updates = append(updates, Update{Record: value})
	}
	return index.Apply(transactionID, updates)
}

// Apply publishes revisions, replacing the ones they succeed.
func (index *Index) Apply(transactionID uint64, updates []Update) (Receipt, error) {
	if index == nil || index.runtime == nil || transactionID == 0 {
		return Receipt{}, fmt.Errorf("%w: Apply request", ErrInvalid)
	}
	if len(updates) == 0 {
		return Receipt{}, nil
	}
	prepared, err := prepare(updates)
	if err != nil {
		return Receipt{}, err
	}

	index.mu.Lock()
	defer index.mu.Unlock()
	plan, changed, err := index.planApplyLocked(prepared)
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
// can be committed in one WAL transaction together with other Trees. That is
// what lets an object land in this Tree and a Row land in its own Table's Tree
// as a single durable fact.
//
// On success the Index's write lock is held until the group commit finishes —
// the group owns releasing it. An Apply with nothing to do adds no member and
// releases immediately.
func (index *Index) StageApply(group *treecommit.Group, updates []Update) error {
	if index == nil || index.runtime == nil || group == nil {
		return fmt.Errorf("%w: Apply request", ErrInvalid)
	}
	if len(updates) == 0 {
		return nil
	}
	prepared, err := prepare(updates)
	if err != nil {
		return err
	}
	index.mu.Lock()
	plan, changed, err := index.planApplyLocked(prepared)
	if err != nil || !changed {
		index.mu.Unlock()
		return err
	}
	group.Add(index.runtime, plan, index.mu.Unlock)
	return nil
}

// planApplyLocked builds the mutation plan for an Apply. The caller holds the
// write lock; changed is false when the revisions are already in the Tree.
func (index *Index) planApplyLocked(prepared []entry) (btree.MutationPlan, bool, error) {
	state := index.runtime.State()
	active, err := index.pending(state, prepared)
	if err != nil {
		return btree.MutationPlan{}, false, err
	}
	if len(active) == 0 {
		return btree.MutationPlan{}, false, nil
	}
	plan, err := index.plan(state, active)
	if err != nil {
		return btree.MutationPlan{}, false, err
	}
	return plan, true, nil
}

func (index *Index) Bootstrap(transactionID uint64) (Receipt, error) {
	if index == nil || index.runtime == nil || transactionID == 0 {
		return Receipt{}, fmt.Errorf("%w: Bootstrap request", ErrInvalid)
	}
	index.mu.Lock()
	defer index.mu.Unlock()
	state := index.runtime.State()
	if state.RootPageID != 0 {
		return Receipt{State: state}, nil
	}
	plan, err := planBootstrap(state, nil)
	if err != nil {
		return Receipt{}, err
	}
	committed, err := index.runtime.Commit(transactionID, plan)
	if err != nil {
		return Receipt{}, err
	}
	return Receipt{Changed: true, State: committed.State, WAL: committed.WAL}, nil
}

func (index *Index) Get(kind uint16, id string) ([]byte, error) {
	value, err := index.Lookup(kind, id)
	if err != nil {
		return nil, err
	}
	return value.Body, nil
}

// Lookup resolves (kind, id) to the stored object, revision included. Get is
// the same descent for callers that only want the bytes.
func (index *Index) Lookup(kind uint16, id string) (Record, error) {
	if index == nil || index.runtime == nil {
		return Record{}, fmt.Errorf("%w: lookup Index", ErrInvalid)
	}
	key, err := recordKey(kind, id)
	if err != nil {
		return Record{}, err
	}
	index.mu.RLock()
	defer index.mu.RUnlock()
	searcher, err := index.searcher()
	if err != nil {
		return Record{}, err
	}
	if searcher == nil {
		return Record{}, ErrNotFound
	}
	value, found, err := get(searcher, key)
	if err != nil {
		return Record{}, err
	}
	if !found {
		return Record{}, ErrNotFound
	}
	if value.Kind != kind || value.ID != id {
		return Record{}, fmt.Errorf("%w: record identity disagrees with its key", ErrCorrupt)
	}
	return value, nil
}

// Page walks one kind in ID order, starting after afterID. It replaces
// enumerating every record in the Database: the cost is the records returned,
// not the records that exist.
func (index *Index) Page(kind uint16, afterID string, limit int) (Page, error) {
	if index == nil || index.runtime == nil || limit < 1 || limit > maxPageSize {
		return Page{}, fmt.Errorf("%w: Page request", ErrInvalid)
	}
	prefix, err := kindPrefix(kind)
	if err != nil {
		return Page{}, err
	}
	end, err := prefixSuccessor(prefix)
	if err != nil {
		return Page{}, err
	}
	start := prefix
	if afterID != "" {
		exclusive, err := recordKey(kind, afterID)
		if err != nil {
			return Page{}, err
		}
		// The cursor is inclusive, so resume one key past the record already seen.
		start = append(exclusive, 0)
	}

	index.mu.RLock()
	defer index.mu.RUnlock()
	searcher, err := index.searcher()
	if err != nil {
		return Page{}, err
	}
	if searcher == nil {
		return Page{}, nil
	}
	cursor, err := searcher.NewCursor(start, end)
	if err != nil {
		return Page{}, err
	}
	// One entry past the page answers "is there more" without a second scan.
	batch, err := cursor.Next(uint64(limit) + 1)
	if err != nil {
		return Page{}, classifyTreeError(err)
	}
	result := Page{Records: make([]Record, 0, min(len(batch.Entries), limit))}
	for position, found := range batch.Entries {
		if position == limit {
			result.Truncated = true
			break
		}
		value, err := decodeRecord(found.Value)
		if err != nil {
			return Page{}, err
		}
		if value.Kind != kind {
			return Page{}, fmt.Errorf("%w: record of kind %d inside kind %d range", ErrCorrupt, value.Kind, kind)
		}
		result.Records = append(result.Records, value)
	}
	if result.Truncated {
		result.NextAfterID = result.Records[len(result.Records)-1].ID
	}
	return result, nil
}

func (index *Index) searcher() (*btree.Searcher, error) {
	state := index.runtime.State()
	if state.RootPageID == 0 {
		return nil, nil
	}
	return btree.NewSearcher(state.SpaceID, state.RootPageID, index.runtime)
}

func prepare(updates []Update) ([]entry, error) {
	prepared := make([]entry, 0, len(updates))
	seen := make(map[string]struct{}, len(updates))
	for _, update := range updates {
		value := update.Record
		if update.ExpectedRevision >= value.Revision {
			return nil, fmt.Errorf(
				"%w: record %q of kind %d publishes revision %d after revision %d",
				ErrInvalid, value.ID, value.Kind, value.Revision, update.ExpectedRevision)
		}
		key, err := recordKey(value.Kind, value.ID)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[string(key)]; duplicate {
			return nil, fmt.Errorf("%w: record %q of kind %d appears twice in one batch",
				ErrConflict, value.ID, value.Kind)
		}
		seen[string(key)] = struct{}{}
		encoded, err := encodeRecord(value)
		if err != nil {
			return nil, err
		}
		prepared = append(prepared, entry{
			key: key, value: encoded, kind: value.Kind, id: value.ID,
			revision: value.Revision, expected: update.ExpectedRevision,
		})
	}
	sort.Slice(prepared, func(left, right int) bool {
		return bytes.Compare(prepared[left].key, prepared[right].key) < 0
	})
	return prepared, nil
}

// pending drops revisions already stored with identical bytes and refuses any
// whose expectation disagrees with what the Tree holds.
func (index *Index) pending(state treecontrol.State, prepared []entry) ([]entry, error) {
	var searcher *btree.Searcher
	if state.RootPageID != 0 {
		found, err := btree.NewSearcher(state.SpaceID, state.RootPageID, index.runtime)
		if err != nil {
			return nil, err
		}
		searcher = found
	}
	active := make([]entry, 0, len(prepared))
	for _, candidate := range prepared {
		var (
			stored Record
			found  bool
			err    error
		)
		if searcher != nil {
			stored, found, err = get(searcher, candidate.key)
			if err != nil {
				return nil, err
			}
		}
		if !found {
			if candidate.expected != 0 {
				return nil, fmt.Errorf(
					"%w: record %q of kind %d succeeds revision %d, but the Tree holds none",
					ErrConflict, candidate.id, candidate.kind, candidate.expected)
			}
			active = append(active, candidate)
			continue
		}
		// A caller cannot always know whether its last attempt landed, so
		// re-sending the stored revision byte for byte converges instead of
		// conflicting. Different bytes under the same revision do not: that is
		// two different objects claiming one revision.
		if stored.Revision == candidate.revision {
			existing, err := encodeRecord(stored)
			if err != nil {
				return nil, err
			}
			if !bytes.Equal(existing, candidate.value) {
				return nil, fmt.Errorf("%w: record %q of kind %d is already published at revision %d with different bytes",
					ErrConflict, stored.ID, stored.Kind, stored.Revision)
			}
			continue
		}
		if candidate.expected != stored.Revision {
			return nil, fmt.Errorf(
				"%w: record %q of kind %d succeeds revision %d, but the Tree holds revision %d",
				ErrConflict, candidate.id, candidate.kind, candidate.expected, stored.Revision)
		}
		active = append(active, candidate)
	}
	return active, nil
}

func get(searcher *btree.Searcher, key []byte) (Record, bool, error) {
	encoded, err := searcher.Get(key)
	if errors.Is(err, btree.ErrNotFound) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, classifyTreeError(err)
	}
	value, err := decodeRecord(encoded)
	if err != nil {
		return Record{}, false, err
	}
	return value, true, nil
}

func (index *Index) plan(state treecontrol.State, entries []entry) (btree.MutationPlan, error) {
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

func planBootstrap(state treecontrol.State, entries []entry) (btree.MutationPlan, error) {
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
	// With no entries the planner reports nothing to write, but the root page
	// was still allocated — and an allocation with no change is not a valid
	// plan. Writing the empty root itself is the change.
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
