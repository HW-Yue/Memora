package row

import (
	"context"

	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/store"
)

func (transaction *Transaction) CreateRouterRoot(
	ctx context.Context,
	databaseID, purpose string,
) (router.Node, error) {
	node, err := transaction.service.routes.CreateRootIn(ctx, transaction.tx, databaseID, purpose)
	return node, stableError(err)
}

func (transaction *Transaction) CreateRouterNode(
	ctx context.Context,
	parentID string,
	definition router.NodeDefinition,
) (router.Node, error) {
	node, err := transaction.service.routes.CreateNodeIn(ctx, transaction.tx, parentID, definition)
	return node, stableError(err)
}

func (service *Service) RouterMemberships(
	ctx context.Context,
	value Row,
) ([]router.Membership, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	tx, err := service.store.Begin(ctx, store.ReadOnly)
	if err != nil {
		return nil, stableError(err)
	}
	defer func() { _ = tx.Rollback() }()
	memberships, err := service.routes.MembershipsForRowIn(
		ctx, tx, value.DatabaseID, value.TableID, value.ID,
	)
	return memberships, stableError(err)
}

func (transaction *Transaction) RouterMemberships(
	ctx context.Context,
	value Row,
) ([]router.Membership, error) {
	memberships, err := transaction.service.routes.MembershipsForRowIn(
		ctx, transaction.tx, value.DatabaseID, value.TableID, value.ID,
	)
	return memberships, stableError(err)
}

func (transaction *Transaction) replaceRouterMemberships(
	ctx context.Context,
	stored storedRow,
	leafIDs []string,
) error {
	if leafIDs == nil {
		return nil
	}
	_, err := transaction.service.routes.ReplaceMembershipsIn(
		ctx, transaction.tx, routerLocator(stored), leafIDs,
	)
	return stableError(err)
}

func (transaction *Transaction) invalidateRouterMemberships(
	ctx context.Context,
	stored storedRow,
) error {
	_, err := transaction.service.routes.ReplaceMembershipsIn(
		ctx, transaction.tx, routerLocator(stored), []string{},
	)
	return stableError(err)
}

func routerLocator(stored storedRow) router.Locator {
	return router.Locator{
		DatabaseID: stored.DatabaseID,
		TableID:    stored.TableID,
		RowID:      stored.ID,
		Revision:   stored.Revision,
	}
}
