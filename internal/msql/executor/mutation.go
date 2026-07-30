package executor

import (
	"context"
	"fmt"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/result"
	datarow "github.com/HW-Yue/Memora/internal/row"
)

func (engine *Engine) Execute(ctx context.Context, statement ast.Statement, parameters Parameters, options MutationOptions) (Output, error) {
	if engine.catalogStatement(statement) {
		return engine.executeCatalog(ctx, statement)
	}
	if statement.Select != nil {
		return engine.Query(ctx, statement, parameters)
	}
	if engine == nil || engine.catalog == nil || engine.rows == nil {
		return Output{}, executeError(result.CodeInternal, "mutation engine is not configured")
	}
	bound, err := bindParameters(statement, parameters)
	if err != nil {
		return Output{}, err
	}
	switch {
	case statement.Package != nil:
		return engine.executePackage(ctx, statement.Package, bound)
	case statement.Show != nil && statement.Show.Object == "HISTORY":
		return engine.showHistory(ctx, statement, bound)
	case statement.Show != nil && statement.Show.Object == "RELATIONS":
		return engine.showRelations(ctx, statement, bound)
	case statement.Show != nil && statement.Show.Object == "ROUTES":
		return engine.showRoutes(ctx, statement, bound)
	case statement.CreateRoute != nil:
		return engine.createRoute(ctx, statement.CreateRoute, bound, options)
	case statement.RenameRoute != nil:
		return engine.renameRoute(ctx, statement.RenameRoute, bound, options)
	case statement.DeleteRoute != nil:
		return engine.deleteRoute(ctx, statement.DeleteRoute, bound, options)
	case statement.OpenRoute != nil:
		return engine.openRoute(ctx, statement.OpenRoute, bound)
	case statement.Insert != nil:
		return engine.insert(ctx, statement.Insert, bound, options)
	case statement.Update != nil:
		return engine.update(ctx, statement.Update, bound, options)
	case statement.Delete != nil:
		return engine.delete(ctx, statement.Delete, bound, options)
	case statement.Restore != nil:
		return engine.restore(ctx, statement.Restore, bound, options)
	case statement.Relate != nil:
		return engine.relate(ctx, statement.Relate, bound, options)
	case statement.Unrelate != nil:
		return engine.unrelate(ctx, statement.Unrelate, bound, options)
	case statement.Match != nil:
		return engine.match(ctx, statement.Match, bound)
	default:
		return Output{}, unsupported(statement)
	}
}

func (engine *Engine) insert(ctx context.Context, insert *ast.InsertStatement, bound bindings, options MutationOptions) (Output, error) {
	if err := validateMutationOptions(options, false); err != nil {
		return Output{}, err
	}
	if uint64(len(insert.Values)) > options.MaxAffectedRows {
		return Output{}, affectedBudgetError(len(insert.Values), options.MaxAffectedRows)
	}
	if len(insert.Values) != 1 {
		return Output{}, executeError(result.CodeValidation, "v1 INSERT requires exactly one VALUES row")
	}
	databaseName, tableName, table, err := engine.bindTable(ctx, insert.Table)
	if err != nil {
		return Output{}, err
	}
	columns, err := insertColumns(table, insert.Columns)
	if err != nil {
		return Output{}, err
	}
	if len(columns) != len(insert.Values[0]) {
		return Output{}, executeError(result.CodeValidation, "INSERT column and value counts differ")
	}
	values := make(map[string]any, len(columns))
	for index, expression := range insert.Values[0] {
		value, err := evaluate(&expression, table, nil, bound)
		if err != nil {
			return Output{}, err
		}
		normalized, err := columns[index].Validate(value)
		if err != nil {
			return Output{}, err
		}
		values[columns[index].Name] = normalized
	}
	inserted, err := engine.rows.Insert(ctx, databaseName, tableName, values, datarow.WriteOptions{
		ExpectedSchemaVersion: options.ExpectedSchemaVersion,
		Metadata:              mutationMetadata(options),
		IndexTerms:            options.IndexTerms,
		RouteLeafIDs:          options.RouteLeafIDs,
	})
	if err != nil {
		return Output{}, normalizeError(err)
	}
	return mutationOutput(inserted), nil
}

