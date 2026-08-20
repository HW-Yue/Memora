package executor

import (
	"context"
	"fmt"
	"strings"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/security"
)

type tableRouterRows interface {
	CreateTableRouterRoot(context.Context, string, string, string, string) (router.Node, error)
	ListTableRouterRootsPage(context.Context, string, string, string, int) ([]router.Node, router.ReadPage, error)
}

type routeSynopsisRows interface {
	UpdateRouterSynopsis(context.Context, string, string, uint64) (router.Node, error)
}

type routeAliasRows interface {
	UpdateRouterAliases(context.Context, string, []string, uint64) (router.Node, error)
}

func (engine *Engine) createRoute(
	ctx context.Context,
	statement *ast.CreateRouteStatement,
	bound bindings,
	options MutationOptions,
) (Output, error) {
	if err := validateRouterMutationOptions(options, false); err != nil {
		return Output{}, err
	}
	purpose, err := routerString(statement.Purpose, bound, "Router purpose")
	if err != nil {
		return Output{}, err
	}
	synopsis := ""
	if statement.Synopsis != nil {
		synopsis, err = routerString(statement.Synopsis, bound, "Router synopsis")
		if err != nil {
			return Output{}, err
		}
	}
	var created router.Node
	switch statement.Mode {
	case "TABLE_ROOT":
		if statement.Table == nil {
			return Output{}, executeError(result.CodeValidation, "Table Router root requires a Table")
		}
		databaseName, tableName, _, err := engine.bindTable(ctx, *statement.Table)
		if err != nil {
			return Output{}, err
		}
		tableRows, ok := engine.rows.(tableRouterRows)
		if !ok {
			return Output{}, executeError(result.CodeUnsupported, "Table Router is not supported by this backend")
		}
		created, err = tableRows.CreateTableRouterRoot(ctx, databaseName, tableName, purpose, synopsis)
		if err != nil {
			return Output{}, normalizeError(err)
		}
	case "CHILD":
		parentID, err := routerString(statement.Parent, bound, "Router parent ID")
		if err != nil {
			return Output{}, err
		}
		if err := engine.authorizeRouterIDAtLevel(ctx, security.LevelStructural, parentID); err != nil {
			return Output{}, err
		}
		name, err := routerString(statement.Name, bound, "Router name")
		if err != nil {
			return Output{}, err
		}
		kind, err := routerString(statement.NodeKind, bound, "Router kind")
		if err != nil {
			return Output{}, err
		}
		created, err = engine.rows.CreateRouterNode(ctx, parentID, router.NodeDefinition{
			Name: name, Kind: router.Kind(strings.ToLower(kind)), Purpose: purpose, Synopsis: synopsis,
		})
		if err != nil {
			return Output{}, normalizeError(err)
		}
	default:
		return Output{}, executeError(result.CodeValidation, "CREATE ROUTE mode is invalid")
	}
	return routerNodeMutationOutput(created), nil
}

func (engine *Engine) describeRoute(
	ctx context.Context,
	statement *ast.DescribeStatement,
	bound bindings,
) (Output, error) {
	if statement == nil || statement.Route == nil {
		return Output{}, executeError(result.CodeValidation, "DESCRIBE ROUTE is incomplete")
	}
	node, err := engine.resolveRouterNode(ctx, statement.Route, bound)
	if err != nil {
		return Output{}, err
	}
	row := routeResult(node)
	row["synopsis"] = node.Synopsis
	return Output{
		Columns: []result.Column{
			{Name: "route_id", Type: "ID"}, {Name: "database_id", Type: "ID"},
			{Name: "table_id", Type: "ID"}, {Name: "parent_id", Type: "ID", Nullable: true},
			{Name: "path", Type: "TEXT"}, {Name: "name", Type: "TEXT"},
			{Name: "aliases", Type: "TEXT_LIST"}, {Name: "kind", Type: "TEXT"},
			{Name: "purpose", Type: "TEXT"},
			{Name: "synopsis", Type: "TEXT", Nullable: true}, {Name: "revision", Type: "INTEGER"},
		},
		Rows: []result.Row{row},
	}, nil
}

