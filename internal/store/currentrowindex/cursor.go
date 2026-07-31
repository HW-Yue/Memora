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

func (index *Index) Page(
	tableID, afterRowID string,
	limit uint64,
) (CursorPage, error) {
	if index == nil || index.runtime == nil || limit == 0 || limit > 1000 {
		return CursorPage{}, fmt.Errorf("%w: Page request", ErrInvalid)
	}
	start, err := tablePrefix(tableID)
	if err != nil {
		return CursorPage{}, err
	}
	if afterRowID != "" {
		start, err = encodeKey(tableID, afterRowID)
		if err != nil {
			return CursorPage{}, err
		}
		// A complete current Row key has no suffix. Appending one zero byte is
		// therefore the first possible position strictly after that key.
		start = append(start, 0)
	}
	prefix, err := tablePrefix(tableID)
	if err != nil {
		return CursorPage{}, err
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
		keyTableID, keyRowID, err := decodeKey(entry.Key)
		if err != nil {
			return CursorPage{}, err
		}
		locator, err := decodeLocator(entry.Value)
		if err != nil {
			return CursorPage{}, err
		}
		if keyTableID != tableID ||
			locator.TableID != keyTableID ||
			locator.RowID != keyRowID {
			return CursorPage{}, fmt.Errorf("%w: cursor key/locator scope", ErrCorrupt)
		}
		result.Locators = append(result.Locators, locator)
	}
	if result.HasMore {
		result.NextAfterRowID = result.Locators[len(result.Locators)-1].RowID
	}
	return result, nil
}
