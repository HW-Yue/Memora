package executor

import (
	"fmt"

	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/result"
)

type bindings map[int]any

func bindParameters(statement ast.Statement, supplied Parameters) (bindings, error) {
	occurrences := (ast.Document{Statement: statement}).Parameters()
	bound := make(bindings, len(occurrences))
	usedNames := make(map[string]struct{})
	positional := 0
	for _, parameter := range occurrences {
		switch parameter.Style {
		case "named":
			value, exists := supplied.Named[parameter.Name]
			if !exists {
				return nil, executeError(result.CodeValidation, fmt.Sprintf("parameter :%s is not bound", parameter.Name))
			}
			usedNames[parameter.Name] = struct{}{}
			bound[parameter.Ordinal] = value
		case "positional":
			if positional >= len(supplied.Positional) {
				return nil, executeError(result.CodeValidation, fmt.Sprintf("positional parameter %d is not bound", positional+1))
			}
			bound[parameter.Ordinal] = supplied.Positional[positional]
			positional++
		default:
			return nil, executeError(result.CodeValidation, "parameter has an unknown style")
		}
	}
	if positional != len(supplied.Positional) {
		return nil, executeError(result.CodeValidation, "too many positional parameters")
	}
	for name := range supplied.Named {
		if _, used := usedNames[name]; !used {
			return nil, executeError(result.CodeValidation, fmt.Sprintf("named parameter :%s is unused", name))
		}
	}
	return bound, nil
}
