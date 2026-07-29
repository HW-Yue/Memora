package row

import (
	"context"

	"github.com/HW-Yue/Memora/internal/mechanicalindex"
	"github.com/HW-Yue/Memora/internal/search"
)

func (service *Service) Match(
	ctx context.Context,
	databaseID, tableID, query string,
	agentTerms []string,
	limit int,
) (search.Result, error) {
	transaction, err := service.BeginTransaction(ctx)
	if err != nil {
		return search.Result{}, err
	}
	defer func() { _ = transaction.Rollback() }()
	return transaction.Match(ctx, databaseID, tableID, query, agentTerms, limit)
}

func (transaction *Transaction) Match(
	ctx context.Context,
	databaseID, tableID, query string,
	agentTerms []string,
	limit int,
) (search.Result, error) {
	planner, err := search.New(transactionSearchSource{transaction: transaction}, transaction.service.searchOptions)
	if err != nil {
		return search.Result{}, stableError(err)
	}
	resultValue, err := planner.Match(ctx, search.Request{
		DatabaseID: databaseID, TableID: tableID,
		AgentTerms: agentTerms, MechanicalTerms: mechanicalindex.QueryTerms(query), Limit: limit,
	})
	return resultValue, stableError(err)
}

type transactionSearchSource struct {
	transaction *Transaction
}

func (source transactionSearchSource) LookupAgent(
	ctx context.Context,
	databaseID, term string,
	limit int,
) ([]search.Locator, error) {
	postings, err := source.transaction.LookupAgentTerm(ctx, databaseID, term, limit)
	if err != nil {
		return nil, err
	}
	values := make([]search.Locator, 0, len(postings))
	for _, posting := range postings {
		values = append(values, search.Locator(posting.Locator))
	}
	return values, nil
}

func (source transactionSearchSource) LookupMechanical(
	ctx context.Context,
	databaseID, term string,
	limit int,
) ([]search.Locator, error) {
	postings, err := source.transaction.LookupMechanicalTerm(ctx, databaseID, term, limit)
	if err != nil {
		return nil, err
	}
	values := make([]search.Locator, 0, len(postings))
	for _, posting := range postings {
		values = append(values, search.Locator(posting.Locator))
	}
	return values, nil
}
