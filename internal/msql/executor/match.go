package executor

import (
	"context"
	"strings"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/result"
)

func (engine *Engine) match(
	ctx context.Context,
	statement *ast.MatchStatement,
	bound bindings,
) (Output, error) {
	_, _, table, err := engine.bindTable(ctx, statement.Table)
	if err != nil {
		return Output{}, err
	}
	queryValue, err := matchParameter(statement.Query, table, bound, "MATCH QUERY")
	if err != nil {
		return Output{}, err
	}
	query, ok := queryValue.(string)
	if !ok || strings.TrimSpace(query) == "" {
		return Output{}, executeError(result.CodeValidation, "MATCH QUERY must be non-empty TEXT")
	}
	termsValue, err := matchParameter(statement.Terms, table, bound, "MATCH TERMS")
	if err != nil {
		return Output{}, err
	}
	terms, ok := stringSlice(termsValue)
	if !ok {
		return Output{}, executeError(result.CodeValidation, "MATCH TERMS must be a TEXT array")
	}
	limit, err := historyPositiveInteger(statement.Limit, table, bound, "MATCH LIMIT")
	if err != nil {
		return Output{}, err
	}
	if limit > maxQueryScan {
		return Output{}, executeError(result.CodeValidation, "MATCH LIMIT must be between 1 and 1000")
	}
	matches, err := engine.rows.Match(
		ctx, table.DatabaseID, table.ID, query, terms, int(limit),
	)
	if err != nil {
		return Output{}, normalizeError(err)
	}
	output := Output{
		Columns: []result.Column{
			{Name: "database_id", Type: "ID"},
			{Name: "table_id", Type: "ID"},
			{Name: "row_id", Type: "ID"},
			{Name: "revision", Type: "INTEGER"},
			{Name: "score", Type: "FLOAT"},
			{Name: "agent_score", Type: "FLOAT"},
			{Name: "mechanical_score", Type: "FLOAT"},
		},
		Rows: make([]result.Row, 0, len(matches.Matches)), Truncated: matches.Truncated,
	}
	for _, match := range matches.Matches {
		output.Rows = append(output.Rows, result.Row{
			"database_id": match.DatabaseID, "table_id": match.TableID,
			"row_id": match.RowID, "revision": match.Revision, "score": match.Score,
			"agent_score": match.AgentScore, "mechanical_score": match.MechanicalScore,
		})
	}
	return output, nil
}

func matchParameter(
	expression *ast.Expression,
	table catalog.Table,
	bound bindings,
	label string,
) (any, error) {
	if expression == nil || containsIdentifier(expression) {
		return nil, executeError(result.CodeValidation, label+" must be a literal or parameter")
	}
	return evaluate(expression, table, nil, bound)
}

func stringSlice(value any) ([]string, bool) {
	switch values := value.(type) {
	case []string:
		return append([]string{}, values...), true
	case []any:
		resultValues := make([]string, len(values))
		for index, value := range values {
			text, ok := value.(string)
			if !ok {
				return nil, false
			}
			resultValues[index] = text
		}
		return resultValues, true
	default:
		return nil, false
	}
}