func (engine *Engine) updateRouteSynopsis(
	ctx context.Context,
	statement *ast.UpdateRouteStatement,
	bound bindings,
	options MutationOptions,
) (Output, error) {
	if err := validateRouterMutationOptions(options, true); err != nil {
		return Output{}, err
	}
	routeID, err := routerString(statement.Route, bound, "Router node ID")
	if err != nil {
		return Output{}, err
	}
	if err := engine.authorizeRouterIDAtLevel(ctx, security.LevelStructural, routeID); err != nil {
		return Output{}, err
	}
	synopsis, err := routerString(statement.Synopsis, bound, "Router synopsis")
	if err != nil {
		return Output{}, err
	}
	service, ok := engine.rows.(routeSynopsisRows)
	if !ok {
		return Output{}, executeError(result.CodeUnsupported, "Route synopsis is not supported by this backend")
	}
	updated, err := service.UpdateRouterSynopsis(ctx, routeID, synopsis, options.ExpectedRevision)
	if err != nil {
		return Output{}, normalizeError(err)
	}
	return routerNodeMutationOutput(updated), nil
}

func (engine *Engine) updateRouteAliases(
	ctx context.Context,
	statement *ast.UpdateRouteStatement,
	bound bindings,
	options MutationOptions,
) (Output, error) {
	if err := validateRouterMutationOptions(options, true); err != nil {
		return Output{}, err
	}
	routeID, err := routerString(statement.Route, bound, "Router node ID")
	if err != nil {
		return Output{}, err
	}
	if err := engine.authorizeRouterIDAtLevel(ctx, security.LevelStructural, routeID); err != nil {
		return Output{}, err
	}
	aliases, err := routerAliases(statement.Aliases, bound)
	if err != nil {
		return Output{}, err
	}
	service, ok := engine.rows.(routeAliasRows)
	if !ok {
		return Output{}, executeError(result.CodeUnsupported, "Route aliases are not supported by this backend")
	}
	updated, err := service.UpdateRouterAliases(ctx, routeID, aliases, options.ExpectedRevision)
	if err != nil {
		return Output{}, normalizeError(err)
	}
	return routerNodeMutationOutput(updated), nil
}

func (engine *Engine) renameRoute(
	ctx context.Context,
	statement *ast.RenameRouteStatement,
	bound bindings,
	options MutationOptions,
) (Output, error) {
	if err := validateRouterMutationOptions(options, true); err != nil {
		return Output{}, err
	}
	routeID, err := routerString(statement.Route, bound, "Router node ID")
	if err != nil {
		return Output{}, err
	}
	if err := engine.authorizeRouterIDAtLevel(ctx, security.LevelStructural, routeID); err != nil {
		return Output{}, err
	}
	name, err := routerString(statement.Name, bound, "Router name")
	if err != nil {
		return Output{}, err
	}
	renamed, err := engine.rows.RenameRouterNode(
		ctx, routeID, name, options.ExpectedRevision,
	)
	if err != nil {
		return Output{}, normalizeError(err)
	}
	return routerNodeMutationOutput(renamed), nil
}

func (engine *Engine) deleteRoute(
	ctx context.Context,
	statement *ast.DeleteRouteStatement,
	bound bindings,
	options MutationOptions,
) (Output, error) {
	if err := validateRouterMutationOptions(options, true); err != nil {
		return Output{}, err
	}
	routeID, err := routerString(statement.Route, bound, "Router node ID")
	if err != nil {
		return Output{}, err
	}
	if err := engine.authorizeRouterIDAtLevel(ctx, security.LevelStructural, routeID); err != nil {
		return Output{}, err
	}
	revision, err := engine.rows.DeleteRouterNode(ctx, routeID, options.ExpectedRevision)
	if err != nil {
		return Output{}, normalizeError(err)
	}
	return routerRevisionMutationOutput(revision), nil
}

// archive runs ARCHIVE and UNARCHIVE. Archiving never destroys anything, so
// UNARCHIVE is the exact inverse and both sides share this dispatch.
func (engine *Engine) archive(
	ctx context.Context,
	statement *ast.ArchiveStatement,
	bound bindings,
	options MutationOptions,
) (Output, error) {
	switch statement.Object {
	case "ROUTE":
		return engine.archiveRoute(ctx, statement, bound, options)
	default:
		return Output{}, executeError(result.CodeValidation, "ARCHIVE object kind is unsupported")
	}
}

