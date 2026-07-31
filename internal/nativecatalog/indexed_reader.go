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
	DatabaseByName(string) (catalogindex.Locator, error)
	TableByName(string, string) (catalogindex.Locator, error)
	ColumnsForTable(string) ([]catalogindex.Locator, error)
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
	databaseLocator, err := reader.index.DatabaseByName(databaseName)
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
		!matchesName(databaseName, database.value.Name, database.value.Aliases) {
		return catalog.Table{}, fmt.Errorf("%w: Database locator and record disagree", ErrCorrupt)
	}

	tableLocator, err := reader.index.TableByName(databaseLocator.ID, tableName)
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
		tableRecord.value.DatabaseID != databaseLocator.ID ||
		!matchesName(tableName, tableRecord.value.Name, tableRecord.value.Aliases) {
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
