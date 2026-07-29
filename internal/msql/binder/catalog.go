package binder

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	dictionary "github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/result"
)

type CatalogService interface {
	CreateDatabase(context.Context, dictionary.DatabaseDefinition) (dictionary.Database, error)
	ShowDatabases(context.Context) ([]dictionary.Database, error)
	DescribeDatabase(context.Context, string) (dictionary.Database, error)
	RenameDatabase(context.Context, string, string) (dictionary.Database, error)
	CreateTable(context.Context, string, dictionary.TableDefinition) (dictionary.Table, error)
	ShowTables(context.Context, string) ([]dictionary.Table, error)
	DescribeTable(context.Context, string, string) (dictionary.Table, error)
	RenameTable(context.Context, string, string, string) (dictionary.Table, error)
	AddColumn(context.Context, string, string, dictionary.ColumnDefinition) (dictionary.Column, error)
	ShowColumns(context.Context, string, string) ([]dictionary.Column, error)
	DescribeColumn(context.Context, string, string, string) (dictionary.Column, error)
	RenameColumn(context.Context, string, string, string, string) (dictionary.Column, error)
}

type Catalog struct {
	service CatalogService
}

type CatalogResult struct {
	Object    string                `json:"object"`
	Database  *dictionary.Database  `json:"database,omitempty"`
	Databases []dictionary.Database `json:"databases,omitempty"`
	Table     *dictionary.Table     `json:"table,omitempty"`
	Tables    []dictionary.Table    `json:"tables,omitempty"`
	Column    *dictionary.Column    `json:"column,omitempty"`
	Columns   []dictionary.Column   `json:"columns,omitempty"`
}

func NewCatalog(service CatalogService) *Catalog {
	return &Catalog{service: service}
}

func (binder *Catalog) Execute(ctx context.Context, statement ast.Statement) (CatalogResult, error) {
	if binder == nil || binder.service == nil {
		return CatalogResult{}, bindError(result.CodeInternal, "catalog binder is not configured")
	}
	switch {
	case statement.Create != nil:
		return binder.create(ctx, statement.Create)
	case statement.Show != nil:
		return binder.show(ctx, statement.Show)
	case statement.Describe != nil:
		return binder.describe(ctx, statement.Describe)
	case statement.Alter != nil:
		return binder.alter(ctx, statement.Alter)
	default:
		return CatalogResult{}, bindError(result.CodeUnsupported, "statement does not operate on the catalog")
	}
}

func (binder *Catalog) create(ctx context.Context, create *ast.CreateStatement) (CatalogResult, error) {
	switch create.Object {
	case "DATABASE":
		names, err := qualified(create.Name, 1, "database")
		if err != nil {
			return CatalogResult{}, err
		}
		database, err := binder.service.CreateDatabase(ctx, dictionary.DatabaseDefinition{
			Name: names[0], Purpose: create.Purpose, Scope: create.Scope, AntiScope: create.AntiScope,
		})
		return CatalogResult{Object: "DATABASE", Database: &database}, catalogError(err)
	case "TABLE":
		names, err := qualified(create.Name, 2, "table")
		if err != nil {
			return CatalogResult{}, err
		}
		columns := make([]dictionary.ColumnDefinition, 0, len(create.Columns))
		for _, column := range create.Columns {
			columns = append(columns, columnDefinition(column))
		}
		table, err := binder.service.CreateTable(ctx, names[0], dictionary.TableDefinition{
			Name: names[1], Purpose: create.Purpose, Scope: create.Scope,
			AntiScope: create.AntiScope, RowSemantics: create.RowSemantics, Columns: columns,
		})
		return CatalogResult{Object: "TABLE", Table: &table}, catalogError(err)
	default:
		return CatalogResult{}, bindError(result.CodeUnsupported, fmt.Sprintf("unsupported CREATE object %q", create.Object))
	}
}

func (binder *Catalog) show(ctx context.Context, show *ast.ShowStatement) (CatalogResult, error) {
	switch show.Object {
	case "DATABASES":
		databases, err := binder.service.ShowDatabases(ctx)
		for index := range databases {
			databases[index].Tables = []dictionary.Table{}
		}
		return CatalogResult{Object: "DATABASES", Databases: databases}, catalogError(err)
	case "TABLES":
		if show.Database == nil {
			return CatalogResult{}, bindError(result.CodeValidation, "SHOW TABLES requires FROM database")
		}
		names, err := qualified(*show.Database, 1, "database")
		if err != nil {
			return CatalogResult{}, err
		}
		tables, err := binder.service.ShowTables(ctx, names[0])
		for index := range tables {
			tables[index].Columns = []dictionary.Column{}
		}
		return CatalogResult{Object: "TABLES", Tables: tables}, catalogError(err)
	case "COLUMNS":
		if show.Table == nil {
			return CatalogResult{}, bindError(result.CodeValidation, "SHOW COLUMNS requires FROM database.table")
		}
		names, err := qualified(*show.Table, 2, "table")
		if err != nil {
			return CatalogResult{}, err
		}
		columns, err := binder.service.ShowColumns(ctx, names[0], names[1])
		return CatalogResult{Object: "COLUMNS", Columns: columns}, catalogError(err)
	default:
		return CatalogResult{}, bindError(result.CodeUnsupported, fmt.Sprintf("unsupported SHOW object %q", show.Object))
	}
}

