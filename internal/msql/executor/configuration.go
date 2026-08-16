package executor

import (
	"context"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/nativeconfig"
	"github.com/HW-Yue/Memora/internal/result"
)

type configurationRows interface {
	CurrentQueryBudgets(context.Context) (nativeconfig.Revision, error)
	QueryBudgetHistory(context.Context) ([]nativeconfig.Revision, error)
	UpdateQueryBudgets(context.Context, nativeconfig.QueryBudgets, uint64, string, string) (nativeconfig.Revision, error)
	RestoreQueryBudgets(context.Context, uint64, uint64, string, string) (nativeconfig.Revision, error)
}

// routePolicyRows carries the Database's structural Route policy. It is a
// separate key from query_budgets: read page budgets and semantic branch
// fan-out are different concepts with their own revision chains.
type routePolicyRows interface {
	CurrentRoutePolicy(context.Context) (nativeconfig.PolicyRevision, error)
	RoutePolicyHistory(context.Context) ([]nativeconfig.PolicyRevision, error)
	UpdateRoutePolicy(context.Context, nativeconfig.RoutePolicy, uint64, string, string) (nativeconfig.PolicyRevision, error)
	RestoreRoutePolicy(context.Context, uint64, uint64, string, string) (nativeconfig.PolicyRevision, error)
}

func (engine *Engine) routePolicyManager() (routePolicyRows, error) {
	manager, ok := engine.rows.(routePolicyRows)
	if !ok {
		return nil, executeError(result.CodeUnsupported, "database configuration is not supported by this backend")
	}
	return manager, nil
}

func legacyQueryBudgets() nativeconfig.QueryBudgets {
	return nativeconfig.QueryBudgets{
		RouteChildren: 100, OpenLocators: 1, SelectScan: maxQueryScan,
		SelectRows: maxQueryScan, RouteFrameNodes: 100,
	}
}

func (engine *Engine) queryBudgets(ctx context.Context) (nativeconfig.QueryBudgets, error) {
	manager, ok := engine.rows.(configurationRows)
	if !ok {
		return legacyQueryBudgets(), nil
	}
	value, err := manager.CurrentQueryBudgets(ctx)
	if err != nil {
		return nativeconfig.QueryBudgets{}, normalizeError(err)
	}
	return value.Budgets, nil
}

func (engine *Engine) showConfiguration(
	ctx context.Context, statement *ast.ShowStatement, bound bindings,
) (Output, error) {
	if statement != nil && statement.Key == "ROUTE_POLICY" {
		return engine.showRoutePolicy(ctx, statement, bound)
	}
	manager, ok := engine.rows.(configurationRows)
	if !ok {
		return Output{}, executeError(result.CodeUnsupported, "database configuration is not supported by this backend")
	}
	if statement != nil && statement.Direction == "HISTORY" {
		limit, err := historyPositiveInteger(
			statement.Limit, catalog.Table{}, bound, "SHOW CONFIGURATION HISTORY LIMIT",
		)
		if err != nil {
			return Output{}, err
		}
		if limit > 100 {
			return Output{}, executeError(result.CodeValidation, "SHOW CONFIGURATION HISTORY LIMIT must be between 1 and 100")
		}
		values, err := manager.QueryBudgetHistory(ctx)
		if err != nil {
			return Output{}, normalizeError(err)
		}
		start := len(values) - 1
		stop := max(-1, len(values)-1-int(limit))
		output := Output{Columns: configurationColumns(), Rows: make([]result.Row, 0, min(len(values), int(limit)))}
		for index := start; index > stop; index-- {
			output.Rows = append(output.Rows, configurationRow(values[index]))
		}
		output.Truncated = len(values) > int(limit)
		return output, nil
	}
	value, err := manager.CurrentQueryBudgets(ctx)
	if err != nil {
		return Output{}, normalizeError(err)
	}
	return configurationOutput(value, false), nil
}

