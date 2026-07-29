package row_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
	sqlitestore "github.com/HW-Yue/Memora/internal/store/sqlite"
)

func TestUpdateAndLogicalDeletePreserveIdentityAndRejectStaleRevision(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "database.db")
	firstStore, err := sqlitestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	dictionary := catalog.New(firstStore, catalog.Options{
		IDs: &idSource{values: []string{"database", "table", "title", "body"}},
	})
	createSchema(t, ctx, dictionary)
	service := row.New(firstStore, dictionary, row.Options{
		IDs: &idSource{values: []string{"note"}},
		Clock: &sequenceClock{values: []time.Time{
			time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC),
			time.Date(2026, 7, 30, 10, 1, 0, 0, time.UTC),
			time.Date(2026, 7, 30, 10, 2, 0, 0, time.UTC),
		}},
	})
	inserted, err := service.Insert(ctx, "work", "notes", map[string]any{
		"title": "Initial",
	}, row.WriteOptions{ExpectedSchemaVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dictionary.RenameColumn(ctx, "work", "notes", "title", "Heading"); err != nil {
		t.Fatal(err)
	}

	_, err = service.Update(ctx, "work", "notes", inserted.ID, map[string]any{
		"heading": "Wrong schema",
	}, row.WriteOptions{ExpectedRevision: 1, ExpectedSchemaVersion: 1})
	assertCode(t, err, result.CodeRevisionConflict)
	updated, err := service.Update(ctx, "work", "notes", inserted.ID, map[string]any{
		"title": "Revised",
	}, row.WriteOptions{ExpectedRevision: 1, ExpectedSchemaVersion: 2})
	if err != nil {
		t.Fatalf("Update(alias) error = %v", err)
	}
	if updated.ID != inserted.ID || updated.Revision != 2 || updated.SchemaVersion != 2 ||
		updated.Values["Heading"] != "Revised" || !updated.CreatedAt.Equal(inserted.CreatedAt) ||
		!updated.UpdatedAt.After(inserted.UpdatedAt) {
		t.Fatalf("updated row = %#v", updated)
	}

	for _, test := range []struct {
		changes map[string]any
		options row.WriteOptions
		code    result.Code
	}{
		{map[string]any{"heading": "stale"}, row.WriteOptions{ExpectedRevision: 1, ExpectedSchemaVersion: 2}, result.CodeRevisionConflict},
		{map[string]any{}, row.WriteOptions{ExpectedRevision: 2, ExpectedSchemaVersion: 2}, result.CodeValidation},
		{map[string]any{"body": 7}, row.WriteOptions{ExpectedRevision: 2, ExpectedSchemaVersion: 2}, result.CodeConstraint},
		{map[string]any{"title": "one", "heading": "two"}, row.WriteOptions{ExpectedRevision: 2, ExpectedSchemaVersion: 2}, result.CodeValidation},
	} {
		_, err := service.Update(ctx, "work", "notes", inserted.ID, test.changes, test.options)
		assertCode(t, err, test.code)
	}
	current, err := service.Get(ctx, "work", "notes", inserted.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Revision != 2 || current.Values["Heading"] != "Revised" {
		t.Fatalf("failed update mutated row: %#v", current)
	}

	_, err = service.Delete(ctx, "work", "notes", inserted.ID, row.WriteOptions{ExpectedSchemaVersion: 2})
	assertCode(t, err, result.CodeValidation)
	deleted, err := service.Delete(ctx, "work", "notes", inserted.ID, row.WriteOptions{
		ExpectedRevision: 2, ExpectedSchemaVersion: 2,
	})
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if deleted.ID != inserted.ID || deleted.Revision != 3 || deleted.State != row.StateDeleted ||
		deleted.Values["Heading"] != "Revised" || !deleted.UpdatedAt.After(updated.UpdatedAt) {
		t.Fatalf("deleted row = %#v", deleted)
	}
	_, err = service.Get(ctx, "work", "notes", inserted.ID)
	assertCode(t, err, result.CodeNotFound)
	_, err = service.Delete(ctx, "work", "notes", inserted.ID, row.WriteOptions{
		ExpectedRevision: 3, ExpectedSchemaVersion: 2,
	})
	assertCode(t, err, result.CodeNotFound)
	if err := firstStore.Close(); err != nil {
		t.Fatal(err)
	}

	secondStore, err := sqlitestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.Close()
	reopened := row.New(secondStore, catalog.New(secondStore, catalog.Options{}), row.Options{})
	tombstone, err := reopened.GetIncludingDeleted(ctx, "work", "notes", inserted.ID)
	if err != nil {
		t.Fatalf("GetIncludingDeleted() error = %v", err)
	}
	if tombstone.ID != inserted.ID || tombstone.Revision != 3 || tombstone.State != row.StateDeleted {
		t.Fatalf("reopened tombstone = %#v", tombstone)
	}
	rows, err := reopened.List(ctx, "work", "notes", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("deleted row remained live: %#v", rows)
	}
}

func TestConcurrentExpectedRevisionAllowsOneWinner(t *testing.T) {
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
		"title": "initial",
	}, row.WriteOptions{ExpectedSchemaVersion: 1})
	if err != nil {
		t.Fatal(err)
	}

	var wait sync.WaitGroup
	errs := make(chan error, 2)
	for _, title := range []string{"first", "second"} {
		title := title
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, updateErr := service.Update(ctx, "work", "notes", inserted.ID, map[string]any{
				"title": title,
			}, row.WriteOptions{ExpectedRevision: 1, ExpectedSchemaVersion: 1})
			errs <- updateErr
		}()
	}
	wait.Wait()
	close(errs)
	successes, conflicts := 0, 0
	for updateErr := range errs {
		if updateErr == nil {
			successes++
			continue
		}
		var stable interface{ StableCode() string }
		if errors.As(updateErr, &stable) && stable.StableCode() == string(result.CodeRevisionConflict) {
			conflicts++
			continue
		}
		t.Fatalf("unexpected update error = %v", updateErr)
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes = %d, conflicts = %d", successes, conflicts)
	}
	current, err := service.Get(ctx, "work", "notes", inserted.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Revision != 2 {
		t.Fatalf("current row = %#v", current)
	}
}

type sequenceClock struct {
	mu     sync.Mutex
	values []time.Time
}

func (clock *sequenceClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	value := clock.values[0]
	clock.values = clock.values[1:]
	return value
}