func (binder *Catalog) describe(ctx context.Context, describe *ast.DescribeStatement) (CatalogResult, error) {
	switch describe.Object {
	case "DATABASE":
		names, err := qualified(describe.Name, 1, "database")
		if err != nil {
			return CatalogResult{}, err
		}
		database, err := binder.service.DescribeDatabase(ctx, names[0])
		if describe.Compact {
			database.Tables = []dictionary.Table{}
		}
		return CatalogResult{Object: "DATABASE", Database: &database}, catalogError(err)
	case "TABLE":
		names, err := qualified(describe.Name, 2, "table")
		if err != nil {
			return CatalogResult{}, err
		}
		table, err := binder.service.DescribeTable(ctx, names[0], names[1])
		if describe.Compact {
			table.Columns = []dictionary.Column{}
		}
		return CatalogResult{Object: "TABLE", Table: &table}, catalogError(err)
	case "COLUMN":
		names, err := qualified(describe.Name, 3, "column")
		if err != nil {
			return CatalogResult{}, err
		}
		column, err := binder.service.DescribeColumn(ctx, names[0], names[1], names[2])
		return CatalogResult{Object: "COLUMN", Column: &column}, catalogError(err)
	default:
		return CatalogResult{}, bindError(result.CodeUnsupported, fmt.Sprintf("unsupported DESCRIBE object %q", describe.Object))
	}
}

func (binder *Catalog) alter(ctx context.Context, alter *ast.AlterStatement) (CatalogResult, error) {
	switch {
	case alter.Object == "DATABASE" && alter.Action == "RENAME":
		names, err := qualified(alter.Name, 1, "database")
		if err != nil {
			return CatalogResult{}, err
		}
		newName, err := requiredIdentifier(alter.NewName, "database rename target")
		if err != nil {
			return CatalogResult{}, err
		}
		database, err := binder.service.RenameDatabase(ctx, names[0], newName)
		return CatalogResult{Object: "DATABASE", Database: &database}, catalogError(err)
	case alter.Object == "TABLE" && alter.Action == "RENAME":
		names, err := qualified(alter.Name, 2, "table")
		if err != nil {
			return CatalogResult{}, err
		}
		newName, err := requiredIdentifier(alter.NewName, "table rename target")
		if err != nil {
			return CatalogResult{}, err
		}
		table, err := binder.service.RenameTable(ctx, names[0], names[1], newName)
		return CatalogResult{Object: "TABLE", Table: &table}, catalogError(err)
	case alter.Object == "TABLE" && alter.Action == "ADD_COLUMN":
		names, err := qualified(alter.Name, 2, "table")
		if err != nil {
			return CatalogResult{}, err
		}
		if alter.Column == nil {
			return CatalogResult{}, bindError(result.CodeValidation, "ADD COLUMN requires a column definition")
		}
		column, err := binder.service.AddColumn(ctx, names[0], names[1], columnDefinition(*alter.Column))
		return CatalogResult{Object: "COLUMN", Column: &column}, catalogError(err)
	case alter.Object == "TABLE" && alter.Action == "RENAME_COLUMN":
		names, err := qualified(alter.Name, 2, "table")
		if err != nil {
			return CatalogResult{}, err
		}
		columnName, err := requiredIdentifier(alter.ColumnName, "column rename source")
		if err != nil {
			return CatalogResult{}, err
		}
		newName, err := requiredIdentifier(alter.NewName, "column rename target")
		if err != nil {
			return CatalogResult{}, err
		}
		column, err := binder.service.RenameColumn(ctx, names[0], names[1], columnName, newName)
		return CatalogResult{Object: "COLUMN", Column: &column}, catalogError(err)
	default:
		return CatalogResult{}, bindError(result.CodeUnsupported, fmt.Sprintf("unsupported ALTER %s action %q", alter.Object, alter.Action))
	}
}

func qualified(name ast.Name, count int, object string) ([]string, error) {
	if len(name.Parts) != count {
		return nil, bindError(result.CodeValidation, fmt.Sprintf("%s name requires %d part(s), got %d", object, count, len(name.Parts)))
	}
	values := make([]string, count)
	for index := range name.Parts {
		if strings.TrimSpace(name.Parts[index].Value) == "" {
			return nil, bindError(result.CodeValidation, fmt.Sprintf("%s name contains an empty identifier", object))
		}
		values[index] = name.Parts[index].Value
	}
	return values, nil
}

func requiredIdentifier(identifier *ast.Identifier, description string) (string, error) {
	if identifier == nil || strings.TrimSpace(identifier.Value) == "" {
		return "", bindError(result.CodeValidation, description+" is missing")
	}
	return identifier.Value, nil
}

func columnDefinition(column ast.ColumnDefinition) dictionary.ColumnDefinition {
	typeName := column.Type.Name.Value
	if len(column.Type.Arguments) > 0 {
		arguments := make([]string, len(column.Type.Arguments))
		for index, argument := range column.Type.Arguments {
			arguments[index] = strconv.FormatInt(argument, 10)
		}
		typeName += "(" + strings.Join(arguments, ",") + ")"
	}
	return dictionary.ColumnDefinition{
		Name: column.Name.Value, Type: typeName, Nullable: !column.NotNull, Purpose: column.Purpose,
	}
}

func catalogError(err error) error {
	if err == nil {
		return nil
	}
	var stable interface{ StableCode() string }
	if errors.As(err, &stable) {
		return err
	}
	switch {
	case errors.Is(err, context.Canceled):
		return bindError(result.CodeCancelled, "catalog operation was cancelled")
	case errors.Is(err, context.DeadlineExceeded):
		return bindError(result.CodeDeadlineExceeded, "catalog operation exceeded its deadline")
	default:
		return bindError(result.CodeInternal, "catalog operation failed")
	}
}
