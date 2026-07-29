package executor

import (
	"context"
	"fmt"
	"strings"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/router"
)

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
	var created router.Node
	switch statement.Mode {
	case "ROOT":
		databaseName, err := routerString(statement.Database, bound, "Router Database")
		if err != nil {
			return Output{}, err
		}
		database, err := engine.catalog.DescribeDatabase(ctx, databaseName)
		if err != nil {
			return Output{}, normalizeError(err)
		}
		created, err = engine.rows.CreateRouterRoot(ctx, database.ID, purpose)
		if err != nil {
			return Output{}, normalizeError(err)
		}
	case "CHILD":
		parentID, err := routerString(statement.Parent, bound, "Router parent ID")
		if err != nil {
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
			Name: name, Kind: router.Kind(strings.ToLower(kind)), Purpose: purpose,
		})
		if err != nil {
			return Output{}, normalizeError(err)
		}
	default:
		return Output{}, executeError(result.CodeValidation, "CREATE ROUTE mode is invalid")
	}
	return routerNodeMutationOutput(created), nil
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
	if show == nil || show.Route == nil || show.Cursor == nil || show.Limit == nil {
		return Output{}, executeError(result.CodeValidation, "SHOW ROUTES is incomplete")
	}
	cursor, err := routerString(show.Cursor, bound, "Router cursor")
	if err != nil {
		return Output{}, err
	}
	limit, err := routerLimit(show.Limit, bound, "SHOW ROUTES LIMIT")
	if err != nil {
		return Output{}, err
	}
	parent, err := engine.resolveRouterNode(
		ctx, show.RouteMode, show.RouteDB, show.Route, bound,
	)
	if err != nil {
		return Output{}, err
	}
	nodes, next, err := engine.rows.ListRouterChildren(
		ctx, parent.ID, cursor, limit,
	)
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
	limit, err := routerLimit(statement.Limit, bound, "OPEN ROUTE LIMIT")
	if err != nil {
		return Output{}, err
	}
	routeExpression := statement.Route
	if statement.Mode == "PATH" {
		routeExpression = statement.Path
	}
	node, err := engine.resolveRouterNode(
		ctx, statement.Mode, statement.Database, routeExpression, bound,
	)
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
	mode string,
	databaseExpression, routeExpression *ast.Expression,
	bound bindings,
) (router.Node, error) {
	switch mode {
	case "ID":
		routeID, err := routerString(routeExpression, bound, "Router node ID")
		if err != nil {
			return router.Node{}, err
		}
		node, err := engine.rows.GetRouterNode(ctx, routeID)
		return node, normalizeError(err)
	case "PATH":
		databaseName, err := routerString(
			databaseExpression, bound, "Router Database",
		)
		if err != nil {
			return router.Node{}, err
		}
		path, err := routerString(routeExpression, bound, "Router path")
		if err != nil {
			return router.Node{}, err
		}
		database, err := engine.catalog.DescribeDatabase(ctx, databaseName)
		if err != nil {
			return router.Node{}, normalizeError(err)
		}
		node, err := engine.rows.ResolveRouterPath(ctx, database.ID, path)
		return node, normalizeError(err)
	default:
		return router.Node{}, executeError(result.CodeValidation, "Router locator mode is invalid")
	}
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

func routerLimit(
	expression *ast.Expression,
	bound bindings,
	label string,
) (int, error) {
	value, err := historyPositiveInteger(
		expression, catalog.Table{}, bound, label,
	)
	if err != nil {
		return 0, err
	}
	if value > 100 {
		return 0, executeError(result.CodeValidation, label+" must be between 1 and 100")
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
