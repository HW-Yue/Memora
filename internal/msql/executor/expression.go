package executor

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/result"
	datarow "github.com/HW-Yue/Memora/internal/row"
)

func evaluate(expression *ast.Expression, table catalog.Table, current *datarow.Row, bound bindings) (any, error) {
	if expression == nil {
		return nil, nil
	}
	switch expression.Kind {
	case "identifier":
		if current == nil {
			return nil, executeError(result.CodeValidation, "row identifier is not valid in this expression")
		}
		return fieldValue(table, *current, expression.Name)
	case "literal":
		return literalValue(expression.Literal)
	case "parameter":
		value, exists := bound[expression.Parameter.Ordinal]
		if !exists {
			return nil, executeError(result.CodeValidation, "parameter is not bound")
		}
		return value, nil
	case "unary":
		value, err := evaluate(expression.Operand, table, current, bound)
		if err != nil {
			return nil, err
		}
		switch expression.Operator {
		case "NOT":
			boolean, ok := value.(bool)
			if !ok {
				return nil, typeError("NOT requires BOOLEAN")
			}
			return !boolean, nil
		case "+", "-":
			integer, ok := integerValue(value)
			if !ok {
				return nil, typeError("unary arithmetic requires INTEGER")
			}
			if expression.Operator == "-" {
				if integer == math.MinInt64 {
					return nil, typeError("INTEGER arithmetic overflow")
				}
				return -integer, nil
			}
			return integer, nil
		}
	case "binary":
		left, err := evaluate(expression.Left, table, current, bound)
		if err != nil {
			return nil, err
		}
		right, err := evaluate(expression.Right, table, current, bound)
		if err != nil {
			return nil, err
		}
		left, right, err = normalizeOperands(expression.Operator, table, expression.Left, expression.Right, left, right)
		if err != nil {
			return nil, err
		}
		return binaryValue(expression.Operator, left, right)
	}
	return nil, executeError(result.CodeValidation, fmt.Sprintf("unsupported expression kind %q", expression.Kind))
}

func binaryValue(operator string, left, right any) (any, error) {
	switch operator {
	case "AND", "OR":
		leftBoolean, leftOK := left.(bool)
		rightBoolean, rightOK := right.(bool)
		if !leftOK || !rightOK {
			return nil, typeError(operator + " requires BOOLEAN operands")
		}
		if operator == "AND" {
			return leftBoolean && rightBoolean, nil
		}
		return leftBoolean || rightBoolean, nil
	case "+", "-", "*", "/", "%":
		leftInteger, leftOK := integerValue(left)
		rightInteger, rightOK := integerValue(right)
		if !leftOK || !rightOK {
			return nil, typeError("arithmetic requires INTEGER operands")
		}
		value, ok := checkedArithmetic(operator, leftInteger, rightInteger)
		if !ok {
			return nil, typeError("INTEGER arithmetic overflow or division by zero")
		}
		return value, nil
	case "=", "!=", "<>", "<", "<=", ">", ">=":
		if left == nil || right == nil {
			return false, nil
		}
		if operator != "=" && operator != "!=" && operator != "<>" {
			comparison, ok := compareValues(left, right)
			if !ok {
				return nil, typeError("ordered comparison requires compatible values")
			}
			switch operator {
			case "<":
				return comparison < 0, nil
			case "<=":
				return comparison <= 0, nil
			case ">":
				return comparison > 0, nil
			default:
				return comparison >= 0, nil
			}
		}
		equal, compatible := equalValues(left, right)
		if !compatible {
			return nil, typeError("equality comparison requires compatible values")
		}
		if operator == "=" {
			return equal, nil
		}
		return !equal, nil
	}
	return nil, executeError(result.CodeValidation, fmt.Sprintf("unsupported operator %q", operator))
}

func literalValue(literal *ast.Literal) (any, error) {
	if literal == nil {
		return nil, executeError(result.CodeValidation, "literal is missing")
	}
	switch literal.Kind {
	case "null":
		return nil, nil
	case "string":
		return literal.Value, nil
	case "boolean":
		return strings.EqualFold(literal.Value, "TRUE"), nil
	case "number":
		value, err := strconv.ParseInt(literal.Value, 10, 64)
		if err != nil {
			return nil, typeError("only INTEGER numeric literals are supported")
		}
		return value, nil
	default:
		return nil, executeError(result.CodeValidation, "unknown literal kind")
	}
}

func fieldValue(table catalog.Table, current datarow.Row, name *ast.Name) (any, error) {
	if name == nil || len(name.Parts) != 1 {
		return nil, executeError(result.CodeValidation, "row fields must be unqualified identifiers")
	}
	field := name.Parts[0].Value
	switch strings.ToLower(field) {
	case "row_id":
		return current.ID, nil
	case "revision":
		return current.Revision, nil
	case "commit_sequence":
		return current.CommitSequence, nil
	case "row_state":
		return string(current.State), nil
	case "schema_version":
		return current.SchemaVersion, nil
	}
	column, found := findColumn(table, field)
	if !found {
		return nil, executeError(result.CodeValidation, fmt.Sprintf("unknown column %q", field))
	}
	return current.Values[column.Name], nil
}