func (engine *Engine) alterConfiguration(
	ctx context.Context, statement *ast.ConfigurationStatement, bound bindings, options MutationOptions,
) (Output, error) {
	if statement != nil && statement.Key == "ROUTE_POLICY" {
		return engine.alterRoutePolicy(ctx, statement, bound, options)
	}
	manager, ok := engine.rows.(configurationRows)
	if !ok {
		return Output{}, executeError(result.CodeUnsupported, "database configuration is not supported by this backend")
	}
	if statement == nil || statement.Action != "ALTER" || statement.Key != "QUERY_BUDGETS" {
		return Output{}, executeError(result.CodeValidation, "ALTER CONFIGURATION is incomplete")
	}
	values := []*ast.Expression{
		statement.RouteChildren, statement.OpenLocators, statement.SelectScan,
		statement.SelectRows, statement.RouteFrameNodes,
	}
	labels := []string{
		"route_children", "open_locators", "select_scan", "select_rows", "route_frame_nodes",
	}
	numbers := make([]int, len(values))
	for index, expression := range values {
		value, err := historyPositiveInteger(expression, catalog.Table{}, bound, labels[index])
		if err != nil {
			return Output{}, err
		}
		numbers[index] = int(value)
	}
	updated, err := manager.UpdateQueryBudgets(ctx, nativeconfig.QueryBudgets{
		RouteChildren: numbers[0], OpenLocators: numbers[1], SelectScan: numbers[2],
		SelectRows: numbers[3], RouteFrameNodes: numbers[4],
	}, options.ExpectedRevision, options.Actor, options.Reason)
	if err != nil {
		return Output{}, normalizeError(err)
	}
	return configurationOutput(updated, true), nil
}

func (engine *Engine) restoreConfiguration(
	ctx context.Context, statement *ast.ConfigurationStatement, bound bindings, options MutationOptions,
) (Output, error) {
	if statement != nil && statement.Key == "ROUTE_POLICY" {
		return engine.restoreRoutePolicy(ctx, statement, bound, options)
	}
	manager, ok := engine.rows.(configurationRows)
	if !ok {
		return Output{}, executeError(result.CodeUnsupported, "database configuration is not supported by this backend")
	}
	if statement == nil || statement.Action != "RESTORE" || statement.Key != "QUERY_BUDGETS" {
		return Output{}, executeError(result.CodeValidation, "RESTORE CONFIGURATION is incomplete")
	}
	target, err := historyPositiveInteger(
		statement.TargetRevision, catalog.Table{}, bound, "configuration target revision",
	)
	if err != nil {
		return Output{}, err
	}
	restored, err := manager.RestoreQueryBudgets(
		ctx, target, options.ExpectedRevision, options.Actor, options.Reason,
	)
	if err != nil {
		return Output{}, normalizeError(err)
	}
	return configurationOutput(restored, true), nil
}

func configurationOutput(value nativeconfig.Revision, mutation bool) Output {
	revision := value.Revision
	output := Output{
		Columns: configurationColumns(),
		Rows:    []result.Row{configurationRow(value)},
	}
	if mutation {
		output.AffectedRows = 1
		output.Revision = &revision
	}
	return output
}

func configurationColumns() []result.Column {
	return []result.Column{
		{Name: "config_key", Type: "TEXT"}, {Name: "route_children", Type: "INTEGER"},
		{Name: "open_locators", Type: "INTEGER"}, {Name: "select_scan", Type: "INTEGER"},
		{Name: "select_rows", Type: "INTEGER"}, {Name: "route_frame_nodes", Type: "INTEGER"},
		{Name: "revision", Type: "INTEGER"}, {Name: "actor", Type: "TEXT"},
		{Name: "reason", Type: "TEXT"}, {Name: "restored_revision", Type: "INTEGER", Nullable: true},
		{Name: "recorded_at", Type: "TIMESTAMP"},
	}
}

func configurationRow(value nativeconfig.Revision) result.Row {
	return result.Row{
		"config_key": value.Key, "route_children": value.Budgets.RouteChildren,
		"open_locators": value.Budgets.OpenLocators, "select_scan": value.Budgets.SelectScan,
		"select_rows": value.Budgets.SelectRows, "route_frame_nodes": value.Budgets.RouteFrameNodes,
		"revision": value.Revision, "actor": value.Actor, "reason": value.Reason,
		"restored_revision": value.RestoredRevision, "recorded_at": value.RecordedAt,
	}
}

