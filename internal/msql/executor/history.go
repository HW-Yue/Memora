package executor

import (
	"context"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/logical"
	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/result"
	datarow "github.com/HW-Yue/Memora/internal/row"
)

func (engine *Engine) showHistory(
	ctx context.Context,
	statement ast.Statement,
	bound bindings,
) (Output, error) {
	show := statement.Show
	if show == nil || show.Table == nil || show.Row == nil || show.Limit == nil {
		return Output{}, executeError(result.CodeValidation, "SHOW HISTORY is incomplete")
	}
	databaseName, tableName, table, err := engine.bindTable(ctx, *show.Table)
	if err != nil {
		return Output{}, err
	}
	rowID, err := historyRowID(show.Row, table, bound)
	if err != nil {
		return Output{}, err
	}
	limit, err := historyPositiveInteger(show.Limit, table, bound, "SHOW HISTORY LIMIT")
	if err != nil {
		return Output{}, err
	}
	if limit > maxQueryScan {
		return Output{}, executeError(result.CodeValidation, "SHOW HISTORY LIMIT must be between 1 and 1000")
	}
	records, hasMore, err := engine.rows.HistoryPage(ctx, databaseName, tableName, rowID, int(limit))
	if err != nil {
		return Output{}, normalizeError(err)
	}
	output := Output{
		Columns: []result.Column{
			{Name: "row_id", Type: "ID"},
			{Name: "revision", Type: "INTEGER"},
			{Name: "commit_sequence", Type: "INTEGER"},
			{Name: "schema_version", Type: "INTEGER"},
			{Name: "operation", Type: "TEXT"},
			{Name: "row_state", Type: "TEXT"},
			{Name: "actor", Type: "TEXT"},
			{Name: "source", Type: "TEXT"},
			{Name: "reason", Type: "TEXT"},
			{Name: "updated_at", Type: "TIMESTAMP"},
		},
		Rows:      make([]result.Row, 0, len(records)),
		Truncated: hasMore,
	}
	for _, record := range records {
		output.Rows = append(output.Rows, result.Row{
			"row_id": record.RowID, "revision": record.Revision,
			"commit_sequence": record.CommitSequence, "schema_version": record.SchemaVersion,
			"operation": string(record.Operation), "row_state": record.State,
			"actor": record.Actor, "source": record.Source, "reason": record.Reason,
			"updated_at": record.UpdatedAt,
		})
	}
	return output, nil
}

func (engine *Engine) restore(
	ctx context.Context,
	restore *ast.RestoreStatement,
	bound bindings,
	options MutationOptions,
) (Output, error) {
	if err := validateMutationOptions(options, true); err != nil {
		return Output{}, err
	}
	databaseName, tableName, table, err := engine.bindTable(ctx, restore.Table)
	if err != nil {
		return Output{}, err
	}
	rowID, err := historyRowID(restore.Row, table, bound)
	if err != nil {
		return Output{}, err
	}
	revision, err := historyPositiveInteger(restore.Revision, table, bound, "RESTORE revision")
	if err != nil {
		return Output{}, err
	}
	restored, err := engine.rows.Restore(
		ctx, databaseName, tableName, rowID, revision,
		datarow.WriteOptions{
			ExpectedSchemaVersion: options.ExpectedSchemaVersion,
			ExpectedRevision:      options.ExpectedRevision,
			Metadata:              mutationMetadata(options),
			IndexTerms:            options.IndexTerms,
			RouteLeafIDs:          options.RouteLeafIDs,
		},
	)
	if err != nil {
		return Output{}, normalizeError(err)
	}
	return mutationOutput(restored), nil
}

func historyRowID(expression *ast.Expression, table catalog.Table, bound bindings) (string, error) {
	if expression == nil || containsIdentifier(expression) {
		return "", executeError(result.CodeValidation, "history Row ID must be a literal or parameter")
	}
	value, err := evaluate(expression, table, nil, bound)
	if err != nil {
		return "", err
	}
	return normalizeHistoryRowID(value)
}

func normalizeHistoryRowID(value any) (string, error) {
	normalized, err := logical.Validate(logical.Constraint{
		Name: "row_id", Definition: logical.Definition{Kind: logical.KindRelationID},
	}, value)
	if err != nil {
		return "", normalizeError(err)
	}
	return normalized.(string), nil
}

func historyPositiveInteger(
	expression *ast.Expression,
	table catalog.Table,
	bound bindings,
	label string,
) (uint64, error) {
	if expression == nil || containsIdentifier(expression) {
		return 0, executeError(result.CodeValidation, label+" must be a literal or parameter")
	}
	value, err := evaluate(expression, table, nil, bound)
	if err != nil {
		return 0, err
	}
	integer, ok := integerValue(value)
	if !ok || integer < 1 {
		return 0, executeError(result.CodeValidation, label+" must be a positive INTEGER")
	}
	return uint64(integer), nil
}
