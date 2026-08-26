package pagestoremigration

import (
	"context"

	"github.com/HW-Yue/Memora/internal/lexicallocation"
)

// SearchLexicalLocations holds the Authority read lock for the complete
// multi-term read so a generation replacement cannot mix two snapshots.
//
// The Fulltext index is derived and no longer written by the transaction that
// changed the data, so a read brings it level first. That is what keeps the
// decoupling invisible: the writer stops carrying the index, and the reader
// still never sees a stale answer.
func (authority *Authority) SearchLexicalLocations(
	ctx context.Context, request lexicallocation.Request,
) (lexicallocation.Page, error) {
	if err := authority.EnsureFulltextCurrent(ctx); err != nil {
		return lexicallocation.Page{}, err
	}
	if err := authority.lockRead(ctx); err != nil {
		return lexicallocation.Page{}, err
	}
	defer authority.mu.RUnlock()
	return lexicallocation.Search(ctx, authority.generation.fulltext, request)
}

// EnsureFulltextCurrent brings the derived Fulltext index level with the
// committed change log if it has fallen behind.
//
// The cheap check runs under the read lock so an already-current index costs a
// cursor read; only an index that is actually behind takes the write lock. Two
// readers racing here is harmless — catch-up re-reads the cursor under the
// write lock, and applying a round twice is a replay.
func (authority *Authority) EnsureFulltextCurrent(ctx context.Context) error {
	behind, err := authority.FulltextBehind(ctx)
	if err != nil || !behind {
		return err
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if authority.closed {
		return nil
	}
	return authority.catchUpFulltextLocked(ctx)
}
