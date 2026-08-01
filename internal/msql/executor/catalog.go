package executor

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/msql/binder"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/security"
)

func (engine *Engine) catalogStatement(statement ast.Statement) bool {
	if statement.Create != nil || statement.Alter != nil ||
		(statement.Describe != nil && statement.Describe.Object != "ROUTE") {
		return true
	}
	if statement.Show == nil {
		return false
	}
	switch statement.Show.Object {
	case "DATABASES", "TABLES", "COLUMNS", "CATALOG_ATLAS":
		return true
	default:
		return false
	}
}

func (engine *Engine) executeCatalog(
	ctx context.Context,
	statement ast.Statement,
	bound bindings,
) (Output, error) {
	if engine == nil || engine.catalogBinder == nil {
		return Output{}, executeError(
			result.CodeUnsupported,
			"Catalog DDL and discovery require an autocommit database session",
		)
	}
	pageRequest, paged, err := catalogPageRequestFor(statement, bound)
	if err != nil {
		return Output{}, err
	}
	if statement.Show != nil && statement.Show.Object == "CATALOG_ATLAS" {
		return engine.executeCatalogAtlas(ctx, pageRequest)
	}
	value, err := engine.catalogBinder.Execute(ctx, statement)
	if err != nil {
		return Output{}, normalizeError(err)
	}
	rows, err := catalogRows(value)
	if err != nil {
		return Output{}, err
	}
	if statement.Show != nil && statement.Show.Object == "DATABASES" {
		rows = authorizedDatabaseRows(ctx, rows)
	}
	output := Output{Columns: []result.Column{}, Rows: rows}
	if paged {
		rows, page, err := paginateCatalogRows(rows, pageRequest)
		if err != nil {
			return Output{}, err
		}
		output.Rows = rows
		output.Truncated = page.Truncated
		output.NextCursor = page.NextCursor
		output.Page = page
	}
	return output, nil
}

func authorizedDatabaseRows(ctx context.Context, rows []result.Row) []result.Row {
	authorization, present := security.AuthorizationFrom(ctx)
	if !present {
		return rows
	}
	authorized := make([]result.Row, 0, len(rows))
	for _, row := range rows {
		selectors := []string{}
		if id, ok := row["database_id"].(string); ok {
			selectors = append(selectors, id)
		}
		if name, ok := row["name"].(string); ok {
			selectors = append(selectors, name)
		}
		if aliases, ok := row["aliases"].([]any); ok {
			for _, alias := range aliases {
				if text, ok := alias.(string); ok {
					selectors = append(selectors, text)
				}
			}
		}
		if security.AllowsAnyDatabase(authorization, selectors...) {
			authorized = append(authorized, row)
		}
	}
	return authorized
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
