package nativecatalog

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/store/catalogindex"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

func TestIndexedReaderDescribesAliasedTableWithStoredColumnOrder(t *testing.T) {
	repository, database, closeFile := indexedCatalogFixture(t)
	defer closeFile()
	lookup := indexedCatalogLookup(database)
	reader, err := NewIndexedReader(repository, lookup)
	if err != nil {
		t.Fatal(err)
	}

	table, err := reader.DescribeTable(context.Background(), "工作库", "知识")
	if err != nil {
		t.Fatal(err)
	}
	if table.ID != "tbl_notes" || table.DatabaseID != "db_work" || len(table.Columns) != 2 ||
		table.Columns[0].ID != "col_title" || table.Columns[1].ID != "col_body" {
		t.Fatalf("DescribeTable() = %#v", table)
	}
}

func TestIndexedReaderRejectsLocatorBodyMismatchAndMapsNotFound(t *testing.T) {
	repository, database, closeFile := indexedCatalogFixture(t)
	defer closeFile()
	lookup := indexedCatalogLookup(database)
	lookup.table.SchemaRevision++
	reader, err := NewIndexedReader(repository, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.DescribeTable(context.Background(), "work", "notes"); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("schema mismatch error = %v", err)
	}

	lookup = indexedCatalogLookup(database)
	lookup.databaseErr = catalogindex.ErrNotFound
	reader, _ = NewIndexedReader(repository, lookup)
	_, err = reader.DescribeTable(context.Background(), "missing", "notes")
	var catalogErr *catalog.Error
	if !errors.As(err, &catalogErr) || catalogErr.Code != catalog.CodeNotFound {
		t.Fatalf("not-found error = %v", err)
	}
}

type fakeCatalogLookup struct {
	database, table catalogindex.Locator
	columns         []catalogindex.Locator
	databaseErr     error
}

func (lookup *fakeCatalogLookup) DatabaseByName(string) (catalogindex.Locator, error) {
	return lookup.database, lookup.databaseErr
}

func (lookup *fakeCatalogLookup) TableByName(string, string) (catalogindex.Locator, error) {
	return lookup.table, nil
}

func (lookup *fakeCatalogLookup) ColumnsForTable(string) ([]catalogindex.Locator, error) {
	return append([]catalogindex.Locator(nil), lookup.columns...), nil
}

func indexedCatalogLookup(database catalog.Database) *fakeCatalogLookup {
	table := database.Tables[0]
	columns := make([]catalogindex.Locator, 0, len(table.Columns))
	for _, column := range table.Columns {
		columns = append(columns, catalogindex.Locator{
			Kind: catalogindex.KindColumn, ID: column.ID, DatabaseID: database.ID,
			TableID: table.ID, SchemaRevision: column.SchemaVersion,
		})
	}
	// Deliberately reverse the index order. Native record order remains authoritative.
	columns[0], columns[1] = columns[1], columns[0]
	return &fakeCatalogLookup{
		database: catalogindex.Locator{
			Kind: catalogindex.KindDatabase, ID: database.ID, SchemaRevision: database.SchemaVersion,
		},
		table: catalogindex.Locator{
			Kind: catalogindex.KindTable, ID: table.ID, DatabaseID: database.ID,
			SchemaRevision: table.SchemaVersion,
		},
		columns: columns,
	}
}

func indexedCatalogFixture(t *testing.T) (*Repository, catalog.Database, func()) {
	t.Helper()
	file, err := nativestore.Create(filepath.Join(t.TempDir(), "database.memora"), nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	database := catalog.Database{
		ID: "db_work", Name: "work", Aliases: []string{"工作库"}, Purpose: "Work",
		Scope: "Projects", SchemaVersion: 1, CreatedAt: now, UpdatedAt: now,
		Tables: []catalog.Table{{
			ID: "tbl_notes", DatabaseID: "db_work", Name: "notes", Aliases: []string{"知识"},
			Purpose: "Notes", RowSemantics: "One note", SchemaVersion: 1,
			CreatedAt: now, UpdatedAt: now,
			Columns: []catalog.Column{
				{ID: "col_title", Name: "title", Aliases: []string{"标题"}, Type: "TEXT", MaxCharacters: 100, Purpose: "Title", SchemaVersion: 1, CreatedAt: now, UpdatedAt: now},
				{ID: "col_body", Name: "body", Aliases: []string{}, Type: "TEXT", MaxCharacters: 1000, Nullable: true, Purpose: "Body", SchemaVersion: 1, CreatedAt: now, UpdatedAt: now},
			},
		}},
	}
	repository := New(file)
	if err := repository.Write([]catalog.Database{database}); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	return repository, database, func() { _ = file.Close() }
}
