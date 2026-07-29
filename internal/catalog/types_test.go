package catalog_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/store"
	sqlitestore "github.com/HW-Yue/Memora/internal/store/sqlite"
)

func TestCatalogPersistsCanonicalTypesAndTextBudgets(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "catalog.db")
	firstStore, err := sqlitestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	service := catalog.New(firstStore, catalog.Options{
		IDs: &idSource{values: []string{"database", "table", "title", "body", "count", "done", "at", "parent"}},
	})
	if _, err := service.CreateDatabase(ctx, catalog.DatabaseDefinition{Name: "work", Purpose: "Work", Scope: "Projects"}); err != nil {
		t.Fatal(err)
	}
	table, err := service.CreateTable(ctx, "work", catalog.TableDefinition{
		Name: "notes", Purpose: "Notes", RowSemantics: "One note",
		Columns: []catalog.ColumnDefinition{
			{Name: "title", Type: "text", Purpose: "Title"},
			{Name: "body", Type: "TEXT(8)", Purpose: "Body"},
			{Name: "count", Type: "INTEGER", Purpose: "Count"},
			{Name: "done", Type: "BOOLEAN", Purpose: "Completion"},
			{Name: "at", Type: "TIMESTAMP", Purpose: "Event time"},
			{Name: "parent", Type: "RELATION_ID", Purpose: "Parent row"},
		},
	})
	if err != nil {
		t.Fatalf("CreateTable() error = %v", err)
	}
	if table.Columns[0].Type != "TEXT" || table.Columns[0].MaxCharacters != 1200 {
		t.Fatalf("default text column = %#v", table.Columns[0])
	}
	if table.Columns[1].Type != "TEXT" || table.Columns[1].MaxCharacters != 8 {
		t.Fatalf("configured text column = %#v", table.Columns[1])
	}
	if _, err := table.Columns[1].Validate("八个字符刚刚好呀"); err != nil {
		t.Fatalf("Validate(configured text) error = %v", err)
	}
	_, err = table.Columns[1].Validate("九个字符会失败呀呀")
	assertStableCode(t, err, result.CodeValueTooLong)

	if err := firstStore.Close(); err != nil {
		t.Fatal(err)
	}
	secondStore, err := sqlitestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.Close()
	reopened := catalog.New(secondStore, catalog.Options{})
	body, err := reopened.DescribeColumn(ctx, "work", "notes", "body")
	if err != nil {
		t.Fatal(err)
	}
	if body.Type != "TEXT" || body.MaxCharacters != 8 {
		t.Fatalf("reopened body = %#v", body)
	}
}

func TestCatalogRejectsUnknownAndMalformedTypesBeforeWriting(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, err := sqlitestore.Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	service := catalog.New(databaseStore, catalog.Options{
		IDs: &idSource{values: []string{"database", "table"}},
	})
	if _, err := service.CreateDatabase(ctx, catalog.DatabaseDefinition{Name: "work", Purpose: "Work", Scope: "Projects"}); err != nil {
		t.Fatal(err)
	}
	for _, typeName := range []string{"FLOAT", "TEXT(0)", "INTEGER(3)"} {
		_, err := service.CreateTable(ctx, "work", catalog.TableDefinition{
			Name: "invalid", Purpose: "Invalid", RowSemantics: "One invalid row",
			Columns: []catalog.ColumnDefinition{{Name: "value", Type: typeName, Purpose: "Value"}},
		})
		assertStableCode(t, err, result.CodeValidation)
	}
	_, err = service.CreateTable(ctx, "work", catalog.TableDefinition{
		Name: "reserved", Purpose: "Reserved", RowSemantics: "One invalid row",
		Columns: []catalog.ColumnDefinition{{Name: "row_id", Type: "TEXT", Purpose: "Collision"}},
	})
	assertStableCode(t, err, result.CodeValidation)
	tables, err := service.ShowTables(ctx, "work")
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) != 0 {
		t.Fatalf("invalid schema was written: %#v", tables)
	}
}

func TestCatalogNormalizesF13TextDeclarationsOnRead(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, err := sqlitestore.Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	tx, err := databaseStore.Begin(ctx, store.ReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`{"version":"memora.catalog/v1","databases":[{"database_id":"db_1","name":"work","aliases":[],"purpose":"Work","scope":"Projects","schema_version":2,"tables":[{"table_id":"tbl_1","name":"notes","aliases":[],"purpose":"Notes","row_semantics":"One note","schema_version":1,"columns":[{"column_id":"col_1","name":"body","aliases":[],"type":"TEXT(8)","purpose":"Body","schema_version":1}]}]}]}`)
	if err := tx.Put(ctx, "catalog", "snapshot", legacy); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	column, err := catalog.New(databaseStore, catalog.Options{}).DescribeColumn(ctx, "work", "notes", "body")
	if err != nil {
		t.Fatal(err)
	}
	if column.Type != "TEXT" || column.MaxCharacters != 8 {
		t.Fatalf("normalized legacy column = %#v", column)
	}
}

func assertStableCode(t *testing.T, err error, want result.Code) {
	t.Helper()
	var stable interface{ StableCode() string }
	if !errors.As(err, &stable) || stable.StableCode() != string(want) {
		t.Fatalf("error = %v, want stable code %q", err, want)
	}
}
