package catalog_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/catalog"
	nativekvstore "github.com/HW-Yue/Memora/internal/store/nativekv"
)

func TestCatalogPersistsSelfDescribingSchemaAndStableIDs(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "catalog.db")
	firstStore, err := nativekvstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ids := &idSource{values: []string{"database-id", "table-id"}}
	service := catalog.New(firstStore, catalog.Options{
		IDs:   ids,
		Clock: fixedClock{value: time.Date(2026, 7, 30, 1, 2, 3, 0, time.UTC)},
	})

	database, err := service.CreateDatabase(ctx, catalog.DatabaseDefinition{
		Name: "Projects", Purpose: "Store active project knowledge", Scope: "Project decisions and work",
		AntiScope: "Personal journals",
	})
	if err != nil {
		t.Fatalf("CreateDatabase() error = %v", err)
	}
	if database.ID != "db_database-id" || database.SchemaVersion != 1 {
		t.Fatalf("database = %#v", database)
	}

	table, err := service.CreateTable(ctx, "projects", catalog.TableDefinition{
		Name: "Notes", Purpose: "Keep durable project notes", RowSemantics: "One reviewed project note",
		AntiScope: "Raw source documents",
	})
	if err != nil {
		t.Fatalf("CreateTable() error = %v", err)
	}
	if table.ID != "tbl_table-id" || table.SchemaVersion != 1 {
		t.Fatalf("table = %#v", table)
	}
	tableID := table.ID
	if _, err := service.RenameTable(ctx, "projects", "notes", "Knowledge"); err != nil {
		t.Fatalf("RenameTable() error = %v", err)
	}
	if _, err := service.RenameDatabase(ctx, "projects", "Work"); err != nil {
		t.Fatalf("RenameDatabase() error = %v", err)
	}
	if err := firstStore.Close(); err != nil {
		t.Fatal(err)
	}

	secondStore, err := nativekvstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.Close()
	reopened := catalog.New(secondStore, catalog.Options{})

	gotDatabase, err := reopened.DescribeDatabase(ctx, "projects")
	if err != nil {
		t.Fatalf("DescribeDatabase(alias) error = %v", err)
	}
	if gotDatabase.ID != database.ID || gotDatabase.Name != "Work" || gotDatabase.SchemaVersion != 4 {
		t.Fatalf("reopened database = %#v", gotDatabase)
	}
	gotDatabaseByID, err := reopened.DescribeDatabase(ctx, database.ID)
	if err != nil || gotDatabaseByID.Name != "Work" {
		t.Fatalf("DescribeDatabase(ID) = %#v, %v", gotDatabaseByID, err)
	}
	gotTable, err := reopened.DescribeTable(ctx, "work", "notes")
	if err != nil {
		t.Fatalf("DescribeTable(alias) error = %v", err)
	}
	if gotTable.ID != tableID || gotTable.Name != "Knowledge" || gotTable.SchemaVersion != 2 {
		t.Fatalf("reopened table = %#v", gotTable)
	}

	databases, err := reopened.ShowDatabases(ctx)
	if err != nil || len(databases) != 1 || databases[0].Purpose == "" || databases[0].Scope == "" {
		t.Fatalf("ShowDatabases() = %#v, %v", databases, err)
	}
	tables, err := reopened.ShowTables(ctx, "work")
	if err != nil || len(tables) != 1 || tables[0].RowSemantics == "" {
		t.Fatalf("ShowTables() = %#v, %v", tables, err)
	}
}

