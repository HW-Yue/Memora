package executor

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/result"
	datarow "github.com/HW-Yue/Memora/internal/row"
)

type projection struct {
	name   string
	column result.Column
}

func (engine *Engine) Query(ctx context.Context, statement ast.Statement, parameters Parameters) (Output, error) {
	if statement.Select == nil {
		return Output{}, unsupported(statement)
	}
	if engine == nil || engine.catalog == nil || engine.rows == nil {
		return Output{}, executeError(result.CodeInternal, "query engine is not configured")
	}
	selectStatement := statement.Select
	databaseName, tableName, err := tableName(selectStatement.From)
	if err != nil {
		return Output{}, err
	}
	table, err := engine.catalog.DescribeTable(ctx, databaseName, tableName)
	if err != nil {
		return Output{}, normalizeError(err)
	}
	bound, err := bindParameters(statement, parameters)
	if err != nil {
		return Output{}, err
	}
	if selectStatement.Limit == nil {
		return Output{}, executeError(result.CodeValidation, "SELECT requires LIMIT")
	}
	limitValue, err := evaluate(selectStatement.Limit, table, nil, bound)
	if err != nil {
		return Output{}, err
	}
	limit, ok := integerValue(limitValue)
	budgets, err := engine.queryBudgets(ctx)
	if err != nil {
		return Output{}, err
	}
	if !ok || limit < 1 || limit > int64(budgets.SelectRows) {
		return Output{}, executeError(result.CodeValidation, fmt.Sprintf("LIMIT must be between 1 and configured select_rows %d", budgets.SelectRows))
	}
	projections, err := bindProjections(table, selectStatement.Projections)
	if err != nil {
		return Output{}, err
	}
	if err := validateIdentifiers(table, selectStatement.Where); err != nil {
		return Output{}, err
	}
	if err := validateStaticComparisons(table, selectStatement.Where, bound); err != nil {
		return Output{}, err
	}
	var candidates []datarow.Row
	hasMore := false
	rowID, exact, err := exactRowID(selectStatement.Where, table, bound)
	if err != nil {
		return Output{}, err
	}
	if selectStatement.AsOf != nil {
		if !exact {
			return Output{}, executeError(result.CodeValidation, "AS OF requires an exact row_id predicate")
		}
		rowID, err = normalizeHistoryRowID(rowID)
		if err != nil {
			return Output{}, err
		}
		if containsIdentifier(&selectStatement.AsOf.Value) {
			return Output{}, executeError(result.CodeValidation, "AS OF value cannot reference a Row field")
		}
		value, err := evaluate(&selectStatement.AsOf.Value, table, nil, bound)
		if err != nil {
			return Output{}, err
		}
		version, ok := integerValue(value)
		if !ok || version < 1 {
			return Output{}, executeError(result.CodeValidation, "AS OF value must be a positive INTEGER")
		}
		var candidate datarow.Row
		switch selectStatement.AsOf.Kind {
		case "REVISION":
			candidate, err = engine.rows.AsOfRevision(ctx, databaseName, tableName, rowID, uint64(version))
		case "COMMIT_SEQUENCE":
			candidate, err = engine.rows.AsOfCommit(ctx, databaseName, tableName, rowID, uint64(version))
		default:
			return Output{}, executeError(result.CodeValidation, "AS OF kind is invalid")
		}
		if err != nil {
			return Output{}, normalizeError(err)
		}
		candidates = []datarow.Row{candidate}
	} else if exact {
		candidate, getErr := engine.rows.Get(ctx, databaseName, tableName, rowID)
		if getErr == nil {
			candidates = []datarow.Row{candidate}
		} else if !hasStableCode(getErr, result.CodeNotFound) {
			return Output{}, normalizeError(getErr)
		}
	} else {
		candidates, hasMore, err = engine.rows.ListPage(ctx, databaseName, tableName, budgets.SelectScan)
		if err != nil {
			return Output{}, normalizeError(err)
		}
	}
	output := Output{
		Columns: make([]result.Column, len(projections)),
		Rows:    []result.Row{},
	}
	for index := range projections {
		output.Columns[index] = projections[index].column
	}
	for _, candidate := range candidates {
		matches := true
		if selectStatement.Where != nil {
			value, err := evaluate(selectStatement.Where, table, &candidate, bound)
			if err != nil {
				return Output{}, err
			}
			boolean, ok := value.(bool)
			if !ok {
				return Output{}, typeError("WHERE expression must be BOOLEAN")
			}
			matches = boolean
		}
		if !matches {
			continue
		}
		output.Rows = append(output.Rows, projectRow(candidate, projections))
		if len(output.Rows) == int(limit) {
			return output, nil
		}
	}
	output.Truncated = hasMore
	return output, nil
}

func exactRowID(expression *ast.Expression, table catalog.Table, bound bindings) (string, bool, error) {
	if expression == nil {
		return "", false, nil
	}
	if expression.Operator == "AND" {
		if value, found, err := exactRowID(expression.Left, table, bound); found || err != nil {
			return value, found, err
		}
		return exactRowID(expression.Right, table, bound)
	}
	if expression.Operator != "=" {
		return "", false, nil
	}
	for _, pair := range [][2]*ast.Expression{{expression.Left, expression.Right}, {expression.Right, expression.Left}} {
		if !isField(pair[0], "row_id") || containsIdentifier(pair[1]) {
			continue
		}
		value, err := evaluate(pair[1], table, nil, bound)
		if err != nil {
			return "", false, err
		}
		normalized, err := normalizeForField(catalog.Column{}, "string", value)
		if err != nil {
			return "", false, err
		}
		return normalized.(string), true, nil
	}
	return "", false, nil
}