func (engine *Engine) update(ctx context.Context, update *ast.UpdateStatement, bound bindings, options MutationOptions) (Output, error) {
	if err := validateMutationOptions(options, true); err != nil {
		return Output{}, err
	}
	databaseName, tableName, table, err := engine.bindTable(ctx, update.Table)
	if err != nil {
		return Output{}, err
	}
	assignments, err := bindAssignments(table, update.Assignments, bound)
	if err != nil {
		return Output{}, err
	}
	matches, err := engine.matchingRows(ctx, databaseName, tableName, table, update.Where, bound, options.MaxAffectedRows)
	if err != nil || len(matches) == 0 {
		return emptyMutationOutput(), err
	}
	if len(matches) != 1 {
		return Output{}, executeError(result.CodeValidation, "v1 revision mutation requires exactly one matched Row")
	}
	changes := make(map[string]any, len(assignments))
	for _, assignment := range assignments {
		value, err := evaluate(&assignment.expression, table, &matches[0], bound)
		if err != nil {
			return Output{}, err
		}
		normalized, err := assignment.column.Validate(value)
		if err != nil {
			return Output{}, err
		}
		changes[assignment.column.Name] = normalized
	}
	updated, err := engine.rows.Update(ctx, databaseName, tableName, matches[0].ID, changes, datarow.WriteOptions{
		ExpectedSchemaVersion: options.ExpectedSchemaVersion,
		ExpectedRevision:      options.ExpectedRevision,
		Metadata:              mutationMetadata(options),
		IndexTerms:            options.IndexTerms,
		RouteLeafIDs:          options.RouteLeafIDs,
	})
	if err != nil {
		return Output{}, normalizeError(err)
	}
	return mutationOutput(updated), nil
}

type boundAssignment struct {
	column     catalog.Column
	expression ast.Expression
}

func bindAssignments(table catalog.Table, assignments []ast.Assignment, bound bindings) ([]boundAssignment, error) {
	resultAssignments := make([]boundAssignment, 0, len(assignments))
	seen := make(map[string]struct{}, len(assignments))
	for _, assignment := range assignments {
		column, found := findColumn(table, assignment.Column.Value)
		if !found {
			return nil, executeError(result.CodeValidation, fmt.Sprintf("unknown update column %q", assignment.Column.Value))
		}
		if _, duplicate := seen[column.ID]; duplicate {
			return nil, executeError(result.CodeValidation, fmt.Sprintf("column %q is assigned more than once", column.Name))
		}
		if err := validateIdentifiers(table, &assignment.Value); err != nil {
			return nil, err
		}
		if !containsIdentifier(&assignment.Value) {
			value, err := evaluate(&assignment.Value, table, nil, bound)
			if err != nil {
				return nil, err
			}
			if _, err := column.Validate(value); err != nil {
				return nil, err
			}
		}
		seen[column.ID] = struct{}{}
		resultAssignments = append(resultAssignments, boundAssignment{column: column, expression: assignment.Value})
	}
	return resultAssignments, nil
}

func (engine *Engine) delete(ctx context.Context, deleteStatement *ast.DeleteStatement, bound bindings, options MutationOptions) (Output, error) {
	if err := validateMutationOptions(options, true); err != nil {
		return Output{}, err
	}
	databaseName, tableName, table, err := engine.bindTable(ctx, deleteStatement.Table)
	if err != nil {
		return Output{}, err
	}
	matches, err := engine.matchingRows(ctx, databaseName, tableName, table, deleteStatement.Where, bound, options.MaxAffectedRows)
	if err != nil || len(matches) == 0 {
		return emptyMutationOutput(), err
	}
	if len(matches) != 1 {
		return Output{}, executeError(result.CodeValidation, "v1 revision mutation requires exactly one matched Row")
	}
	deleted, err := engine.rows.Delete(ctx, databaseName, tableName, matches[0].ID, datarow.WriteOptions{
		ExpectedSchemaVersion: options.ExpectedSchemaVersion,
		ExpectedRevision:      options.ExpectedRevision,
		Metadata:              mutationMetadata(options),
	})
	if err != nil {
		return Output{}, normalizeError(err)
	}
	return mutationOutput(deleted), nil
}

func (engine *Engine) bindTable(ctx context.Context, name ast.Name) (string, string, catalog.Table, error) {
	databaseName, tableName, err := tableName(name)
	if err != nil {
		return "", "", catalog.Table{}, err
	}
	table, err := engine.catalog.DescribeTable(ctx, databaseName, tableName)
	if err != nil {
		return "", "", catalog.Table{}, normalizeError(err)
	}
	return databaseName, tableName, table, nil
}

