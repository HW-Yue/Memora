package executor

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/msql/binder"
	"github.com/HW-Yue/Memora/internal/result"
)

func (engine *Engine) catalogStatement(statement ast.Statement) bool {
	if statement.Create != nil || statement.Alter != nil || statement.Describe != nil {
		return true
	}
	if statement.Show == nil {
		return false
	}
	switch statement.Show.Object {
	case "DATABASES", "TABLES", "COLUMNS":
		return true
	default:
		return false
	}
}

func (engine *Engine) executeCatalog(
	ctx context.Context,
	statement ast.Statement,
) (Output, error) {
	if engine == nil || engine.catalogBinder == nil {
		return Output{}, executeError(
			result.CodeUnsupported,
			"Catalog DDL and discovery require an autocommit database session",
		)
	}
	value, err := engine.catalogBinder.Execute(ctx, statement)
	if err != nil {
		return Output{}, normalizeError(err)
	}
	rows, err := catalogRows(value)
	if err != nil {
		return Output{}, err
	}
	return Output{Columns: []result.Column{}, Rows: rows}, nil
}

func catalogRows(value binder.CatalogResult) ([]result.Row, error) {
	objects := []any{}
	switch {
	case value.Database != nil:
		objects = append(objects, value.Database)
	case value.Table != nil:
		objects = append(objects, value.Table)
	case value.Column != nil:
		objects = append(objects, value.Column)
	case value.Databases != nil:
		for index := range value.Databases {
			objects = append(objects, value.Databases[index])
		}
	case value.Tables != nil:
		for index := range value.Tables {
			objects = append(objects, value.Tables[index])
		}
	case value.Columns != nil:
		for index := range value.Columns {
			objects = append(objects, value.Columns[index])
		}
	}
	rows := make([]result.Row, 0, len(objects))
	for _, object := range objects {
		encoded, err := json.Marshal(object)
		if err != nil {
			return nil, executeError(result.CodeInternal, "Catalog result could not be encoded")
		}
		decoder := json.NewDecoder(bytes.NewReader(encoded))
		decoder.UseNumber()
		var row result.Row
		if err := decoder.Decode(&row); err != nil {
			return nil, executeError(result.CodeInternal, "Catalog result could not be normalized")
		}
		rows = append(rows, row)
	}
	return rows, nil
}
