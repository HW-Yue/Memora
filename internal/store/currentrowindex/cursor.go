package currentrowindex

import (
	"fmt"

	"github.com/HW-Yue/Memora/internal/store/btree"
)

type CursorPage struct {
	Locators       []Locator
	HasMore        bool
	NextAfterRowID string
}

// Page walks this Tree's current Rows in Row ID order.
//
// The Tree holds exactly one Table, so the scan is bounded by the key prefix
// every entry shares rather than by a Table prefix that had to be matched
// against. That is the whole point of the split: a scan of one Table cannot
// reach another Table's entries because they are not in this Tree.
func (index *Index) Page(
	afterRowID string,
	limit uint64,
) (CursorPage, error) {
	if index == nil || index.runtime == nil || limit == 0 || limit > 1000 {
		return CursorPage{}, fmt.Errorf("%w: Page request", ErrInvalid)
	}
	prefix := indexPrefix()
	start := prefix
	if afterRowID != "" {
		encoded, err := encodeKey(afterRowID)
		if err != nil {
			return CursorPage{}, err
		}
		// A complete current Row key has no suffix. Appending one zero byte is
		// therefore the first possible position strictly after that key.
		start = append(encoded, 0)
	}
	end, err := prefixSuccessor(prefix)
	if err != nil {
		return CursorPage{}, err
	}

	index.mu.RLock()
	defer index.mu.RUnlock()
	state := index.runtime.State()
	if state.RootPageID == 0 {
		return CursorPage{Locators: []Locator{}}, nil
	}
	searcher, err := btree.NewSearcher(state.SpaceID, state.RootPageID, index.runtime)
	if err != nil {
		return CursorPage{}, err
	}
	cursor, err := searcher.NewCursor(start, end)
	if err != nil {
		return CursorPage{}, err
	}
	batch, err := cursor.Next(limit + 1)
	if err != nil {
		return CursorPage{}, classifyTreeError(err)
	}
	count := len(batch.Entries)
	hasMore := uint64(count) > limit
	if hasMore {
		count = int(limit)
	}
	result := CursorPage{
		Locators: make([]Locator, 0, count),
		HasMore:  hasMore,
	}
	for _, entry := range batch.Entries[:count] {
		keyRowID, err := decodeKey(entry.Key)
		if err != nil {
			return CursorPage{}, err
		}
		locator, err := decodeLocator(entry.Value)
		if err != nil {
			return CursorPage{}, err
		}
		if locator.RowID != keyRowID {
			return CursorPage{}, fmt.Errorf("%w: cursor key/locator scope", ErrCorrupt)
		}
		result.Locators = append(result.Locators, locator)
	}
	if result.HasMore {
		result.NextAfterRowID = result.Locators[len(result.Locators)-1].RowID
	}
	return result, nil
}
