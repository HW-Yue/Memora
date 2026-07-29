package row_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/row"
	sqlitestore "github.com/HW-Yue/Memora/internal/store/sqlite"
)

func TestRowWritesAtomicallyReplaceAndInvalidateAgentTerms(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, err := sqlitestore.Open(filepath.Join(t.TempDir(), "database.db"))
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
		"title": "Architecture", "body": "Database internals",
	}, row.WriteOptions{
		ExpectedSchemaVersion: 1,
		IndexTerms:            []string{"from title", "from body", "FROM BODY"},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertAgentLookup(t, ctx, service, inserted.DatabaseID, "from body", inserted.ID, 1)

	updated, err := service.Update(ctx, "work", "notes", inserted.ID, map[string]any{
		"body": "New content",
	}, row.WriteOptions{
		ExpectedSchemaVersion: 1, ExpectedRevision: 1,
		IndexTerms: []string{"new content"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if matches, err := service.LookupAgentTerm(ctx, inserted.DatabaseID, "from body", 10); err != nil || len(matches) != 0 {
		t.Fatalf("old term after UPDATE = %#v, %v", matches, err)
	}
	assertAgentLookup(t, ctx, service, inserted.DatabaseID, "new content", inserted.ID, 2)

	tx, err := service.BeginTransaction(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Update(ctx, "work", "notes", inserted.ID, map[string]any{
		"title": "rolled back",
	}, row.WriteOptions{
		ExpectedSchemaVersion: 1, ExpectedRevision: 2,
		IndexTerms: []string{"rolled back term"},
	}); err != nil {
		t.Fatal(err)
	}
	if matches, err := tx.LookupAgentTerm(ctx, inserted.DatabaseID, "rolled back term", 10); err != nil || len(matches) != 1 {
		t.Fatalf("transaction read-own-index-write = %#v, %v", matches, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	assertAgentLookup(t, ctx, service, inserted.DatabaseID, "new content", inserted.ID, 2)

	deleted, err := service.Delete(ctx, "work", "notes", inserted.ID, row.WriteOptions{
		ExpectedSchemaVersion: 1, ExpectedRevision: updated.Revision,
	})
	if err != nil || deleted.Revision != 3 {
		t.Fatalf("Delete() = %#v, %v", deleted, err)
	}
	if matches, err := service.LookupAgentTerm(ctx, inserted.DatabaseID, "new content", 10); err != nil || len(matches) != 0 {
		t.Fatalf("term after DELETE = %#v, %v", matches, err)
	}
}

func TestRowInsertCanCommitExplicitEmptyAgentTermSnapshot(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, err := sqlitestore.Open(filepath.Join(t.TempDir(), "database.db"))
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
	if _, err := service.Insert(ctx, "work", "notes", map[string]any{
		"title": "No useful terms",
	}, row.WriteOptions{
		ExpectedSchemaVersion: 1, IndexTerms: []string{},
	}); err != nil {
		t.Fatal(err)
	}
}

func assertAgentLookup(
	t *testing.T,
	ctx context.Context,
	service *row.Service,
	databaseID, term, rowID string,
	revision uint64,
) {
	t.Helper()
	matches, err := service.LookupAgentTerm(ctx, databaseID, term, 10)
	if err != nil || len(matches) != 1 || matches[0].RowID != rowID ||
		matches[0].Revision != revision {
		t.Fatalf("LookupAgentTerm(%q) = %#v, %v", term, matches, err)
	}
}
