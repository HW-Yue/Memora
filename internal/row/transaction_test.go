package row_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/row"
	nativekvstore "github.com/HW-Yue/Memora/internal/store/nativekv"
)

func TestTransactionRollsBackMultipleRowWrites(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, err := nativekvstore.Open(filepath.Join(t.TempDir(), "database.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	dictionary := catalog.New(databaseStore, catalog.Options{
		IDs: &idSource{values: []string{"database", "table", "title", "body"}},
	})
	createSchema(t, ctx, dictionary)
	service := row.New(databaseStore, dictionary, row.Options{
		IDs: &idSource{values: []string{"first", "second"}},
	})

	transaction, err := service.BeginTransaction(ctx)
	if err != nil {
		t.Fatalf("BeginTransaction() error = %v", err)
	}
	first, err := transaction.Insert(ctx, "work", "notes", map[string]any{
		"title": "first",
	}, row.WriteOptions{ExpectedSchemaVersion: 1})
	if err != nil {
		t.Fatalf("transaction Insert(first) error = %v", err)
	}
	if _, err := transaction.Insert(ctx, "work", "notes", map[string]any{
		"title": "second",
	}, row.WriteOptions{ExpectedSchemaVersion: 1}); err != nil {
		t.Fatalf("transaction Insert(second) error = %v", err)
	}
	visible, err := transaction.Get(ctx, "work", "notes", first.ID)
	if err != nil || visible.Values["title"] != "first" {
		t.Fatalf("transaction Get() = %#v, %v", visible, err)
	}
	if err := transaction.Rollback(); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}

	rows, err := service.List(ctx, "work", "notes", 100)
	if err != nil || len(rows) != 0 {
		t.Fatalf("rows after rollback = %#v, %v", rows, err)
	}
}

func TestTransactionCommitsRevisionedChangesAtomically(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, err := nativekvstore.Open(filepath.Join(t.TempDir(), "database.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	dictionary := catalog.New(databaseStore, catalog.Options{
		IDs: &idSource{values: []string{"database", "table", "title", "body"}},
	})
	createSchema(t, ctx, dictionary)
	service := row.New(databaseStore, dictionary, row.Options{
		IDs: &idSource{values: []string{"note"}},
	})
	inserted, err := service.Insert(ctx, "work", "notes", map[string]any{
		"title": "before",
	}, row.WriteOptions{ExpectedSchemaVersion: 1})
	if err != nil {
		t.Fatal(err)
	}

	transaction, err := service.BeginTransaction(ctx)
	if err != nil {
		t.Fatalf("BeginTransaction() error = %v", err)
	}
	table, err := transaction.DescribeTable(ctx, "work", "notes")
	if err != nil || table.ID != inserted.TableID {
		t.Fatalf("DescribeTable() = %#v, %v", table, err)
	}
	updated, err := transaction.Update(ctx, "work", "notes", inserted.ID, map[string]any{
		"title": "after",
	}, row.WriteOptions{ExpectedSchemaVersion: 1, ExpectedRevision: 1})
	if err != nil || updated.Revision != 2 {
		t.Fatalf("transaction Update() = %#v, %v", updated, err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	persisted, err := service.Get(ctx, "work", "notes", inserted.ID)
	if err != nil || persisted.Revision != 2 || persisted.Values["title"] != "after" {
		t.Fatalf("persisted row = %#v, %v", persisted, err)
	}
}