// findColumn is the executor's single Column-name resolver. Archived Columns
// stay in table.Columns so stored values keep decoding, so this is where a
// name must stop resolving to one.
func findColumn(table catalog.Table, name string) (catalog.Column, bool) {
	candidate := strings.ToLower(strings.TrimSpace(name))
	for _, column := range table.Columns {
		if column.Archived() {
			continue
		}
		if strings.ToLower(column.Name) == candidate {
			return column, true
		}
		for _, alias := range column.Aliases {
			if strings.ToLower(alias) == candidate {
				return column, true
			}
		}
	}
	return catalog.Column{}, false
}

func integerValue(value any) (int64, bool) {
	switch number := value.(type) {
	case int:
		return int64(number), true
	case int8:
		return int64(number), true
	case int16:
		return int64(number), true
	case int32:
		return int64(number), true
	case int64:
		return number, true
	case uint:
		if uint64(number) <= math.MaxInt64 {
			return int64(number), true
		}
	case uint8:
		return int64(number), true
	case uint16:
		return int64(number), true
	case uint32:
		return int64(number), true
	case uint64:
		if number <= math.MaxInt64 {
			return int64(number), true
		}
	case json.Number:
		integer, err := strconv.ParseInt(string(number), 10, 64)
		return integer, err == nil
	}
	return 0, false
}

func equalValues(left, right any) (bool, bool) {
	if left == nil || right == nil {
		return left == nil && right == nil, true
	}
	if leftInteger, ok := integerValue(left); ok {
		rightInteger, rightOK := integerValue(right)
		return rightOK && leftInteger == rightInteger, rightOK
	}
	switch leftValue := left.(type) {
	case string:
		rightValue, ok := right.(string)
		return ok && leftValue == rightValue, ok
	case bool:
		rightValue, ok := right.(bool)
		return ok && leftValue == rightValue, ok
	case time.Time:
		rightValue, ok := right.(time.Time)
		return ok && leftValue.Equal(rightValue), ok
	}
	return false, false
}

func normalizeOperands(operator string, table catalog.Table, leftExpression, rightExpression *ast.Expression, left, right any) (any, any, error) {
	switch operator {
	case "=", "!=", "<>", "<", "<=", ">", ">=":
	default:
		return left, right, nil
	}
	if left == nil || right == nil {
		return left, right, nil
	}
	if column, kind, found := expressionField(table, leftExpression); found && rightExpression.Kind != "identifier" {
		normalized, err := normalizeForField(column, kind, right)
		return left, normalized, err
	}
	if column, kind, found := expressionField(table, rightExpression); found && leftExpression.Kind != "identifier" {
		normalized, err := normalizeForField(column, kind, left)
		return normalized, right, err
	}
	return left, right, nil
}

func expressionField(table catalog.Table, expression *ast.Expression) (catalog.Column, string, bool) {
	if expression == nil || expression.Kind != "identifier" || expression.Name == nil || len(expression.Name.Parts) != 1 {
		return catalog.Column{}, "", false
	}
	name := expression.Name.Parts[0].Value
	if column, found := findColumn(table, name); found {
		return column, "", true
	}
	switch strings.ToLower(name) {
	case "row_id", "row_state":
		return catalog.Column{}, "string", true
	case "revision", "schema_version", "commit_sequence":
		return catalog.Column{}, "integer", true
	default:
		return catalog.Column{}, "", false
	}
}

func normalizeForField(column catalog.Column, kind string, value any) (any, error) {
	if kind == "" {
		return column.Validate(value)
	}
	if kind == "string" {
		if _, ok := value.(string); !ok {
			return nil, typeError("field requires string value")
		}
		return value, nil
	}
	integer, ok := integerValue(value)
	if !ok {
		return nil, typeError("field requires INTEGER value")
	}
	return integer, nil
}

func checkedArithmetic(operator string, left, right int64) (int64, bool) {
	switch operator {
	case "+":
		if (right > 0 && left > math.MaxInt64-right) || (right < 0 && left < math.MinInt64-right) {
			return 0, false
		}
		return left + right, true
	case "-":
		if (right < 0 && left > math.MaxInt64+right) || (right > 0 && left < math.MinInt64+right) {
			return 0, false
		}
		return left - right, true
	case "*":
		if left == 0 || right == 0 {
			return 0, true
		}
		if (left == math.MinInt64 && right == -1) || (right == math.MinInt64 && left == -1) {
			return 0, false
		}
		value := left * right
		return value, value/right == left
	case "/", "%":
		if right == 0 || (left == math.MinInt64 && right == -1) {
			return 0, false
		}
		if operator == "/" {
			return left / right, true
		}
		return left % right, true
	default:
		return 0, false
	}
}

func compareValues(left, right any) (int, bool) {
	if leftInteger, ok := integerValue(left); ok {
		rightInteger, rightOK := integerValue(right)
		if !rightOK {
			return 0, false
		}
		switch {
		case leftInteger < rightInteger:
			return -1, true
		case leftInteger > rightInteger:
			return 1, true
		default:
			return 0, true
		}
	}
	switch leftValue := left.(type) {
	case string:
		rightValue, ok := right.(string)
		if !ok {
			return 0, false
		}
		return strings.Compare(leftValue, rightValue), true
	case time.Time:
		rightValue, ok := right.(time.Time)
		if !ok {
			return 0, false
		}
		if leftValue.Before(rightValue) {
			return -1, true
		}
		if leftValue.After(rightValue) {
			return 1, true
		}
		return 0, true
	default:
		return 0, false
	}
}

func typeError(message string) error {
	return executeError(result.CodeConstraint, message)
}
