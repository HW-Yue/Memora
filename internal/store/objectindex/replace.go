package objectindex

import (
	"bytes"
	"fmt"
	"sort"

	"github.com/HW-Yue/Memora/internal/store/btree"
	"github.com/HW-Yue/Memora/internal/store/treecommit"
)

// ReplaceKinds makes the Tree's entries for the named kinds exactly records.
//
// It is the shape an object family needs when one publication owns the whole
// family: the Catalog. A Catalog write hands over the complete set of
// Databases, Tables and Columns, and anything of those kinds the Tree still
// holds is a dropped object — DROP COLUMN is a real statement, so a family that
// only ever grew would keep bodies nothing points at any more.
//
// Kinds outside kinds are untouched, which is what lets Routes and the Catalog
// share one Tree: a Route is revised one at a time and never wholly replaced,
// while the Catalog is only ever published whole.
//
// There is no compare-and-set here, and it is not missing: a replace carries the
// entire desired state, so there is no earlier state to agree with. The check
// that matters is between Trees — the same Catalog goes into the Catalog Tree in
// the same group commit — and a caller gets that by staging both.
func (index *Index) ReplaceKinds(transactionID uint64, kinds []uint16, records []Record) (Receipt, error) {
	if index == nil || index.runtime == nil || transactionID == 0 {
		return Receipt{}, fmt.Errorf("%w: ReplaceKinds request", ErrInvalid)
	}
	prepared, owned, err := prepareReplacement(kinds, records)
	if err != nil {
		return Receipt{}, err
	}
	index.mu.Lock()
	defer index.mu.Unlock()
	plan, changed, err := index.planReplaceLocked(owned, prepared)
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

// StageReplaceKinds is ReplaceKinds enrolled in a group, so the Catalog can land
// in this Tree and in the Catalog Tree as one WAL transaction. See ReplaceKinds.
func (index *Index) StageReplaceKinds(
	group *treecommit.Group, kinds []uint16, records []Record,
) error {
	if index == nil || index.runtime == nil || group == nil {
		return fmt.Errorf("%w: ReplaceKinds request", ErrInvalid)
	}
	prepared, owned, err := prepareReplacement(kinds, records)
	if err != nil {
		return err
	}
	index.mu.Lock()
	plan, changed, err := index.planReplaceLocked(owned, prepared)
	if err != nil || !changed {
		index.mu.Unlock()
		return err
	}
	group.Add(index.runtime, plan, index.mu.Unlock)
	return nil
}

// prepareReplacement encodes the desired records and reports the kinds the
// replacement owns. Every record must belong to one of them: a record of some
// other kind would be written and then never cleaned up, because nothing would
// look at that kind again.
func prepareReplacement(kinds []uint16, records []Record) ([]entry, map[uint16]struct{}, error) {
	if len(kinds) == 0 {
		return nil, nil, fmt.Errorf("%w: ReplaceKinds names no kind", ErrInvalid)
	}
	owned := make(map[uint16]struct{}, len(kinds))
	for _, kind := range kinds {
		if kind == 0 {
			return nil, nil, fmt.Errorf("%w: object kind is required", ErrInvalid)
		}
		owned[kind] = struct{}{}
	}
	updates := make([]Update, 0, len(records))
	for _, value := range records {
		if _, mine := owned[value.Kind]; !mine {
			return nil, nil, fmt.Errorf("%w: record %q is of kind %d, which this replacement does not own",
				ErrInvalid, value.ID, value.Kind)
		}
		updates = append(updates, Update{Record: value})
	}
	prepared, err := prepare(updates)
	if err != nil {
		return nil, nil, err
	}
	return prepared, owned, nil
}

// planReplaceLocked diffs the owned kinds against the desired entries. The
// caller holds the write lock; changed is false when the Tree already says this.
func (index *Index) planReplaceLocked(
	owned map[uint16]struct{}, desired []entry,
) (btree.MutationPlan, bool, error) {
	state := index.runtime.State()
	if state.RootPageID == 0 {
		if len(desired) == 0 {
			return btree.MutationPlan{}, false, nil
		}
		plan, err := planBootstrap(state, desired)
		return plan, err == nil, err
	}
	stored, err := index.storedKeys(owned)
	if err != nil {
		return btree.MutationPlan{}, false, err
	}
	wanted := make(map[string][]byte, len(desired))
	for _, value := range desired {
		wanted[string(value.key)] = value.value
	}
	removed := make([]string, 0)
	for key := range stored {
		if _, keep := wanted[key]; !keep {
			removed = append(removed, key)
		}
	}
	sort.Strings(removed)
	written := make([]entry, 0, len(desired))
	for _, value := range desired {
		if existing, exists := stored[string(value.key)]; exists && bytes.Equal(existing, value.value) {
			continue
		}
		written = append(written, value)
	}
	if len(removed) == 0 && len(written) == 0 {
		return btree.MutationPlan{}, false, nil
	}
	planner, err := btree.NewMutationPlanner(
		state.SpaceID, state.Generation, state.RootPageID, state.NextPageID, index.runtime,
		index.runtime.FreePageIDs()...,
	)
	if err != nil {
		return btree.MutationPlan{}, false, err
	}
	for _, key := range removed {
		if err := planner.Delete([]byte(key)); err != nil {
			return btree.MutationPlan{}, false, classifyTreeError(err)
		}
	}
	for _, value := range written {
		if err := planner.Upsert(value.key, value.value); err != nil {
			return btree.MutationPlan{}, false, classifyTreeError(err)
		}
	}
	return planner.Plan(), true, nil
}

// storedKeys reads what the Tree currently holds for the owned kinds. It is a
// range scan per kind, not a pass over the Tree: the kinds a replacement owns
// are bounded by that family, and the families it does not own stay unread.
func (index *Index) storedKeys(owned map[uint16]struct{}) (map[string][]byte, error) {
	result := make(map[string][]byte)
	kinds := make([]uint16, 0, len(owned))
	for kind := range owned {
		kinds = append(kinds, kind)
	}
	sort.Slice(kinds, func(left, right int) bool { return kinds[left] < kinds[right] })
	searcher, err := index.searcher()
	if err != nil || searcher == nil {
		return result, err
	}
	for _, kind := range kinds {
		prefix, err := kindPrefix(kind)
		if err != nil {
			return nil, err
		}
		end, err := prefixSuccessor(prefix)
		if err != nil {
			return nil, err
		}
		cursor, err := searcher.NewCursor(prefix, end)
		if err != nil {
			return nil, err
		}
		for {
			batch, err := cursor.Next(256)
			if err != nil {
				return nil, classifyTreeError(err)
			}
			for _, found := range batch.Entries {
				result[string(found.Key)] = bytes.Clone(found.Value)
			}
			if batch.Done {
				break
			}
		}
	}
	return result, nil
}