func (engine *Engine) matchingRows(
	ctx context.Context,
	databaseName, tableName string,
	table catalog.Table,
	where *ast.Expression,
	bound bindings,
	maxAffected uint64,
) ([]datarow.Row, error) {
	if err := validateIdentifiers(table, where); err != nil {
		return nil, err
	}
	if err := validateStaticComparisons(table, where, bound); err != nil {
		return nil, err
	}
	var candidates []datarow.Row
	rowID, exact, err := exactRowID(where, table, bound)
	if err != nil {
		return nil, err
	}
	hasMore := false
	if exact {
		candidate, getErr := engine.rows.Get(ctx, databaseName, tableName, rowID)
		if getErr == nil {
			candidates = []datarow.Row{candidate}
		} else if !hasStableCode(getErr, result.CodeNotFound) {
			return nil, normalizeError(getErr)
		}
	} else {
		candidates, hasMore, err = engine.rows.ListPage(ctx, databaseName, tableName, maxQueryScan)
		if err != nil {
			return nil, normalizeError(err)
		}
	}
	matches := make([]datarow.Row, 0, min(len(candidates), int(maxAffected)+1))
	for _, candidate := range candidates {
		matched := true
		if where != nil {
			value, err := evaluate(where, table, &candidate, bound)
			if err != nil {
				return nil, err
			}
			boolean, ok := value.(bool)
			if !ok {
				return nil, typeError("WHERE expression must be BOOLEAN")
			}
			matched = boolean
		}
		if matched {
			matches = append(matches, candidate)
			if uint64(len(matches)) > maxAffected {
				return nil, affectedBudgetError(len(matches), maxAffected)
			}
		}
	}
	if hasMore {
		return nil, executeError(result.CodeOutputTruncated, "mutation candidate scan is incomplete")
	}
	return matches, nil
}

func insertColumns(table catalog.Table, identifiers []ast.Identifier) ([]catalog.Column, error) {
	if len(identifiers) == 0 {
		return append([]catalog.Column{}, table.Columns...), nil
	}
	columns := make([]catalog.Column, 0, len(identifiers))
	seen := make(map[string]struct{}, len(identifiers))
	for _, identifier := range identifiers {
		column, found := findColumn(table, identifier.Value)
		if !found {
			return nil, executeError(result.CodeValidation, fmt.Sprintf("unknown insert column %q", identifier.Value))
		}
		if _, duplicate := seen[column.ID]; duplicate {
			return nil, executeError(result.CodeValidation, fmt.Sprintf("column %q is inserted more than once", column.Name))
		}
		seen[column.ID] = struct{}{}
		columns = append(columns, column)
	}
	return columns, nil
}

func validateMutationOptions(options MutationOptions, revision bool) error {
	if options.ExpectedSchemaVersion == 0 {
		return executeError(result.CodeValidation, "mutation requires expected schema version")
	}
	if options.MaxAffectedRows < 1 || options.MaxAffectedRows > maxQueryScan {
		return executeError(result.CodeValidation, fmt.Sprintf("max_affected_rows must be between 1 and %d", maxQueryScan))
	}
	if revision && options.ExpectedRevision == 0 {
		return executeError(result.CodeValidation, "UPDATE/DELETE requires expected revision")
	}
	return nil
}

func affectedBudgetError(actual int, maximum uint64) error {
	return executeError(result.CodeConstraint, fmt.Sprintf("mutation matched %d rows; max_affected_rows is %d", actual, maximum))
}

func mutationOutput(changed datarow.Row) Output {
	revision := changed.Revision
	commitSequence := changed.CommitSequence
	return Output{
		Columns:      []result.Column{{Name: "row_id", Type: "TEXT", Nullable: false}},
		Rows:         []result.Row{{"row_id": changed.ID}},
		AffectedRows: 1, Revision: &revision, CommitSequence: &commitSequence,
	}
}

func emptyMutationOutput() Output {
	return Output{Columns: []result.Column{}, Rows: []result.Row{}}
}

func mutationMetadata(options MutationOptions) datarow.WriteMetadata {
	return datarow.WriteMetadata{Actor: options.Actor, Source: options.Source, Reason: options.Reason}
}
