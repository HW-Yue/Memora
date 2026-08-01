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

func (service *Service) CreateRouterRoot(
	ctx context.Context,
	databaseID, purpose string,
) (router.Node, error) {
	transaction, err := service.BeginTransaction(ctx)
	if err != nil {
		return router.Node{}, err
	}
	defer func() { _ = transaction.Rollback() }()
	node, err := transaction.CreateRouterRoot(ctx, databaseID, purpose)
	if err != nil {
		return router.Node{}, err
	}
	if err := transaction.Commit(); err != nil {
		return router.Node{}, err
	}
	return node, nil
}

func (service *Service) CreateRouterNode(
	ctx context.Context,
	parentID string,
	definition router.NodeDefinition,
) (router.Node, error) {
	transaction, err := service.BeginTransaction(ctx)
	if err != nil {
		return router.Node{}, err
	}
	defer func() { _ = transaction.Rollback() }()
	node, err := transaction.CreateRouterNode(ctx, parentID, definition)
	if err != nil {
		return router.Node{}, err
	}
	if err := transaction.Commit(); err != nil {
		return router.Node{}, err
	}
	return node, nil
}

func (service *Service) RenameRouterNode(
	ctx context.Context,
	nodeID, newName string,
	expectedRevision uint64,
) (router.Node, error) {
	transaction, err := service.BeginTransaction(ctx)
	if err != nil {
		return router.Node{}, err
	}
	defer func() { _ = transaction.Rollback() }()
	node, err := transaction.RenameRouterNode(ctx, nodeID, newName, expectedRevision)
	if err != nil {
		return router.Node{}, err
	}
	if err := transaction.Commit(); err != nil {
		return router.Node{}, err
	}
	return node, nil
}

func (transaction *Transaction) RenameRouterNode(
	ctx context.Context,
	nodeID, newName string,
	expectedRevision uint64,
) (router.Node, error) {
	node, err := transaction.service.routes.RenameIn(
		ctx, transaction.tx, nodeID, newName, expectedRevision,
	)
	return node, stableError(err)
}

func (service *Service) DeleteRouterNode(
	ctx context.Context,
	nodeID string,
	expectedRevision uint64,
) (uint64, error) {
	transaction, err := service.BeginTransaction(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = transaction.Rollback() }()
	revision, err := transaction.DeleteRouterNode(ctx, nodeID, expectedRevision)
	if err != nil {
		return 0, err
	}
	if err := transaction.Commit(); err != nil {
		return 0, err
	}
	return revision, nil
}

func (transaction *Transaction) DeleteRouterNode(
	ctx context.Context,
	nodeID string,
	expectedRevision uint64,
) (uint64, error) {
	if err := transaction.service.routes.DeleteNodeIn(
		ctx, transaction.tx, nodeID, expectedRevision,
	); err != nil {
		return 0, stableError(err)
	}
	return expectedRevision + 1, nil
}

func (service *Service) GetRouterNode(
	ctx context.Context,
	nodeID string,
) (router.Node, error) {
	node, err := service.routes.Get(ctx, nodeID)
	return node, stableError(err)
}

func (transaction *Transaction) GetRouterNode(
	ctx context.Context,
	nodeID string,
) (router.Node, error) {
	node, err := transaction.service.routes.GetIn(ctx, transaction.tx, nodeID)
	return node, stableError(err)
}

func (service *Service) ResolveRouterPath(
	ctx context.Context,
	databaseID, path string,
) (router.Node, error) {
	node, err := service.routes.ResolvePath(ctx, databaseID, path)
	return node, stableError(err)
}

func (transaction *Transaction) ResolveRouterPath(
	ctx context.Context,
	databaseID, path string,
) (router.Node, error) {
	node, err := transaction.service.routes.ResolvePathIn(
		ctx, transaction.tx, databaseID, path,
	)
	return node, stableError(err)
}

func (service *Service) ListRouterChildren(
	ctx context.Context,
	parentID, cursor string,
	limit int,
) ([]router.Node, string, error) {
	nodes, next, err := service.routes.ListChildren(ctx, parentID, cursor, limit)
	return nodes, next, stableError(err)
}

func (service *Service) ListRouterChildrenPage(
	ctx context.Context,
	parentID, cursor string,
	limit int,
) ([]router.Node, router.ReadPage, error) {
	nodes, page, err := service.routes.ListChildrenPage(ctx, parentID, cursor, limit)
	return nodes, page, stableError(err)
}

func (transaction *Transaction) ListRouterChildren(
	ctx context.Context,
	parentID, cursor string,
	limit int,
) ([]router.Node, string, error) {
	nodes, next, err := transaction.service.routes.ListChildrenIn(
		ctx, transaction.tx, parentID, cursor, limit,
	)
	return nodes, next, stableError(err)
}

func (transaction *Transaction) ListRouterChildrenPage(
	ctx context.Context,
	parentID, cursor string,
	limit int,
) ([]router.Node, router.ReadPage, error) {
	nodes, page, err := transaction.service.routes.ListChildrenPageIn(
		ctx, transaction.tx, parentID, cursor, limit,
	)
	return nodes, page, stableError(err)
}

func (service *Service) ListRouterNodes(ctx context.Context) ([]router.Node, error) {
	nodes, err := service.routes.ListNodes(ctx)
	return nodes, stableError(err)
}

func (transaction *Transaction) ListRouterNodes(ctx context.Context) ([]router.Node, error) {
	nodes, err := transaction.service.routes.ListNodesIn(ctx, transaction.tx)
	return nodes, stableError(err)
}

func (service *Service) ListRouterLeaf(
	ctx context.Context,
	leafID string,
	limit int,
) ([]router.Locator, bool, error) {
	locators, truncated, err := service.routes.ListLeafPage(ctx, leafID, limit)
	return locators, truncated, stableError(err)
}

func (service *Service) ListRouterLeafPage(
	ctx context.Context,
	leafID, cursor string,
	limit int,
) ([]router.Locator, router.ReadPage, error) {
	locators, page, err := service.routes.ListLeafCursorPage(ctx, leafID, cursor, limit)
	return locators, page, stableError(err)
}

func (transaction *Transaction) ListRouterLeaf(
	ctx context.Context,
	leafID string,
	limit int,
) ([]router.Locator, bool, error) {
	locators, truncated, err := transaction.service.routes.ListLeafPageIn(
		ctx, transaction.tx, leafID, limit,
	)
	return locators, truncated, stableError(err)
}

func (transaction *Transaction) ListRouterLeafPage(
	ctx context.Context,
	leafID, cursor string,
	limit int,
) ([]router.Locator, router.ReadPage, error) {
	locators, page, err := transaction.service.routes.ListLeafCursorPageIn(
		ctx, transaction.tx, leafID, cursor, limit,
	)
	return locators, page, stableError(err)
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
