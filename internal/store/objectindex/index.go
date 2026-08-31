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
	key   []byte
	value []byte
}

func Open(runtime *treecommit.Runtime) (*Index, error) {
	if runtime == nil || runtime.State().SpaceID == 0 {
		return nil, fmt.Errorf("%w: durable Tree Runtime", ErrInvalid)
	}
	return &Index{runtime: runtime}, nil
}

// Put publishes records. A record is immutable once published, the same rule the
// append-only record log enforced by refusing a duplicate record ID: rewriting
// one with different bytes is a conflict, and re-publishing identical bytes is
// the no-op that lets a retried publication converge.
func (index *Index) Put(transactionID uint64, records []Record) (Receipt, error) {
	if index == nil || index.runtime == nil || transactionID == 0 {
		return Receipt{}, fmt.Errorf("%w: Put request", ErrInvalid)
	}
	if len(records) == 0 {
		return Receipt{}, nil
	}
	prepared, err := prepare(records)
	if err != nil {
		return Receipt{}, err
	}

	index.mu.Lock()
	defer index.mu.Unlock()
	state := index.runtime.State()
	active, err := index.pending(state, prepared)
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

// Bootstrap plants the empty root.
//
// Put cannot do it: it returns early when given no records, so a Tree that has
// never held anything would have no root at all. A Tree with no root is a
// second empty state for every reader to know about, and the generation
// manifest's own invariant is that a Tree has one — so a Tree is born with an
// empty root instead of without one.
//
// It is a no-op on a Tree that already has a root, which is what makes it safe
// to call on every open.
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
	if index == nil || index.runtime == nil {
		return nil, fmt.Errorf("%w: lookup Index", ErrInvalid)
	}
	key, err := recordKey(kind, id)
	if err != nil {
		return nil, err
	}
	index.mu.RLock()
	defer index.mu.RUnlock()
	searcher, err := index.searcher()
	if err != nil || searcher == nil {
		if err != nil {
			return nil, err
		}
		return nil, ErrNotFound
	}
	value, found, err := get(searcher, key)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrNotFound
	}
	if value.Kind != kind || value.ID != id {
		return nil, fmt.Errorf("%w: record identity disagrees with its key", ErrCorrupt)
	}
	return value.Body, nil
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

func prepare(records []Record) ([]entry, error) {
	prepared := make([]entry, 0, len(records))
	seen := make(map[string]struct{}, len(records))
	for _, value := range records {
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
		prepared = append(prepared, entry{key: key, value: encoded})
	}
	sort.Slice(prepared, func(left, right int) bool {
		return bytes.Compare(prepared[left].key, prepared[right].key) < 0
	})
	return prepared, nil
}

// pending drops records already stored with identical bytes and refuses any that
// would rewrite a published record.
func (index *Index) pending(state treecontrol.State, prepared []entry) ([]entry, error) {
	if state.RootPageID == 0 {
		return prepared, nil
	}
	searcher, err := btree.NewSearcher(state.SpaceID, state.RootPageID, index.runtime)
	if err != nil {
		return nil, err
	}
	active := make([]entry, 0, len(prepared))
	for _, candidate := range prepared {
		stored, found, err := get(searcher, candidate.key)
		if err != nil {
			return nil, err
		}
		if !found {
			active = append(active, candidate)
			continue
		}
		existing, err := encodeRecord(stored)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(existing, candidate.value) {
			return nil, fmt.Errorf("%w: record %q of kind %d is already published with different bytes",
				ErrConflict, stored.ID, stored.Kind)
		}
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