func (engine *Engine) archiveRoute(
	ctx context.Context,
	statement *ast.ArchiveStatement,
	bound bindings,
	options MutationOptions,
) (Output, error) {
	if err := validateRouterMutationOptions(options, true); err != nil {
		return Output{}, err
	}
	routeID, err := routerString(statement.Target, bound, "Router node ID")
	if err != nil {
		return Output{}, err
	}
	if !statement.Restore {
		if _, err := routerString(statement.Reason, bound, "archive reason"); err != nil {
			return Output{}, err
		}
	}
	// UNARCHIVE has to authorize against a node the live surface hides, so the
	// restore path resolves its Database through the archive-aware read.
	lookup := engine.rows.GetRouterNode
	mutate := engine.rows.DeleteRouterNode
	if statement.Restore {
		lookup, mutate = engine.rows.GetArchivedRouterNode, engine.rows.RestoreRouterNode
	}
	if err := engine.authorizeRouterNodeAtLevel(ctx, security.LevelStructural, routeID, lookup); err != nil {
		return Output{}, err
	}
	revision, err := mutate(ctx, routeID, options.ExpectedRevision)
	if err != nil {
		return Output{}, normalizeError(err)
	}
	return routerRevisionMutationOutput(revision), nil
}

func (engine *Engine) showRoutes(
	ctx context.Context,
	statement ast.Statement,
	bound bindings,
) (Output, error) {
	show := statement.Show
	if show == nil || show.Limit == nil || (show.RouteMode != "TABLE_ROOT" && show.Route == nil) {
		return Output{}, executeError(result.CodeValidation, "SHOW ROUTES is incomplete")
	}
	cursor := ""
	if show.Cursor != nil {
		var err error
		cursor, err = routerString(show.Cursor, bound, "Router cursor")
		if err != nil {
			return Output{}, err
		}
	}
	budgets, err := engine.queryBudgets(ctx)
	if err != nil {
		return Output{}, err
	}
	limit, err := engine.routerLimit(ctx, show.Limit, bound, "SHOW ROUTES LIMIT", budgets.RouteChildren)
	if err != nil {
		return Output{}, err
	}
	var nodes []router.Node
	var page router.ReadPage
	expectedDatabaseID, expectedTableID, expectedParentID := "", "", ""
	if show.RouteMode == "TABLE_ROOT" {
		if show.Table == nil {
			return Output{}, executeError(result.CodeValidation, "SHOW ROUTES requires a Table")
		}
		_, _, table, bindErr := engine.bindTable(ctx, *show.Table)
		if bindErr != nil {
			return Output{}, bindErr
		}
		tableRows, ok := engine.rows.(tableRouterRows)
		if !ok {
			return Output{}, executeError(result.CodeUnsupported, "Table Router is not supported by this backend")
		}
		expectedDatabaseID, expectedTableID = table.DatabaseID, table.ID
		nodes, page, err = tableRows.ListTableRouterRootsPage(ctx, table.DatabaseID, table.ID, cursor, limit)
	} else {
		parent, resolveErr := engine.resolveRouterNode(ctx, show.Route, bound)
		if resolveErr != nil {
			return Output{}, resolveErr
		}
		if parent.Kind == router.KindLeaf {
			return Output{}, executeError(result.CodeConstraint, "SHOW ROUTES UNDER requires a root or branch; use OPEN ROUTE for a leaf")
		}
		expectedDatabaseID, expectedTableID, expectedParentID = parent.DatabaseID, parent.TableID, parent.ID
		nodes, page, err = engine.rows.ListRouterChildrenPage(ctx, parent.ID, cursor, limit)
	}
	if err != nil {
		return Output{}, normalizeError(err)
	}
	if page.Snapshot == "" {
		return Output{}, executeError(result.CodeInternal, "Route child page has no snapshot")
	}
	if err := validateRouteChildren(nodes, expectedDatabaseID, expectedTableID, expectedParentID); err != nil {
		return Output{}, err
	}
	output := Output{
		Columns: []result.Column{
			{Name: "route_id", Type: "ID"},
			{Name: "database_id", Type: "ID"},
			{Name: "table_id", Type: "ID"},
			{Name: "parent_id", Type: "ID", Nullable: true},
			{Name: "path", Type: "TEXT"},
			{Name: "name", Type: "TEXT"},
			{Name: "aliases", Type: "TEXT_LIST"},
			{Name: "kind", Type: "TEXT"},
			{Name: "purpose", Type: "TEXT"},
			{Name: "revision", Type: "INTEGER"},
		},
		Rows:      make([]result.Row, 0, len(nodes)),
		Truncated: page.NextCursor != "", NextCursor: page.NextCursor,
		Page: &result.ListPage{
			Version: result.ListPageVersion, Limit: uint64(limit), Cursor: cursor,
			Snapshot: page.Snapshot, Truncated: page.NextCursor != "", NextCursor: page.NextCursor,
		},
	}
	for _, node := range nodes {
		output.Rows = append(output.Rows, routeResult(node))
	}
	return output, nil
}

