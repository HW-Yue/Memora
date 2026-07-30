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
	ListTableRouterRoots(context.Context, string, string, string, int) ([]router.Node, string, error)
}

type routeSynopsisRows interface {
	UpdateRouterSynopsis(context.Context, string, string, uint64) (router.Node, error)
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
		if err := engine.authorizeRouterID(ctx, parentID); err != nil {
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
			{Name: "route_id", Type: "ID"}, {Name: "parent_id", Type: "ID", Nullable: true},
			{Name: "path", Type: "TEXT"}, {Name: "name", Type: "TEXT"},
			{Name: "kind", Type: "TEXT"}, {Name: "purpose", Type: "TEXT"},
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
	if err := engine.authorizeRouterID(ctx, routeID); err != nil {
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
	if err := engine.authorizeRouterID(ctx, routeID); err != nil {
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
	if err := engine.authorizeRouterID(ctx, routeID); err != nil {
		return Output{}, err
	}
	revision, err := engine.rows.DeleteRouterNode(ctx, routeID, options.ExpectedRevision)
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
	var next string
	if show.RouteMode == "TABLE_ROOT" {
		if show.Table == nil {
			return Output{}, executeError(result.CodeValidation, "SHOW ROUTES requires a Table")
		}
		_, _, table, err := engine.bindTable(ctx, *show.Table)
		if err != nil {
			return Output{}, err
		}
		tableRows, ok := engine.rows.(tableRouterRows)
		if !ok {
			return Output{}, executeError(result.CodeUnsupported, "Table Router is not supported by this backend")
		}
		nodes, next, err = tableRows.ListTableRouterRoots(ctx, table.DatabaseID, table.ID, cursor, limit)
	} else {
		parent, err := engine.resolveRouterNode(ctx, show.Route, bound)
		if err != nil {
			return Output{}, err
		}
		nodes, next, err = engine.rows.ListRouterChildren(ctx, parent.ID, cursor, limit)
	}
	if err != nil {
		return Output{}, normalizeError(err)
	}
	output := Output{
		Columns: []result.Column{
			{Name: "route_id", Type: "ID"},
			{Name: "parent_id", Type: "ID", Nullable: true},
			{Name: "path", Type: "TEXT"},
			{Name: "name", Type: "TEXT"},
			{Name: "kind", Type: "TEXT"},
			{Name: "purpose", Type: "TEXT"},
			{Name: "revision", Type: "INTEGER"},
		},
		Rows:      make([]result.Row, 0, len(nodes)),
		Truncated: next != "", NextCursor: next,
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
	locators, truncated, err := engine.rows.ListRouterLeaf(ctx, node.ID, limit)
	if err != nil {
		return Output{}, normalizeError(err)
	}
	output := Output{
		Columns: []result.Column{
			{Name: "database_id", Type: "ID"},
			{Name: "table_id", Type: "ID"},
			{Name: "row_id", Type: "ID"},
			{Name: "revision", Type: "INTEGER"},
		},
		Rows: make([]result.Row, 0, len(locators)), Truncated: truncated,
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
	if _, present := security.AuthorizationFrom(ctx); !present {
		return nil
	}
	node, err := engine.rows.GetRouterNode(ctx, routeID)
	if err != nil {
		return normalizeError(err)
	}
	return engine.authorizeDatabaseReference(ctx, node.DatabaseID)
}

func routeResult(node router.Node) result.Row {
	return result.Row{
		"route_id": node.ID, "parent_id": node.ParentID,
		"path": node.Path, "name": node.Name, "kind": string(node.Kind),
		"purpose": node.Purpose, "revision": node.Revision,
	}
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
			{Name: "parent_id", Type: "ID", Nullable: true},
			{Name: "path", Type: "TEXT"},
			{Name: "name", Type: "TEXT"},
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
