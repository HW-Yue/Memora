package nativecatalog

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/store/catalogindex"
)

type LookupIndex interface {
	DatabaseByID(string) (catalogindex.Locator, error)
	DatabaseByName(string) (catalogindex.Locator, error)
	TableByID(string) (catalogindex.Locator, error)
	TableByName(string, string) (catalogindex.Locator, error)
	ColumnsForTable(string) ([]catalogindex.Locator, error)
}

type EnumerationIndex interface {
	Databases() ([]catalogindex.Locator, error)
	TablesForDatabase(string) ([]catalogindex.Locator, error)
}

// IndexedReader resolves one Table schema without rebuilding the full Catalog.
type IndexedReader struct {
	repository *Repository
	index      LookupIndex
}

func NewIndexedReader(repository *Repository, index LookupIndex) (*IndexedReader, error) {
	if repository == nil || repository.file == nil || index == nil {
		return nil, fmt.Errorf("%w: Catalog repository and lookup index are required", ErrInvalid)
	}
	return &IndexedReader{repository: repository, index: index}, nil
}

func (reader *IndexedReader) DescribeTable(
	ctx context.Context,
	databaseName, tableName string,
) (catalog.Table, error) {
	if reader == nil || reader.repository == nil || reader.index == nil {
		return catalog.Table{}, fmt.Errorf("%w: indexed Catalog reader", ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return catalog.Table{}, err
	}
	databaseLocator, err := reader.databaseLocator(databaseName)
	if err != nil {
		if errors.Is(err, catalogindex.ErrNotFound) {
			return catalog.Table{}, &catalog.Error{Code: catalog.CodeNotFound, Object: "database", Name: databaseName}
		}
		return catalog.Table{}, err
	}
	database, err := reader.repository.readDatabaseRevision(
		databaseLocator.ID, databaseLocator.SchemaRevision,
	)
	if err != nil {
		return catalog.Table{}, err
	}
	if databaseLocator.Kind != catalogindex.KindDatabase ||
		database.value.ID != databaseLocator.ID ||
		!matchesSelector(databaseName, database.value.ID, database.value.Name, database.value.Aliases) {
		return catalog.Table{}, fmt.Errorf("%w: Database locator and record disagree", ErrCorrupt)
	}

	tableLocator, err := reader.tableLocator(databaseLocator.ID, tableName)
	if err != nil {
		if errors.Is(err, catalogindex.ErrNotFound) {
			return catalog.Table{}, &catalog.Error{Code: catalog.CodeNotFound, Object: "table", Name: tableName}
		}
		return catalog.Table{}, err
	}
	tableRecord, err := reader.repository.readTableRevision(
		tableLocator.ID, tableLocator.SchemaRevision,
	)
	if err != nil {
		return catalog.Table{}, err
	}
	if tableLocator.Kind != catalogindex.KindTable ||
		tableLocator.DatabaseID != databaseLocator.ID ||
		tableRecord.value.ID != tableLocator.ID ||
		tableRecord.value.DatabaseID != databaseLocator.ID ||
		!matchesSelector(tableName, tableRecord.value.ID, tableRecord.value.Name, tableRecord.value.Aliases) {
		return catalog.Table{}, fmt.Errorf("%w: Table locator and record disagree", ErrCorrupt)
	}

	locators, err := reader.index.ColumnsForTable(tableLocator.ID)
	if err != nil {
		return catalog.Table{}, err
	}
	columns := make([]columnRecord, 0, len(locators))
	orders := make(map[uint64]struct{}, len(locators))
	ids := make(map[string]struct{}, len(locators))
	for _, locator := range locators {
		if locator.Kind != catalogindex.KindColumn ||
			locator.DatabaseID != databaseLocator.ID ||
			locator.TableID != tableLocator.ID {
			return catalog.Table{}, fmt.Errorf("%w: Column locator scope mismatch", ErrCorrupt)
		}
		column, err := reader.repository.readColumnRevision(locator.ID, locator.SchemaRevision)
		if err != nil {
			return catalog.Table{}, err
		}
		if column.tableID != tableLocator.ID {
			return catalog.Table{}, fmt.Errorf("%w: Column record scope mismatch", ErrCorrupt)
		}
		if _, duplicate := orders[column.order]; duplicate {
			return catalog.Table{}, fmt.Errorf("%w: duplicate Column order", ErrCorrupt)
		}
		if _, duplicate := ids[column.value.ID]; duplicate {
			return catalog.Table{}, fmt.Errorf("%w: duplicate Column identity", ErrCorrupt)
		}
		orders[column.order] = struct{}{}
		ids[column.value.ID] = struct{}{}
		columns = append(columns, column)
	}
	sort.Slice(columns, func(left, right int) bool { return columns[left].order < columns[right].order })
	table := tableRecord.value
	if table.Aliases == nil {
		table.Aliases = []string{}
	}
	table.Columns = make([]catalog.Column, 0, len(columns))
	for _, record := range columns {
		column := record.value
		if column.Aliases == nil {
			column.Aliases = []string{}
		}
		table.Columns = append(table.Columns, column)
	}
	return table, nil
}

func (reader *IndexedReader) databaseLocator(selector string) (catalogindex.Locator, error) {
	if strings.HasPrefix(selector, "db_") {
		return reader.index.DatabaseByID(selector)
	}
	return reader.index.DatabaseByName(selector)
}

func (reader *IndexedReader) tableLocator(databaseID, selector string) (catalogindex.Locator, error) {
	if strings.HasPrefix(selector, "tbl_") {
		return reader.index.TableByID(selector)
	}
	return reader.index.TableByName(databaseID, selector)
}

func (reader *IndexedReader) Snapshot(ctx context.Context) ([]catalog.Database, error) {
	if reader == nil || reader.repository == nil || reader.index == nil {
		return nil, fmt.Errorf("%w: indexed Catalog reader", ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	enumeration, ok := reader.index.(EnumerationIndex)
	if !ok {
		return nil, fmt.Errorf("%w: Catalog enumeration index", ErrInvalid)
	}
	databaseLocators, err := enumeration.Databases()
	if err != nil {
		return nil, err
	}
	databaseRecords := make([]databaseRecord, 0, len(databaseLocators))
	for _, locator := range databaseLocators {
		if locator.Kind != catalogindex.KindDatabase {
			return nil, fmt.Errorf("%w: Database enumeration locator", ErrCorrupt)
		}
		database, err := reader.repository.readDatabaseRevision(locator.ID, locator.SchemaRevision)
		if err != nil {
			return nil, err
		}
		if database.value.ID != locator.ID {
			return nil, fmt.Errorf("%w: Database enumeration body", ErrCorrupt)
		}
		tableLocators, err := enumeration.TablesForDatabase(locator.ID)
		if err != nil {
			return nil, err
		}
		for _, tableLocator := range tableLocators {
			if tableLocator.Kind != catalogindex.KindTable || tableLocator.DatabaseID != locator.ID {
				return nil, fmt.Errorf("%w: Table enumeration locator", ErrCorrupt)
			}
			table, err := reader.repository.readTableRevision(tableLocator.ID, tableLocator.SchemaRevision)
			if err != nil {
				return nil, err
			}
			if table.value.ID != tableLocator.ID || table.value.DatabaseID != locator.ID {
				return nil, fmt.Errorf("%w: Table enumeration body", ErrCorrupt)
			}
			columnLocators, err := reader.index.ColumnsForTable(tableLocator.ID)
			if err != nil {
				return nil, err
			}
			for _, columnLocator := range columnLocators {
				column, err := reader.repository.readColumnRevision(columnLocator.ID, columnLocator.SchemaRevision)
				if err != nil {
					return nil, err
				}
				if column.tableID != tableLocator.ID {
					return nil, fmt.Errorf("%w: Column enumeration body", ErrCorrupt)
				}
				table.columns = append(table.columns, column)
			}
			sort.Slice(table.columns, func(left, right int) bool { return table.columns[left].order < table.columns[right].order })
			for _, column := range table.columns {
				value := column.value
				if value.Aliases == nil {
					value.Aliases = []string{}
				}
				table.value.Columns = append(table.value.Columns, value)
			}
			database.tables = append(database.tables, &table)
		}
		sort.Slice(database.tables, func(left, right int) bool { return database.tables[left].order < database.tables[right].order })
		for _, table := range database.tables {
			value := table.value
			if value.Aliases == nil {
				value.Aliases = []string{}
			}
			database.value.Tables = append(database.value.Tables, value)
		}
		if database.value.Aliases == nil {
			database.value.Aliases = []string{}
		}
		databaseRecords = append(databaseRecords, database)
	}
	sort.Slice(databaseRecords, func(left, right int) bool { return databaseRecords[left].order < databaseRecords[right].order })
	result := make([]catalog.Database, 0, len(databaseRecords))
	for _, database := range databaseRecords {
		result = append(result, database.value)
	}
	return result, nil
}

func (reader *IndexedReader) ShowDatabases(ctx context.Context) ([]catalog.Database, error) {
	databases, err := reader.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	sort.Slice(databases, func(left, right int) bool { return canonical(databases[left].Name) < canonical(databases[right].Name) })
	return databases, nil
}

func (reader *IndexedReader) DescribeDatabase(ctx context.Context, name string) (catalog.Database, error) {
	databases, err := reader.Snapshot(ctx)
	if err != nil {
		return catalog.Database{}, err
	}
	database, ok := findDatabase(databases, name)
	if !ok {
		return catalog.Database{}, catalogFailure(catalog.CodeNotFound, "database", name, "")
	}
	return *database, nil
}

func (reader *IndexedReader) ShowTables(ctx context.Context, databaseName string) ([]catalog.Table, error) {
	database, err := reader.DescribeDatabase(ctx, databaseName)
	if err != nil {
		return nil, err
	}
	tables := append([]catalog.Table(nil), database.Tables...)
	sort.Slice(tables, func(left, right int) bool { return canonical(tables[left].Name) < canonical(tables[right].Name) })
	return tables, nil
}

func matchesName(requested, current string, aliases []string) bool {
	want := strings.ToLower(strings.TrimSpace(requested))
	if want == strings.ToLower(strings.TrimSpace(current)) {
		return true
	}
	for _, alias := range aliases {
		if want == strings.ToLower(strings.TrimSpace(alias)) {
			return true
		}
	}
	return false
}

func matchesSelector(requested, id, current string, aliases []string) bool {
	return requested == id || matchesName(requested, current, aliases)
}