func (engine *Engine) openRoute(
	ctx context.Context,
	statement *ast.OpenRouteStatement,
	bound bindings,
) (Output, error) {
	cursor := ""
	if statement.Cursor != nil {
		var err error
		cursor, err = routerString(statement.Cursor, bound, "Router cursor")
		if err != nil {
			return Output{}, err
		}
	}
	budgets, err := engine.queryBudgets(ctx)
	if err != nil {
		return Output{}, err
	}
	limit, err := engine.routerLimit(ctx, statement.Limit, bound, "OPEN ROUTE LIMIT", budgets.OpenLocators)
	if err != nil {
		return Output{}, err
	}
	node, err := engine.resolveRouterNode(ctx, statement.Route, bound)
	if err != nil {
		return Output{}, err
	}
	if node.Kind != router.KindLeaf {
		return Output{}, executeError(result.CodeConstraint, "OPEN ROUTE requires a leaf")
	}
	locators, page, err := engine.rows.ListRouterLeafPage(ctx, node.ID, cursor, limit)
	if err != nil {
		return Output{}, normalizeError(err)
	}
	if page.Snapshot == "" {
		return Output{}, executeError(result.CodeInternal, "Route locator page has no snapshot")
	}
	if err := validateRouteLocators(locators, node); err != nil {
		return Output{}, err
	}
	output := Output{
		Columns: []result.Column{
			{Name: "database_id", Type: "ID"},
			{Name: "table_id", Type: "ID"},
			{Name: "row_id", Type: "ID"},
			{Name: "revision", Type: "INTEGER"},
		},
		Rows:      make([]result.Row, 0, len(locators)),
		Truncated: page.NextCursor != "", NextCursor: page.NextCursor,
		Page: &result.ListPage{
			Version: result.ListPageVersion, Limit: uint64(limit), Cursor: cursor,
			Snapshot: page.Snapshot, Truncated: page.NextCursor != "", NextCursor: page.NextCursor,
		},
	}
	for _, locator := range locators {
		if err := engine.authorizeDatabaseReference(ctx, locator.DatabaseID); err != nil {
			return Output{}, err
		}
		output.Rows = append(output.Rows, result.Row{
			"database_id": locator.DatabaseID,
			"table_id":    locator.TableID,
			"row_id":      locator.RowID,
			"revision":    locator.Revision,
		})
	}
	return output, nil
}

func (engine *Engine) resolveRouterNode(
	ctx context.Context,
	routeExpression *ast.Expression,
	bound bindings,
) (router.Node, error) {
	routeID, err := routerString(routeExpression, bound, "Router node ID")
	if err != nil {
		return router.Node{}, err
	}
	node, err := engine.rows.GetRouterNode(ctx, routeID)
	if err != nil {
		return router.Node{}, normalizeError(err)
	}
	if err := engine.authorizeDatabaseReference(ctx, node.DatabaseID); err != nil {
		return router.Node{}, err
	}
	return node, nil
}

func (engine *Engine) authorizeRouterID(ctx context.Context, routeID string) error {
	return engine.authorizeRouterIDAtLevel(ctx, security.LevelRead, routeID)
}

func (engine *Engine) authorizeRouterIDAtLevel(ctx context.Context, level security.RiskLevel, routeID string) error {
	return engine.authorizeRouterNodeAtLevel(ctx, level, routeID, engine.rows.GetRouterNode)
}

func (engine *Engine) authorizeRouterNodeAtLevel(
	ctx context.Context,
	level security.RiskLevel,
	routeID string,
	lookup func(context.Context, string) (router.Node, error),
) error {
	_, authorized := security.AuthorizationFrom(ctx)
	if !authorized && level == security.LevelRead {
		return nil
	}
	node, err := lookup(ctx, routeID)
	if err != nil {
		return normalizeError(err)
	}
	if level != security.LevelRead {
		if err := engine.requireWritableDatabaseReference(ctx, node.DatabaseID); err != nil {
			return err
		}
	}
	if !authorized {
		return nil
	}
	return engine.authorizeDatabaseReferenceAtLevel(ctx, level, node.DatabaseID)
}

func routeResult(node router.Node) result.Row {
	return result.Row{
		"route_id": node.ID, "database_id": node.DatabaseID,
		"table_id": node.TableID, "parent_id": node.ParentID,
		"path": node.Path, "name": node.Name, "kind": string(node.Kind),
		"aliases": append([]string{}, node.Aliases...),
		"purpose": node.Purpose, "revision": node.Revision,
	}
}

