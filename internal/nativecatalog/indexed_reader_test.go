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
	"github.com/HW-Yue/Memora/internal/store/objectindex"
	"github.com/HW-Yue/Memora/internal/store/page"
	"github.com/HW-Yue/Memora/internal/store/treecommit"
	"github.com/HW-Yue/Memora/internal/store/wal"
)

func TestIndexedReaderDescribesAliasedTableWithStoredColumnOrder(t *testing.T) {
	database, closeFile := indexedCatalogFixture(t)
	defer closeFile()
	lookup := indexedCatalogLookup(database)
	reader, err := NewIndexedReader(lookup, indexedCatalogObjects(t, []catalog.Database{database}))
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

func TestIndexedReaderDescribesTableByStableIDs(t *testing.T) {
	database, closeFile := indexedCatalogFixture(t)
	defer closeFile()
	lookup := indexedCatalogLookup(database)
	lookup.databaseNameErr = errors.New("DatabaseByName must not resolve a stable ID")
	lookup.tableNameErr = errors.New("TableByName must not resolve a stable ID")
	reader, err := NewIndexedReader(lookup, indexedCatalogObjects(t, []catalog.Database{database}))
	if err != nil {
		t.Fatal(err)
	}

	table, err := reader.DescribeTable(context.Background(), database.ID, database.Tables[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if table.ID != database.Tables[0].ID || table.DatabaseID != database.ID || len(table.Columns) != 2 {
		t.Fatalf("DescribeTable() = %#v", table)
	}
}

func TestIndexedReaderRejectsStableTableIDFromAnotherDatabase(t *testing.T) {
	database, closeFile := indexedCatalogFixture(t)
	defer closeFile()
	lookup := indexedCatalogLookup(database)
	lookup.table.DatabaseID = "db_other"
	reader, err := NewIndexedReader(lookup, indexedCatalogObjects(t, []catalog.Database{database}))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := reader.DescribeTable(context.Background(), database.ID, database.Tables[0].ID); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("cross-Database stable Table error = %v", err)
	}
}

func TestIndexedReaderRejectsLocatorBodyMismatchAndMapsNotFound(t *testing.T) {
	database, closeFile := indexedCatalogFixture(t)
	defer closeFile()
	lookup := indexedCatalogLookup(database)
	lookup.table.SchemaRevision++
	reader, err := NewIndexedReader(lookup, indexedCatalogObjects(t, []catalog.Database{database}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.DescribeTable(context.Background(), "work", "notes"); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("schema mismatch error = %v", err)
	}

	lookup = indexedCatalogLookup(database)
	lookup.databaseErr = catalogindex.ErrNotFound
	reader, _ = NewIndexedReader(lookup, indexedCatalogObjects(t, []catalog.Database{database}))
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
	databaseNameErr error
	tableNameErr    error
}

func (lookup *fakeCatalogLookup) DatabaseByID(string) (catalogindex.Locator, error) {
	return lookup.database, lookup.databaseErr
}

func (lookup *fakeCatalogLookup) DatabaseByName(string) (catalogindex.Locator, error) {
	if lookup.databaseNameErr != nil {
		return catalogindex.Locator{}, lookup.databaseNameErr
	}
	return lookup.database, lookup.databaseErr
}

func (lookup *fakeCatalogLookup) TableByID(string) (catalogindex.Locator, error) {
	return lookup.table, nil
}

func (lookup *fakeCatalogLookup) TableByName(string, string) (catalogindex.Locator, error) {
	return lookup.table, lookup.tableNameErr
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

// treeObjects is an ObjectSource over a real objects Tree, seeded the way a
// generation build seeds one. The bodies under test are the ones a Page file
// actually holds, not a stand-in.
type treeObjects struct{ index *objectindex.Index }

func (source treeObjects) CatalogObjects() *objectindex.Index { return source.index }

func indexedCatalogObjects(t *testing.T, databases []catalog.Database) ObjectSource {
	t.Helper()
	directory := t.TempDir()
	const spaceID = uint64(0x4d454d4f424a)
	set, err := wal.CreateSegmentSet(filepath.Join(directory, "wal"), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = set.Close() })
	manager, err := page.Create(filepath.Join(directory, "objects.pages"), spaceID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	runtime, _, err := treecommit.OpenRuntime(set, manager, treecommit.RuntimeConfig{
		SpaceID: spaceID, Capacity: 128, OldFrames: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	index, err := objectindex.Open(runtime)
	if err != nil {
		t.Fatal(err)
	}
	records, err := ObjectRecords(databases)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := index.Bootstrap(1, records); err != nil {
		t.Fatal(err)
	}
	return treeObjects{index: index}
}

// indexedCatalogFixture writes the Catalog to a record log, then hands back only
// the Catalog itself. The reader under test has no handle on that file — the
// bodies it reads come from the objects Tree — so the file exists here to prove
// the encodings agree, not to be read from.
func indexedCatalogFixture(t *testing.T) (catalog.Database, func()) {
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
	return database, func() { _ = file.Close() }
}