func isField(expression *ast.Expression, name string) bool {
	return expression != nil && expression.Kind == "identifier" && expression.Name != nil &&
		len(expression.Name.Parts) == 1 && strings.EqualFold(expression.Name.Parts[0].Value, name)
}

func hasStableCode(err error, code result.Code) bool {
	var stable interface{ StableCode() string }
	return errors.As(err, &stable) && stable.StableCode() == string(code)
}

func tableName(name ast.Name) (string, string, error) {
	if len(name.Parts) != 2 {
		return "", "", executeError(result.CodeValidation, "table name must be database.table")
	}
	return name.Parts[0].Value, name.Parts[1].Value, nil
}

func bindProjections(table catalog.Table, expressions []ast.Expression) ([]projection, error) {
	projections := make([]projection, 0, len(expressions))
	for _, expression := range expressions {
		if expression.Kind == "star" {
			if len(expressions) != 1 {
				return nil, executeError(result.CodeValidation, "* must be the only projection")
			}
			return starProjections(table), nil
		}
		if expression.Kind != "identifier" || expression.Name == nil || len(expression.Name.Parts) != 1 {
			return nil, executeError(result.CodeValidation, "SELECT projections must be Column identifiers or *")
		}
		name := expression.Name.Parts[0].Value
		if system, found := systemProjection(name); found {
			projections = append(projections, system)
			continue
		}
		column, found := findColumn(table, name)
		if !found {
			return nil, executeError(result.CodeValidation, fmt.Sprintf("unknown column %q", name))
		}
		projections = append(projections, projection{
			name:   column.Name,
			column: result.Column{Name: column.Name, Type: column.Type, Nullable: column.Nullable},
		})
	}
	return projections, nil
}

func starProjections(table catalog.Table) []projection {
	projections := []projection{
		mustSystemProjection("row_id"),
		mustSystemProjection("revision"),
		mustSystemProjection("commit_sequence"),
		mustSystemProjection("row_state"),
		mustSystemProjection("schema_version"),
	}
	for _, column := range table.Columns {
		projections = append(projections, projection{
			name:   column.Name,
			column: result.Column{Name: column.Name, Type: column.Type, Nullable: column.Nullable},
		})
	}
	return projections
}

func systemProjection(name string) (projection, bool) {
	switch strings.ToLower(name) {
	case "row_id":
		return projection{name: "row_id", column: result.Column{Name: "row_id", Type: "ID"}}, true
	case "revision":
		return projection{name: "revision", column: result.Column{Name: "revision", Type: "INTEGER"}}, true
	case "commit_sequence":
		return projection{name: "commit_sequence", column: result.Column{Name: "commit_sequence", Type: "INTEGER"}}, true
	case "row_state":
		return projection{name: "row_state", column: result.Column{Name: "row_state", Type: "TEXT"}}, true
	case "schema_version":
		return projection{name: "schema_version", column: result.Column{Name: "schema_version", Type: "INTEGER"}}, true
	default:
		return projection{}, false
	}
}

func mustSystemProjection(name string) projection {
	value, _ := systemProjection(name)
	return value
}

func projectRow(current datarow.Row, projections []projection) result.Row {
	projected := make(result.Row, len(projections))
	for _, projection := range projections {
		switch projection.name {
		case "row_id":
			projected[projection.name] = current.ID
		case "revision":
			projected[projection.name] = current.Revision
		case "commit_sequence":
			projected[projection.name] = current.CommitSequence
		case "row_state":
			projected[projection.name] = string(current.State)
		case "schema_version":
			projected[projection.name] = current.SchemaVersion
		default:
			projected[projection.name] = current.Values[projection.name]
		}
	}
	return projected
}

func validateIdentifiers(table catalog.Table, expression *ast.Expression) error {
	if expression == nil {
		return nil
	}
	if expression.Kind == "identifier" {
		if expression.Name == nil || len(expression.Name.Parts) != 1 {
			return executeError(result.CodeValidation, "row fields must be unqualified identifiers")
		}
		name := expression.Name.Parts[0].Value
		if _, system := systemProjection(name); !system {
			if _, found := findColumn(table, name); !found {
				return executeError(result.CodeValidation, fmt.Sprintf("unknown column %q", name))
			}
		}
	}
	if err := validateIdentifiers(table, expression.Left); err != nil {
		return err
	}
	if err := validateIdentifiers(table, expression.Right); err != nil {
		return err
	}
	return validateIdentifiers(table, expression.Operand)
}

func validateStaticComparisons(table catalog.Table, expression *ast.Expression, bound bindings) error {
	if expression == nil {
		return nil
	}
	switch expression.Operator {
	case "=", "!=", "<>", "<", "<=", ">", ">=":
		if column, kind, found := expressionField(table, expression.Left); found && !containsIdentifier(expression.Right) {
			value, err := evaluate(expression.Right, table, nil, bound)
			if err != nil {
				return err
			}
			if value != nil {
				if _, err := normalizeForField(column, kind, value); err != nil {
					return err
				}
			}
		}
		if column, kind, found := expressionField(table, expression.Right); found && !containsIdentifier(expression.Left) {
			value, err := evaluate(expression.Left, table, nil, bound)
			if err != nil {
				return err
			}
			if value != nil {
				if _, err := normalizeForField(column, kind, value); err != nil {
					return err
				}
			}
		}
	}
	if err := validateStaticComparisons(table, expression.Left, bound); err != nil {
		return err
	}
	if err := validateStaticComparisons(table, expression.Right, bound); err != nil {
		return err
	}
	return validateStaticComparisons(table, expression.Operand, bound)
}

func containsIdentifier(expression *ast.Expression) bool {
	if expression == nil {
		return false
	}
	return expression.Kind == "identifier" ||
		containsIdentifier(expression.Left) ||
		containsIdentifier(expression.Right) ||
		containsIdentifier(expression.Operand)
}
