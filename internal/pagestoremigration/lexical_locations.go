package pagestoremigration

import (
	"context"

	"github.com/HW-Yue/Memora/internal/lexicallocation"
)

// SearchLexicalLocations holds the Authority read lock for the complete
// multi-term read so a generation replacement cannot mix two snapshots.
func (authority *Authority) SearchLexicalLocations(
	ctx context.Context, request lexicallocation.Request,
) (lexicallocation.Page, error) {
	if err := authority.lockRead(ctx); err != nil {
		return lexicallocation.Page{}, err
	}
	defer authority.mu.RUnlock()
	return lexicallocation.Search(ctx, authority.generation.fulltext, request)
}
