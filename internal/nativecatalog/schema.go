package nativecatalog

import (
	"context"
	"sort"

	"github.com/HW-Yue/Memora/internal/catalog"
)

func (service *Service) RenameDatabase(ctx context.Context, name, newName string) (catalog.Database, error) {
	if err := required("database", name, "new name", newName); err != nil {
		return catalog.Database{}, err
	}
	var renamed catalog.Database
	err := service.mutate(ctx, func(databases *[]catalog.Database) error {
		database, ok := findDatabase(*databases, name)
		if !ok {
			return catalogFailure(catalog.CodeNotFound, "database", name, "")
		}
		if conflict, exists := findDatabase(*databases, newName); exists && conflict.ID != database.ID {
			return catalogFailure(catalog.CodeAlreadyExists, "database", newName, "")
		}
		if database.Name != newName {
			database.Aliases = addAlias(database.Aliases, database.Name)
			database.Name = newName
			touchDatabase(database, service.clock.Now())
		}
		renamed = *database
		return nil
	})
	return renamed, err
}

func (service *Service) RenameTable(ctx context.Context, databaseName, tableName, newName string) (catalog.Table, error) {
	if err := required("table", tableName, "new name", newName); err != nil {
		return catalog.Table{}, err
	}
	var renamed catalog.Table
	err := service.mutate(ctx, func(databases *[]catalog.Database) error {
		database, table, err := locateTable(*databases, databaseName, tableName)
		if err != nil {
			return err
		}
		if conflict, ok := findTable(database, newName); ok && conflict.ID != table.ID {
			return catalogFailure(catalog.CodeAlreadyExists, "table", newName, "")
		}
		if table.Name != newName {
			now := service.clock.Now()
			table.Aliases = addAlias(table.Aliases, table.Name)
			table.Name = newName
			touchTable(table, now)
			touchDatabase(database, now)
		}
		renamed = *table
		return nil
	})
	return renamed, err
}

func (service *Service) AddColumn(ctx context.Context, databaseName, tableName string, definition catalog.ColumnDefinition) (catalog.Column, error) {
	if err := validateColumnDefinition(definition); err != nil {
		return catalog.Column{}, err
	}
	var created catalog.Column
	err := service.mutate(ctx, func(databases *[]catalog.Database) error {
		database, table, err := locateTable(*databases, databaseName, tableName)
		if err != nil {
			return err
		}
		if _, ok := findColumn(table, definition.Name); ok {
			return catalogFailure(catalog.CodeAlreadyExists, "column", definition.Name, "")
		}
		now := service.clock.Now().UTC()
		created, err = service.newColumn(definition, now)
		if err != nil {
			return err
		}
		table.Columns = append(table.Columns, created)
		touchTable(table, now)
		touchDatabase(database, now)
		return nil
	})
	return created, err
}

func (service *Service) ShowColumns(ctx context.Context, databaseName, tableName string) ([]catalog.Column, error) {
	table, err := service.DescribeTable(ctx, databaseName, tableName)
	if err != nil {
		return nil, err
	}
	columns := append([]catalog.Column(nil), table.Columns...)
	sort.Slice(columns, func(left, right int) bool { return canonical(columns[left].Name) < canonical(columns[right].Name) })
	return columns, nil
}

func (service *Service) DescribeColumn(ctx context.Context, databaseName, tableName, columnName string) (catalog.Column, error) {
	table, err := service.DescribeTable(ctx, databaseName, tableName)
	if err != nil {
		return catalog.Column{}, err
	}
	column, ok := findColumn(&table, columnName)
	if !ok {
		return catalog.Column{}, catalogFailure(catalog.CodeNotFound, "column", columnName, "")
	}
	return *column, nil
}

func (service *Service) RenameColumn(ctx context.Context, databaseName, tableName, columnName, newName string) (catalog.Column, error) {
	if err := required("column", columnName, "new name", newName); err != nil {
		return catalog.Column{}, err
	}
	var renamed catalog.Column
	err := service.mutate(ctx, func(databases *[]catalog.Database) error {
		database, table, err := locateTable(*databases, databaseName, tableName)
		if err != nil {
			return err
		}
		column, ok := findColumn(table, columnName)
		if !ok {
			return catalogFailure(catalog.CodeNotFound, "column", columnName, "")
		}
		if conflict, ok := findColumn(table, newName); ok && conflict.ID != column.ID {
			return catalogFailure(catalog.CodeAlreadyExists, "column", newName, "")
		}
		if column.Name != newName {
			now := service.clock.Now()
			column.Aliases = addAlias(column.Aliases, column.Name)
			column.Name = newName
			column.SchemaVersion++
			column.UpdatedAt = now.UTC()
			touchTable(table, now)
			touchDatabase(database, now)
		}
		renamed = *column
		return nil
	})
	return renamed, err
}
