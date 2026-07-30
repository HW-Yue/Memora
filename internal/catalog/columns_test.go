package catalog_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/store"
	nativekvstore "github.com/HW-Yue/Memora/internal/store/nativekv"
)

func TestCatalogPersistsColumnsAndPropagatesSchemaVersions(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "catalog.db")
	firstStore, err := nativekvstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	service := catalog.New(firstStore, catalog.Options{
		IDs:   &idSource{values: []string{"database", "table", "body", "title", "status"}},
		Clock: fixedClock{value: time.Date(2026, 7, 30, 2, 0, 0, 0, time.UTC)},
	})
	if _, err := service.CreateDatabase(ctx, catalog.DatabaseDefinition{
		Name: "work", Purpose: "Project knowledge", Scope: "Active projects",
	}); err != nil {
		t.Fatal(err)
	}
	table, err := service.CreateTable(ctx, "work", catalog.TableDefinition{
		Name: "notes", Purpose: "Durable notes", RowSemantics: "One reviewed note",
		Columns: []catalog.ColumnDefinition{
			{Name: "Body", Type: "text", Purpose: "Complete note body"},
			{Name: "Title", Type: "TEXT", Purpose: "Human-readable title"},
		},
	})
	if err != nil {
		t.Fatalf("CreateTable() error = %v", err)
	}
	if table.SchemaVersion != 1 || len(table.Columns) != 2 {
		t.Fatalf("created table = %#v", table)
	}
	if table.Columns[0].ID != "col_body" || table.Columns[0].Type != "TEXT" || table.Columns[0].SchemaVersion != 1 {
		t.Fatalf("body column = %#v", table.Columns[0])
	}

	status, err := service.AddColumn(ctx, "work", "notes", catalog.ColumnDefinition{
		Name: "Status", Type: "text", Nullable: true, Purpose: "Current workflow state",
	})
	if err != nil {
		t.Fatalf("AddColumn() error = %v", err)
	}
	if status.ID != "col_status" || !status.Nullable {
		t.Fatalf("status column = %#v", status)
	}
	heading, err := service.RenameColumn(ctx, "work", "notes", "title", "Heading")
	if err != nil {
		t.Fatalf("RenameColumn() error = %v", err)
	}
	if heading.ID != "col_title" || heading.SchemaVersion != 2 {
		t.Fatalf("renamed column = %#v", heading)
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
	database, err := reopened.DescribeDatabase(ctx, "work")
	if err != nil {
		t.Fatal(err)
	}
	if database.SchemaVersion != 4 {
		t.Fatalf("database schema version = %d, want 4", database.SchemaVersion)
	}
	gotTable, err := reopened.DescribeTable(ctx, "work", "notes")
	if err != nil {
		t.Fatal(err)
	}
	if gotTable.SchemaVersion != 3 {
		t.Fatalf("table schema version = %d, want 3", gotTable.SchemaVersion)
	}
	gotHeading, err := reopened.DescribeColumn(ctx, "work", "notes", "title")
	if err != nil {
		t.Fatalf("DescribeColumn(alias) error = %v", err)
	}
	if gotHeading.ID != heading.ID || gotHeading.Name != "Heading" || gotHeading.Purpose == "" {
		t.Fatalf("reopened heading = %#v", gotHeading)
	}
	columns, err := reopened.ShowColumns(ctx, "work", "notes")
	if err != nil {
		t.Fatal(err)
	}
	if len(columns) != 3 || columns[0].Name != "Body" || columns[1].Name != "Heading" || columns[2].Name != "Status" {
		t.Fatalf("column order = %#v", columns)
	}
}

