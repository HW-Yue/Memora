package row_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/row"
	nativekvstore "github.com/HW-Yue/Memora/internal/store/nativekv"
)

func TestRowWritesAutomaticallyMaintainMechanicalTextIndex(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, err := nativekvstore.Open(filepath.Join(t.TempDir(), "database.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	dictionary := catalog.New(databaseStore, catalog.Options{
		IDs: &idSource{values: []string{"database", "table", "title", "count"}},
	})
	if _, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{
		Name: "work", Purpose: "Work", Scope: "Work",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := dictionary.CreateTable(ctx, "work", catalog.TableDefinition{
		Name: "notes", Purpose: "Notes", Scope: "Notes", RowSemantics: "One note",
		Columns: []catalog.ColumnDefinition{
			{Name: "title", Type: "TEXT(100)", Purpose: "Title"},
			{Name: "count", Type: "INTEGER", Purpose: "Count"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	service := row.New(databaseStore, dictionary, row.Options{
		IDs: &idSource{values: []string{"note"}},
	})
	inserted, err := service.Insert(ctx, "work", "notes", map[string]any{
		"title": "Memora 数据库", "count": 123,
	}, row.WriteOptions{ExpectedSchemaVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	assertMechanicalLookup(t, ctx, service, inserted.DatabaseID, "memora", inserted.ID, 1)
	assertMechanicalLookup(t, ctx, service, inserted.DatabaseID, "数据", inserted.ID, 1)
	if err := service.RebuildMechanicalIndex(ctx, "work", "notes", inserted.ID); err != nil {
		t.Fatalf("RebuildMechanicalIndex() error = %v", err)
	}
	assertMechanicalLookup(t, ctx, service, inserted.DatabaseID, "memora", inserted.ID, 1)
	if matches, err := service.LookupMechanicalTerm(ctx, inserted.DatabaseID, "123", 10); err != nil || len(matches) != 0 {
		t.Fatalf("non-text field lookup = %#v, %v", matches, err)
	}

	updated, err := service.Update(ctx, "work", "notes", inserted.ID, map[string]any{
		"title": "Updated architecture",
	}, row.WriteOptions{ExpectedSchemaVersion: 1, ExpectedRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	if matches, err := service.LookupMechanicalTerm(ctx, inserted.DatabaseID, "memora", 10); err != nil || len(matches) != 0 {
		t.Fatalf("old mechanical term = %#v, %v", matches, err)
	}
	assertMechanicalLookup(t, ctx, service, inserted.DatabaseID, "updated", inserted.ID, 2)

	tx, err := service.BeginTransaction(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Update(ctx, "work", "notes", inserted.ID, map[string]any{
		"title": "Rolled back wording",
	}, row.WriteOptions{ExpectedSchemaVersion: 1, ExpectedRevision: 2}); err != nil {
		t.Fatal(err)
	}
	if matches, err := tx.LookupMechanicalTerm(ctx, inserted.DatabaseID, "rolled", 10); err != nil || len(matches) != 1 {
		t.Fatalf("read-own mechanical write = %#v, %v", matches, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	assertMechanicalLookup(t, ctx, service, inserted.DatabaseID, "updated", inserted.ID, 2)

	restored, err := service.Restore(ctx, "work", "notes", inserted.ID, 1, row.WriteOptions{
		ExpectedSchemaVersion: 1, ExpectedRevision: updated.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertMechanicalLookup(t, ctx, service, inserted.DatabaseID, "memora", inserted.ID, 3)
	if _, err := service.Delete(ctx, "work", "notes", inserted.ID, row.WriteOptions{
		ExpectedSchemaVersion: 1, ExpectedRevision: restored.Revision,
	}); err != nil {
		t.Fatal(err)
	}
	if matches, err := service.LookupMechanicalTerm(ctx, inserted.DatabaseID, "memora", 10); err != nil || len(matches) != 0 {
		t.Fatalf("mechanical term after DELETE = %#v, %v", matches, err)
	}
}

func assertMechanicalLookup(
	t *testing.T,
	ctx context.Context,
	service *row.Service,
	databaseID, term, rowID string,
	revision uint64,
) {
	t.Helper()
	matches, err := service.LookupMechanicalTerm(ctx, databaseID, term, 10)
	if err != nil || len(matches) != 1 || matches[0].RowID != rowID ||
		matches[0].Revision != revision {
		t.Fatalf("LookupMechanicalTerm(%q) = %#v, %v", term, matches, err)
	}
}
