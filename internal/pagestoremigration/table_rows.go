package pagestoremigration

import (
	"fmt"

	"github.com/HW-Yue/Memora/internal/store/currentrowindex"
)

// tableCurrentRows answers current Row reads by routing to the Tree of the
// Table they name.
//
// It is the seam that let the split happen without touching the reader: the
// reader has always passed a Table ID with every lookup, because the key used
// to carry one. Now the Table ID picks the Tree instead of prefixing the key.
type tableCurrentRows struct {
	generation *Generation
}

func (rows tableCurrentRows) index(tableID string) (*currentrowindex.Index, error) {
	if rows.generation == nil {
		return nil, fmt.Errorf("%w: generation", ErrInvalid)
	}
	index := rows.generation.CurrentRowsFor(tableID)
	if index == nil {
		// A Table with no Tree has no Rows. That is the state between a Catalog
		// publication and the Tree creation that follows it, and after a
		// Database is opened whose Catalog names a Table nothing ever wrote to.
		return nil, nil
	}
	return index, nil
}

func (rows tableCurrentRows) Lookup(tableID, rowID string) (currentrowindex.Locator, error) {
	index, err := rows.index(tableID)
	if err != nil {
		return currentrowindex.Locator{}, err
	}
	if index == nil {
		return currentrowindex.Locator{}, currentrowindex.ErrNotFound
	}
	return index.Lookup(rowID)
}

func (rows tableCurrentRows) Page(
	tableID, afterRowID string, limit uint64,
) (currentrowindex.CursorPage, error) {
	index, err := rows.index(tableID)
	if err != nil {
		return currentrowindex.CursorPage{}, err
	}
	if index == nil {
		return currentrowindex.CursorPage{Locators: []currentrowindex.Locator{}}, nil
	}
	return index.Page(afterRowID, limit)
}
