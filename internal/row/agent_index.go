package row

import (
	"context"

	"github.com/HW-Yue/Memora/internal/agentindex"
)

func (service *Service) LookupAgentTerm(
	ctx context.Context,
	databaseID, term string,
	limit int,
) ([]agentindex.Posting, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	values, err := service.agentIndex.Lookup(ctx, databaseID, term, limit)
	return values, stableError(err)
}

func (transaction *Transaction) LookupAgentTerm(
	ctx context.Context,
	databaseID, term string,
	limit int,
) ([]agentindex.Posting, error) {
	values, err := transaction.service.agentIndex.LookupIn(
		ctx, transaction.tx, databaseID, term, limit,
	)
	return values, stableError(err)
}