func (engine *Engine) showRoutePolicy(
	ctx context.Context, statement *ast.ShowStatement, bound bindings,
) (Output, error) {
	manager, err := engine.routePolicyManager()
	if err != nil {
		return Output{}, err
	}
	if statement != nil && statement.Direction == "HISTORY" {
		limit, err := historyPositiveInteger(
			statement.Limit, catalog.Table{}, bound, "SHOW CONFIGURATION ROUTE_POLICY HISTORY LIMIT",
		)
		if err != nil {
			return Output{}, err
		}
		if limit > 100 {
			return Output{}, executeError(result.CodeValidation, "SHOW CONFIGURATION ROUTE_POLICY HISTORY LIMIT must be between 1 and 100")
		}
		values, err := manager.RoutePolicyHistory(ctx)
		if err != nil {
			return Output{}, normalizeError(err)
		}
		start := len(values) - 1
		stop := max(-1, len(values)-1-int(limit))
		output := Output{Columns: routePolicyColumns(), Rows: make([]result.Row, 0, min(len(values), int(limit)))}
		for index := start; index > stop; index-- {
			output.Rows = append(output.Rows, routePolicyRow(values[index]))
		}
		output.Truncated = len(values) > int(limit)
		return output, nil
	}
	value, err := manager.CurrentRoutePolicy(ctx)
	if err != nil {
		return Output{}, normalizeError(err)
	}
	return routePolicyOutput(value, false), nil
}

func (engine *Engine) alterRoutePolicy(
	ctx context.Context, statement *ast.ConfigurationStatement, bound bindings, options MutationOptions,
) (Output, error) {
	manager, err := engine.routePolicyManager()
	if err != nil {
		return Output{}, err
	}
	if statement == nil || statement.Action != "ALTER" || statement.BranchFanout == nil {
		return Output{}, executeError(result.CodeValidation, "ALTER CONFIGURATION ROUTE_POLICY is incomplete")
	}
	fanout, err := historyPositiveInteger(statement.BranchFanout, catalog.Table{}, bound, "branch_fanout")
	if err != nil {
		return Output{}, err
	}
	updated, err := manager.UpdateRoutePolicy(ctx, nativeconfig.RoutePolicy{
		BranchFanout: int(fanout),
	}, options.ExpectedRevision, options.Actor, options.Reason)
	if err != nil {
		return Output{}, normalizeError(err)
	}
	return routePolicyOutput(updated, true), nil
}

func (engine *Engine) restoreRoutePolicy(
	ctx context.Context, statement *ast.ConfigurationStatement, bound bindings, options MutationOptions,
) (Output, error) {
	manager, err := engine.routePolicyManager()
	if err != nil {
		return Output{}, err
	}
	if statement == nil || statement.Action != "RESTORE" {
		return Output{}, executeError(result.CodeValidation, "RESTORE CONFIGURATION ROUTE_POLICY is incomplete")
	}
	target, err := historyPositiveInteger(
		statement.TargetRevision, catalog.Table{}, bound, "configuration target revision",
	)
	if err != nil {
		return Output{}, err
	}
	restored, err := manager.RestoreRoutePolicy(
		ctx, target, options.ExpectedRevision, options.Actor, options.Reason,
	)
	if err != nil {
		return Output{}, normalizeError(err)
	}
	return routePolicyOutput(restored, true), nil
}

func routePolicyOutput(value nativeconfig.PolicyRevision, mutation bool) Output {
	revision := value.Revision
	output := Output{Columns: routePolicyColumns(), Rows: []result.Row{routePolicyRow(value)}}
	if mutation {
		output.AffectedRows = 1
		output.Revision = &revision
	}
	return output
}

func routePolicyColumns() []result.Column {
	return []result.Column{
		{Name: "config_key", Type: "TEXT"}, {Name: "branch_fanout", Type: "INTEGER"},
		{Name: "revision", Type: "INTEGER"}, {Name: "actor", Type: "TEXT"},
		{Name: "reason", Type: "TEXT"}, {Name: "restored_revision", Type: "INTEGER", Nullable: true},
		{Name: "recorded_at", Type: "TIMESTAMP"},
	}
}

func routePolicyRow(value nativeconfig.PolicyRevision) result.Row {
	return result.Row{
		"config_key": value.Key, "branch_fanout": value.Policy.BranchFanout,
		"revision": value.Revision, "actor": value.Actor, "reason": value.Reason,
		"restored_revision": value.RestoredRevision, "recorded_at": value.RecordedAt,
	}
}
