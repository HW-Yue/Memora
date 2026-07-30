package row_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
	nativekvstore "github.com/HW-Yue/Memora/internal/store/nativekv"
)

func TestServicePersistsStableRevisionedRowsByColumnIdentity(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "database.db")
	firstStore, err := nativekvstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	dictionary := catalog.New(firstStore, catalog.Options{
		IDs: &idSource{values: []string{"database", "table", "title", "body"}},
	})
	createSchema(t, ctx, dictionary)
	service := row.New(firstStore, dictionary, row.Options{
		IDs:   &idSource{values: []string{"note"}},
		Clock: fixedClock{value: time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)},
	})

	inserted, err := service.Insert(ctx, "work", "notes", map[string]any{
		"title": "Storage design",
		"body":  "A complete semantic note",
	}, row.WriteOptions{ExpectedSchemaVersion: 1})
	if err != nil {
		t.Fatalf("Insert() error = %v", err)
	}
	if inserted.ID != "row_note" || inserted.DatabaseID != "db_database" || inserted.Revision != 1 || inserted.State != row.StateLive || inserted.SchemaVersion != 1 {
		t.Fatalf("inserted row = %#v", inserted)
	}

	if _, err := dictionary.RenameColumn(ctx, "work", "notes", "title", "Heading"); err != nil {
		t.Fatal(err)
	}
	renamed, err := service.Get(ctx, "work", "notes", inserted.ID)
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Values["Heading"] != "Storage design" {
		t.Fatalf("row did not follow stable column identity: %#v", renamed.Values)
	}
	if _, exists := renamed.Values["title"]; exists {
		t.Fatalf("row exposed deprecated column name: %#v", renamed.Values)
	}

	if err := firstStore.Close(); err != nil {
		t.Fatal(err)
	}

	secondStore, err := nativekvstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.Close()
	reopenedDictionary := catalog.New(secondStore, catalog.Options{})
	reopened := row.New(secondStore, reopenedDictionary, row.Options{})
	persisted, err := reopened.Get(ctx, "work", "notes", inserted.ID)
	if err != nil {
		t.Fatalf("Get() after reopen error = %v", err)
	}
	if persisted.ID != inserted.ID || persisted.Revision != 1 || persisted.State != row.StateLive || persisted.Values["Heading"] != "Storage design" {
		t.Fatalf("reopened row = %#v", persisted)
	}
	rows, err := reopened.List(ctx, "work", "notes", 100)
	if err != nil {
		t.Fatal(err)
	}
	if rows == nil || len(rows) != 1 || rows[0].ID != inserted.ID {
		t.Fatalf("List() = %#v, want persisted live row", rows)
	}
}

func TestServiceRejectsInvalidRowsAtomically(t *testing.T) {
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
		IDs: &idSource{values: []string{"valid"}},
	})

	for _, test := range []struct {
		values map[string]any
		code   result.Code
	}{
		{map[string]any{"body": "missing required title"}, result.CodeConstraint},
		{map[string]any{"title": "unknown", "mystery": "field"}, result.CodeValidation},
		{map[string]any{"title": 7}, result.CodeConstraint},
		{map[string]any{"title": "123456789012345678901234567890123"}, result.CodeValueTooLong},
	} {
		_, err := service.Insert(ctx, "work", "notes", test.values, row.WriteOptions{ExpectedSchemaVersion: 1})
		assertCode(t, err, test.code)
	}
	_, err = service.Insert(ctx, "work", "notes", map[string]any{
		"title": "valid",
	}, row.WriteOptions{ExpectedSchemaVersion: 9})
	assertCode(t, err, result.CodeRevisionConflict)
	rows, err := service.List(ctx, "work", "notes", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("invalid insert persisted rows: %#v", rows)
	}
	_, err = service.List(ctx, "work", "notes", 0)
	assertCode(t, err, result.CodeValidation)

	inserted, err := service.Insert(ctx, "work", "notes", map[string]any{
		"title": "valid",
	}, row.WriteOptions{ExpectedSchemaVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if inserted.Values["body"] != nil {
		t.Fatalf("missing nullable body = %#v, want nil", inserted.Values["body"])
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = service.Get(cancelled, "work", "notes", inserted.ID)
	assertCode(t, err, result.CodeCancelled)
}

func createSchema(t *testing.T, ctx context.Context, dictionary *catalog.Service) {
	t.Helper()
	if _, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{
		Name: "work", Purpose: "Work", Scope: "Projects",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := dictionary.CreateTable(ctx, "work", catalog.TableDefinition{
		Name: "notes", Purpose: "Notes", RowSemantics: "One note",
		Columns: []catalog.ColumnDefinition{
			{Name: "title", Type: "TEXT(32)", Purpose: "Title"},
			{Name: "body", Type: "TEXT", Nullable: true, Purpose: "Body"},
		},
	}); err != nil {
		t.Fatal(err)
	}
}

func assertCode(t *testing.T, err error, want result.Code) {
	t.Helper()
	var stable interface{ StableCode() string }
	if !errors.As(err, &stable) || stable.StableCode() != string(want) {
		t.Fatalf("error = %v, want stable code %q", err, want)
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