func routerAliases(expression *ast.Expression, bound bindings) ([]string, error) {
	value, err := evaluate(expression, catalog.Table{}, nil, bound)
	if err != nil {
		return nil, err
	}
	switch aliases := value.(type) {
	case []string:
		return append([]string{}, aliases...), nil
	case []any:
		resultAliases := make([]string, len(aliases))
		for index, alias := range aliases {
			text, ok := alias.(string)
			if !ok {
				return nil, executeError(result.CodeValidation, "Route aliases must be an array of TEXT")
			}
			resultAliases[index] = text
		}
		return resultAliases, nil
	default:
		return nil, executeError(result.CodeValidation, "Route aliases must be an array of TEXT")
	}
}

func validateRouteChildren(nodes []router.Node, databaseID, tableID, parentID string) error {
	if parentID == "" && len(nodes) > 0 {
		parentID = nodes[0].ParentID
	}
	seen := make(map[string]struct{}, len(nodes))
	for _, node := range nodes {
		if node.Version != router.Version || node.ID == "" || node.Revision == 0 || node.Deleted ||
			node.DatabaseID != databaseID || node.TableID != tableID || node.ParentID == "" ||
			node.ParentID != parentID || (node.Kind != router.KindBranch && node.Kind != router.KindLeaf) {
			return executeError(result.CodeInternal, "Route child page violates its logical scope")
		}
		if _, duplicate := seen[node.ID]; duplicate {
			return executeError(result.CodeInternal, "Route child page contains a duplicate node")
		}
		seen[node.ID] = struct{}{}
	}
	return nil
}

func validateRouteLocators(locators []router.Locator, leaf router.Node) error {
	seen := make(map[string]struct{}, len(locators))
	for _, locator := range locators {
		if locator.DatabaseID != leaf.DatabaseID || locator.TableID != leaf.TableID ||
			locator.RowID == "" || locator.Revision == 0 {
			return executeError(result.CodeInternal, "Route locator page violates its leaf scope")
		}
		if _, duplicate := seen[locator.RowID]; duplicate {
			return executeError(result.CodeInternal, "Route locator page contains a duplicate RowID")
		}
		seen[locator.RowID] = struct{}{}
	}
	return nil
}

func routerString(
	expression *ast.Expression,
	bound bindings,
	label string,
) (string, error) {
	return relationshipString(expression, catalog.Table{}, bound, label)
}

func (engine *Engine) routerLimit(
	_ context.Context,
	expression *ast.Expression,
	bound bindings,
	label string,
	maximum int,
) (int, error) {
	value, err := historyPositiveInteger(
		expression, catalog.Table{}, bound, label,
	)
	if err != nil {
		return 0, err
	}
	if value > uint64(maximum) {
		return 0, executeError(result.CodeValidation, fmt.Sprintf("%s must be between 1 and configured maximum %d", label, maximum))
	}
	return int(value), nil
}

func validateRouterMutationOptions(options MutationOptions, revision bool) error {
	if options.MaxAffectedRows < 1 || options.MaxAffectedRows > maxQueryScan {
		return executeError(
			result.CodeValidation,
			fmt.Sprintf("max_affected_rows must be between 1 and %d", maxQueryScan),
		)
	}
	if revision && options.ExpectedRevision == 0 {
		return executeError(result.CodeValidation, "Router mutation requires expected revision")
	}
	return nil
}

func routerNodeMutationOutput(node router.Node) Output {
	revision := node.Revision
	return Output{
		Columns: []result.Column{
			{Name: "route_id", Type: "ID"},
			{Name: "database_id", Type: "ID"},
			{Name: "table_id", Type: "ID"},
			{Name: "parent_id", Type: "ID", Nullable: true},
			{Name: "path", Type: "TEXT"},
			{Name: "name", Type: "TEXT"},
			{Name: "aliases", Type: "TEXT_LIST"},
			{Name: "kind", Type: "TEXT"},
			{Name: "purpose", Type: "TEXT"},
			{Name: "revision", Type: "INTEGER"},
		},
		Rows:         []result.Row{routeResult(node)},
		AffectedRows: 1,
		Revision:     &revision,
	}
}

func routerRevisionMutationOutput(revision uint64) Output {
	return Output{
		Columns: []result.Column{}, Rows: []result.Row{},
		AffectedRows: 1, Revision: &revision,
	}
}