func TestCatalogRejectsInvalidAndDuplicateColumnsAtomically(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, err := nativekvstore.Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	service := catalog.New(databaseStore, catalog.Options{
		IDs: &idSource{values: []string{"database", "table", "body", "other"}},
	})
	if _, err := service.CreateDatabase(ctx, catalog.DatabaseDefinition{
		Name: "work", Purpose: "Project knowledge", Scope: "Active projects",
	}); err != nil {
		t.Fatal(err)
	}

	_, err = service.CreateTable(ctx, "work", catalog.TableDefinition{
		Name: "invalid", Purpose: "Invalid table", RowSemantics: "One invalid row",
		Columns: []catalog.ColumnDefinition{{Name: "body", Type: "TEXT"}},
	})
	assertCode(t, err, catalog.CodeValidation)
	_, err = service.CreateTable(ctx, "work", catalog.TableDefinition{
		Name: "duplicate", Purpose: "Duplicate table", RowSemantics: "One duplicate row",
		Columns: []catalog.ColumnDefinition{
			{Name: "Body", Type: "TEXT", Purpose: "Body"},
			{Name: "body", Type: "TEXT", Purpose: "Duplicate body"},
		},
	})
	assertCode(t, err, catalog.CodeAlreadyExists)
	tables, err := service.ShowTables(ctx, "work")
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) != 0 {
		t.Fatalf("failed table creation persisted %#v", tables)
	}

	if _, err := service.CreateTable(ctx, "work", catalog.TableDefinition{
		Name: "notes", Purpose: "Notes", RowSemantics: "One note",
		Columns: []catalog.ColumnDefinition{{Name: "Body", Type: "TEXT", Purpose: "Note body"}},
	}); err != nil {
		t.Fatal(err)
	}
	_, err = service.AddColumn(ctx, "work", "notes", catalog.ColumnDefinition{Name: "missing_type", Purpose: "Invalid"})
	assertCode(t, err, catalog.CodeValidation)
	_, err = service.AddColumn(ctx, "work", "notes", catalog.ColumnDefinition{Name: "missing_purpose", Type: "TEXT"})
	assertCode(t, err, catalog.CodeValidation)
	_, err = service.AddColumn(ctx, "work", "notes", catalog.ColumnDefinition{Name: "body", Type: "TEXT", Purpose: "Duplicate"})
	assertCode(t, err, catalog.CodeAlreadyExists)
	if _, err := service.RenameColumn(ctx, "work", "notes", "body", "Content"); err != nil {
		t.Fatal(err)
	}
	_, err = service.AddColumn(ctx, "work", "notes", catalog.ColumnDefinition{Name: "BODY", Type: "TEXT", Purpose: "Alias duplicate"})
	assertCode(t, err, catalog.CodeAlreadyExists)
	_, err = service.DescribeColumn(ctx, "work", "notes", "missing")
	assertCode(t, err, catalog.CodeNotFound)
}

func TestCatalogReadsF13aTablesWithoutColumnField(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, err := nativekvstore.Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	tx, err := databaseStore.Begin(ctx, store.ReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`{"version":"memora.catalog/v1","databases":[{"database_id":"db_1","name":"work","aliases":[],"purpose":"Work","scope":"Projects","schema_version":2,"tables":[{"table_id":"tbl_1","name":"notes","aliases":[],"purpose":"Notes","row_semantics":"One note","schema_version":1}]}]}`)
	if err := tx.Put(ctx, "catalog", "snapshot", legacy); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	service := catalog.New(databaseStore, catalog.Options{IDs: &idSource{values: []string{"body"}}})
	table, err := service.DescribeTable(ctx, "work", "notes")
	if err != nil {
		t.Fatal(err)
	}
	if table.DatabaseID != "db_1" {
		t.Fatalf("legacy table database ID = %q", table.DatabaseID)
	}
	columns, err := service.ShowColumns(ctx, "work", "notes")
	if err != nil {
		t.Fatal(err)
	}
	if columns == nil || len(columns) != 0 {
		t.Fatalf("columns = %#v, want non-nil empty list", columns)
	}
	if _, err := service.AddColumn(ctx, "work", "notes", catalog.ColumnDefinition{
		Name: "body", Type: "TEXT", Purpose: "Note body",
	}); err != nil {
		t.Fatalf("AddColumn() after F13a snapshot error = %v", err)
	}
}