func TestCatalogRejectsMissingSemanticsAndNameCollisions(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, err := nativekvstore.Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	service := catalog.New(databaseStore, catalog.Options{
		IDs:   &idSource{values: []string{"db-1", "db-2", "table-1"}},
		Clock: fixedClock{value: time.Unix(1, 0).UTC()},
	})

	_, err = service.CreateDatabase(ctx, catalog.DatabaseDefinition{Name: "work", Scope: "projects"})
	assertCode(t, err, catalog.CodeValidation)
	_, err = service.CreateDatabase(ctx, catalog.DatabaseDefinition{Name: "work", Purpose: "projects"})
	assertCode(t, err, catalog.CodeValidation)
	if _, err := service.CreateDatabase(ctx, catalog.DatabaseDefinition{Name: "Work", Purpose: "Projects", Scope: "Project data"}); err != nil {
		t.Fatal(err)
	}
	_, err = service.CreateDatabase(ctx, catalog.DatabaseDefinition{Name: "work", Purpose: "Duplicate", Scope: "Anything"})
	assertCode(t, err, catalog.CodeAlreadyExists)
	if _, err := service.RenameDatabase(ctx, "work", "Projects"); err != nil {
		t.Fatal(err)
	}
	_, err = service.CreateDatabase(ctx, catalog.DatabaseDefinition{Name: "WORK", Purpose: "Alias collision", Scope: "Anything"})
	assertCode(t, err, catalog.CodeAlreadyExists)

	_, err = service.CreateTable(ctx, "projects", catalog.TableDefinition{Name: "notes", RowSemantics: "one note"})
	assertCode(t, err, catalog.CodeValidation)
	_, err = service.CreateTable(ctx, "projects", catalog.TableDefinition{Name: "notes", Purpose: "notes"})
	assertCode(t, err, catalog.CodeValidation)
	if _, err := service.CreateTable(ctx, "projects", catalog.TableDefinition{
		Name: "Notes", Purpose: "Notes", RowSemantics: "One note",
	}); err != nil {
		t.Fatal(err)
	}
	_, err = service.CreateTable(ctx, "projects", catalog.TableDefinition{
		Name: "notes", Purpose: "Duplicate", RowSemantics: "One duplicate",
	})
	assertCode(t, err, catalog.CodeAlreadyExists)
	_, err = service.DescribeDatabase(ctx, "missing")
	assertCode(t, err, catalog.CodeNotFound)
}

func TestCatalogListsObjectsDeterministically(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, err := nativekvstore.Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	service := catalog.New(databaseStore, catalog.Options{
		IDs: &idSource{values: []string{
			"db-z", "db-a", "table-z", "table-a",
		}},
	})
	for _, definition := range []catalog.DatabaseDefinition{
		{Name: "Zulu", Purpose: "Zulu data", Scope: "Zulu scope"},
		{Name: "alpha", Purpose: "Alpha data", Scope: "Alpha scope"},
	} {
		if _, err := service.CreateDatabase(ctx, definition); err != nil {
			t.Fatal(err)
		}
	}
	for _, definition := range []catalog.TableDefinition{
		{Name: "Zulu", Purpose: "Zulu rows", RowSemantics: "One Zulu"},
		{Name: "alpha", Purpose: "Alpha rows", RowSemantics: "One Alpha"},
	} {
		if _, err := service.CreateTable(ctx, "alpha", definition); err != nil {
			t.Fatal(err)
		}
	}
	databases, err := service.ShowDatabases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if databases[0].Name != "alpha" || databases[1].Name != "Zulu" {
		t.Fatalf("database order = %#v", databases)
	}
	tables, err := service.ShowTables(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if tables[0].Name != "alpha" || tables[1].Name != "Zulu" {
		t.Fatalf("table order = %#v", tables)
	}
}

func assertCode(t *testing.T, err error, want catalog.Code) {
	t.Helper()
	var catalogError *catalog.Error
	if !errors.As(err, &catalogError) || catalogError.Code != want {
		t.Fatalf("error = %v, want catalog code %q", err, want)
	}
}

type idSource struct {
	values []string
}

func (source *idSource) Next() (string, error) {
	value := source.values[0]
	source.values = source.values[1:]
	return value, nil
}

type fixedClock struct {
	value time.Time
}

func (clock fixedClock) Now() time.Time {
	return clock.value
}
