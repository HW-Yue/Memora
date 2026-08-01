package catalog

import (
	"context"
	"sort"
	"time"

	"github.com/HW-Yue/Memora/internal/logical"
)

func (service *Service) AddColumn(ctx context.Context, databaseName, tableName string, definition ColumnDefinition) (Column, error) {
	if err := validateColumnDefinition(definition); err != nil {
		return Column{}, err
	}
	var created Column
	err := service.mutate(ctx, func(state *snapshot) error {
		database, table, err := locateTable(state, databaseName, tableName)
		if err != nil {
			return err
		}
		if _, exists := findColumn(table, definition.Name); exists {
			return duplicate("column", definition.Name)
		}
		if role := normalizeSemanticRole(definition.SemanticRole); role == "title" || role == "summary" {
			for _, existing := range table.Columns {
				if normalizeSemanticRole(existing.SemanticRole) == role {
					return &Error{Code: CodeValidation, Object: "column", Name: definition.Name, Field: "unique " + role + " role"}
				}
			}
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

func (service *Service) ShowColumns(ctx context.Context, databaseName, tableName string) ([]Column, error) {
	table, err := service.DescribeTable(ctx, databaseName, tableName)
	if err != nil {
		return nil, err
	}
	columns := append([]Column{}, table.Columns...)
	sort.Slice(columns, func(left, right int) bool {
		return canonical(columns[left].Name) < canonical(columns[right].Name)
	})
	return columns, nil
}

func (service *Service) DescribeColumn(ctx context.Context, databaseName, tableName, columnName string) (Column, error) {
	table, err := service.DescribeTable(ctx, databaseName, tableName)
	if err != nil {
		return Column{}, err
	}
	column, found := findColumn(&table, columnName)
	if !found {
		return Column{}, missing("column", columnName)
	}
	return *column, nil
}

func (service *Service) RenameColumn(ctx context.Context, databaseName, tableName, columnName, newName string) (Column, error) {
	if err := require("column", columnName, "new name", newName); err != nil {
		return Column{}, err
	}
	if isReservedColumn(newName) {
		return Column{}, &Error{Code: CodeValidation, Object: "column", Name: newName, Field: "a non-system name"}
	}
	var renamed Column
	err := service.mutate(ctx, func(state *snapshot) error {
		database, table, err := locateTable(state, databaseName, tableName)
		if err != nil {
			return err
		}
		column, found := findColumn(table, columnName)
		if !found {
			return missing("column", columnName)
		}
		if conflict, exists := findColumn(table, newName); exists && conflict.ID != column.ID {
			return duplicate("column", newName)
		}
		if column.Name == newName {
			renamed = *column
			return nil
		}
		column.Aliases = addAlias(column.Aliases, column.Name)
		column.Name = newName
		column.SchemaVersion++
		column.UpdatedAt = service.clock.Now().UTC()
		touchTable(table, service.clock.Now())
		touchDatabase(database, service.clock.Now())
		renamed = *column
		return nil
	})
	return renamed, err
}

func (service *Service) newColumn(definition ColumnDefinition, now time.Time) (Column, error) {
	id, err := service.nextID("col")
	if err != nil {
		return Column{}, err
	}
	logicalType, err := logical.ParseDeclaration(definition.Type)
	if err != nil {
		return Column{}, err
	}
	return Column{
		ID: id, Name: definition.Name, Aliases: []string{}, Type: string(logicalType.Kind),
		MaxCharacters: logicalType.MaxCharacters,
		Nullable:      definition.Nullable, Purpose: definition.Purpose,
		SemanticRole: normalizeSemanticRole(definition.SemanticRole), SchemaVersion: 1,
		CreatedAt: now, UpdatedAt: now,
	}, nil
}

func validateColumnDefinition(definition ColumnDefinition) error {
	if err := require("column", definition.Name, "name", definition.Name); err != nil {
		return err
	}
	if isReservedColumn(definition.Name) {
		return &Error{Code: CodeValidation, Object: "column", Name: definition.Name, Field: "a non-system name"}
	}
	if err := require("column", definition.Name, "type", definition.Type); err != nil {
		return err
	}
	if err := require("column", definition.Name, "purpose", definition.Purpose); err != nil {
		return err
	}
	if !validSemanticRole(definition.SemanticRole) {
		return &Error{Code: CodeValidation, Object: "column", Name: definition.Name, Field: "a supported semantic role"}
	}
	_, err := logical.ParseDeclaration(definition.Type)
	return err
}

func normalizeSemanticRole(value string) string { return canonical(value) }

func validSemanticRole(value string) bool {
	switch normalizeSemanticRole(value) {
	case "", "title", "summary", "identity", "status":
		return true
	default:
		return false
	}
}

func uniqueDisplayRoles(columns []ColumnDefinition) error {
	seen := map[string]string{}
	for _, column := range columns {
		role := normalizeSemanticRole(column.SemanticRole)
		if role != "title" && role != "summary" {
			continue
		}
		if first, exists := seen[role]; exists {
			return &Error{Code: CodeValidation, Object: "column", Name: column.Name, Field: "unique " + role + " role; already used by " + first}
		}
		seen[role] = column.Name
	}
	return nil
}

func isReservedColumn(name string) bool {
	switch canonical(name) {
	case "row_id", "database_id", "table_id", "schema_version", "revision",
		"commit_sequence", "row_state", "created_at", "updated_at":
		return true
	default:
		return false
	}
}

func uniqueColumnDefinitions(columns []ColumnDefinition) error {
	seen := make(map[string]struct{}, len(columns))
	for _, column := range columns {
		key := canonical(column.Name)
		if _, exists := seen[key]; exists {
			return duplicate("column", column.Name)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func findColumn(table *Table, name string) (*Column, bool) {
	for index := range table.Columns {
		if hasName(table.Columns[index].Name, table.Columns[index].Aliases, name) {
			return &table.Columns[index], true
		}
	}
	return nil, false
}
