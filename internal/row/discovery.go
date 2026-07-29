package row

import (
	"context"

	"github.com/HW-Yue/Memora/internal/discovery"
	"github.com/HW-Yue/Memora/internal/relation"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/search"
)

func (service *Service) Discover(
	ctx context.Context,
	request discovery.Request,
) (discovery.Result, error) {
	transaction, err := service.BeginTransaction(ctx)
	if err != nil {
		return discovery.Result{}, err
	}
	defer func() { _ = transaction.Rollback() }()
	return transaction.Discover(ctx, request)
}

func (transaction *Transaction) Discover(
	ctx context.Context,
	request discovery.Request,
) (discovery.Result, error) {
	planner, err := discovery.New(
		transactionDiscoverySource{transaction: transaction},
		transaction.service.discoveryOptions,
	)
	if err != nil {
		return discovery.Result{}, stableError(err)
	}
	value, err := planner.Discover(ctx, request)
	return value, stableError(err)
}

type transactionDiscoverySource struct {
	transaction *Transaction
}

func (source transactionDiscoverySource) Root(
	ctx context.Context,
	databaseID string,
) (router.Node, error) {
	return source.transaction.service.routes.ResolvePathIn(
		ctx, source.transaction.tx, databaseID, "/",
	)
}

func (source transactionDiscoverySource) Children(
	ctx context.Context,
	parentID string,
) ([]router.Node, error) {
	nodes, next, err := source.transaction.service.routes.ListChildrenIn(
		ctx, source.transaction.tx, parentID, "", 100,
	)
	if err != nil {
		return nil, err
	}
	if next != "" {
		return nil, rowError(result.CodeInternal, "Router child capacity exceeds discovery page")
	}
	return nodes, nil
}

func (source transactionDiscoverySource) Leaf(
	ctx context.Context,
	leafID string,
	limit int,
) ([]router.Locator, bool, error) {
	return source.transaction.service.routes.ListLeafPageIn(
		ctx, source.transaction.tx, leafID, limit,
	)
}

func (source transactionDiscoverySource) Match(
	ctx context.Context,
	databaseID, query string,
	queryTerms []string,
	limit int,
) (search.Result, error) {
	return source.transaction.Match(ctx, databaseID, "", query, queryTerms, limit)
}

func (source transactionDiscoverySource) Relations(
	ctx context.Context,
	locator search.Locator,
	limit int,
) ([]search.Locator, bool, error) {
	endpoint := relation.Endpoint{
		DatabaseID: locator.DatabaseID, TableID: locator.TableID, RowID: locator.RowID,
	}
	outgoing, err := source.transaction.service.relations.ListOutgoingIn(
		ctx, source.transaction.tx, endpoint,
	)
	if err != nil {
		return nil, false, err
	}
	incoming, err := source.transaction.service.relations.ListIncomingIn(
		ctx, source.transaction.tx, endpoint,
	)
	if err != nil {
		return nil, false, err
	}
	values := make([]search.Locator, 0, min(limit, len(outgoing)+len(incoming)))
	seen := make(map[string]struct{}, len(outgoing)+len(incoming))
	for _, edge := range append(outgoing, incoming...) {
		neighbor := edge.Target
		if edge.Target == endpoint {
			neighbor = edge.Source
		}
		if neighbor.DatabaseID != locator.DatabaseID {
			continue
		}
		key := neighbor.TableID + "\x00" + neighbor.RowID
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		stored, err := getStored(ctx, source.transaction.tx, neighbor.TableID, neighbor.RowID)
		if err != nil {
			return nil, false, err
		}
		if stored.State != StateLive {
			continue
		}
		if len(values) == limit {
			return values, true, nil
		}
		values = append(values, search.Locator{
			DatabaseID: neighbor.DatabaseID, TableID: neighbor.TableID,
			RowID: neighbor.RowID, Revision: stored.Revision,
		})
	}
	return values, false, nil
}
