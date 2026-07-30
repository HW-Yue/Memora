package snapshot_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/relation"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/snapshot"
	"github.com/HW-Yue/Memora/internal/store"
	nativekvstore "github.com/HW-Yue/Memora/internal/store/nativekv"
)

func TestLogicalSnapshotRoundTripPreservesAuthorityAndRebuildsOnlyLogicalIndexes(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	sourceStore, err := nativekvstore.Open(filepath.Join(t.TempDir(), "source.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sourceStore.Close()
	_, _, firstID, secondID, relationID := seedDatabase(t, ctx, sourceStore)
	exported, err := snapshot.New(sourceStore).Export(ctx)
	if err != nil {
		t.Fatal(err)
	}

	targetStore, err := nativekvstore.Open(filepath.Join(t.TempDir(), "target.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer targetStore.Close()
	if err := snapshot.New(targetStore).Import(ctx, exported); err != nil {
		t.Fatal(err)
	}
	targetCatalog := catalog.New(targetStore, catalog.Options{})
	targetRows := row.New(targetStore, targetCatalog, row.Options{
		IDs: &idSource{values: []string{"after-import"}},
	})
	current, err := targetRows.Get(ctx, "work", "notes", firstID)
	if err != nil || current.Revision != 2 || current.Values["title"] != "revised" {
		t.Fatalf("current row = %#v, %v", current, err)
	}
	deleted, err := targetRows.GetIncludingDeleted(ctx, "work", "notes", secondID)
	if err != nil || deleted.State != row.StateDeleted || deleted.Revision != 2 {
		t.Fatalf("deleted row = %#v, %v", deleted, err)
	}
	firstHistory, err := targetRows.History(ctx, "work", "notes", firstID)
	if err != nil || len(firstHistory) != 2 || firstHistory[1].Values["col_title"] != "revised" {
		t.Fatalf("first history = %#v, %v", firstHistory, err)
	}
	secondHistory, err := targetRows.History(ctx, "work", "notes", secondID)
	if err != nil || len(secondHistory) != 2 || secondHistory[1].State != string(row.StateDeleted) {
		t.Fatalf("second history = %#v, %v", secondHistory, err)
	}
	currentRelation, err := targetRows.GetRelation(ctx, relationID)
	if err != nil || currentRelation.State != relation.StateDeleted || currentRelation.Revision != 2 {
		t.Fatalf("current relation = %#v, %v", currentRelation, err)
	}
	relationHistory, err := relation.New(targetStore, relation.Options{}).History(ctx, relationID)
	if err != nil || len(relationHistory) != 2 ||
		relationHistory[0].State != relation.StateLive ||
		relationHistory[1].State != relation.StateDeleted {
		t.Fatalf("relation history = %#v, %v", relationHistory, err)
	}

	reexported, err := snapshot.New(targetStore).Export(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sourceHash, err := snapshot.CanonicalHash(exported)
	if err != nil {
		t.Fatal(err)
	}
	targetHash, err := snapshot.CanonicalHash(reexported)
	if err != nil {
		t.Fatal(err)
	}
	if sourceHash != targetHash {
		t.Fatalf("round-trip hash = %s, want %s", targetHash, sourceHash)
	}
	inserted, err := targetRows.Insert(ctx, "work", "notes", map[string]any{
		"title": "after import",
	}, row.WriteOptions{ExpectedSchemaVersion: 1})
	if err != nil || inserted.CommitSequence != 7 {
		t.Fatalf("post-import insert = %#v, %v", inserted, err)
	}
}

func TestLogicalSnapshotPreservesUnknownFieldsAndMigratesV0Fixture(t *testing.T) {
	t.Parallel()

	fixture, err := os.ReadFile(filepath.Join("testdata", "logical-snapshot-v0.json"))
	if err != nil {
		t.Fatal(err)
	}
	migrated, err := snapshot.Migrate(fixture)
	if err != nil {
		t.Fatal(err)
	}
	assertJSONField(t, migrated, "version", snapshot.Version)
	assertJSONField(t, migrated, "future_v0", "preserve-me")

	ctx := context.Background()
	databaseStore, err := nativekvstore.Open(filepath.Join(t.TempDir(), "target.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	if err := snapshot.New(databaseStore).Import(ctx, migrated); err != nil {
		t.Fatal(err)
	}
	service := row.New(databaseStore, catalog.New(databaseStore, catalog.Options{}), row.Options{})
	imported, err := service.Get(ctx, "legacy", "notes", "row_legacy")
	if err != nil || imported.Values["title"] != "legacy note" {
		t.Fatalf("legacy row = %#v, %v", imported, err)
	}
	reexported, err := snapshot.New(databaseStore).Export(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertJSONField(t, reexported, "future_v0", "preserve-me")
	if !jsonPathExists(reexported, "catalog", "databases", 0, "future_catalog") {
		t.Fatal("unknown Catalog field was not preserved")
	}
	if !jsonPathExists(reexported, "rows", 0, "future_row") {
		t.Fatal("unknown Row field was not preserved")
	}
}

func TestLogicalSnapshotRejectsUnsupportedAndNonEmptyImportWithStableCodes(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, err := nativekvstore.Open(filepath.Join(t.TempDir(), "target.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	service := snapshot.New(databaseStore)
	err = service.Import(ctx, []byte(`{"version":"memora.logical-snapshot/v99"}`))
	assertCode(t, err, result.CodeValidation)

	dictionary := catalog.New(databaseStore, catalog.Options{})
	if _, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{
		Name: "occupied", Purpose: "existing", Scope: "target",
	}); err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile(filepath.Join("testdata", "logical-snapshot-v0.json"))
	if err != nil {
		t.Fatal(err)
	}
	err = service.Import(ctx, fixture)
	assertCode(t, err, result.CodeAlreadyExists)
}

func seedDatabase(
	t *testing.T,
	ctx context.Context,
	databaseStore store.Store,
) (*row.Service, string, string, string, string) {
	t.Helper()
	now := fixedClock{value: time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)}
	dictionary := catalog.New(databaseStore, catalog.Options{
		IDs:   &idSource{values: []string{"work", "notes", "title"}},
		Clock: now,
	})
	database, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{
		Name: "work", Purpose: "project knowledge", Scope: "reviewed notes",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dictionary.CreateTable(ctx, "work", catalog.TableDefinition{
		Name: "notes", Purpose: "durable notes", RowSemantics: "one reviewed note",
		Columns: []catalog.ColumnDefinition{
			{Name: "title", Type: "TEXT(2000)", Purpose: "note title"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	service := row.New(databaseStore, dictionary, row.Options{
		IDs:         &idSource{values: []string{"first", "second"}},
		RelationIDs: &idSource{values: []string{"edge"}},
		Clock:       now,
	})
	first, err := service.Insert(ctx, "work", "notes", map[string]any{
		"title": "initial",
	}, row.WriteOptions{
		ExpectedSchemaVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Insert(ctx, "work", "notes", map[string]any{
		"title": "second",
	}, row.WriteOptions{
		ExpectedSchemaVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Update(ctx, "work", "notes", first.ID, map[string]any{
		"title": "revised",
	}, row.WriteOptions{
		ExpectedSchemaVersion: 1, ExpectedRevision: 1,
		RouteLeafIDs: []string{},
	}); err != nil {
		t.Fatal(err)
	}
	createdRelation, err := service.Relate(ctx, row.RelationDefinition{
		Source:      row.RelationEndpoint{Database: "work", Table: "notes", RowID: first.ID},
		Type:        "supports",
		Target:      row.RelationEndpoint{Database: "work", Table: "notes", RowID: second.ID},
		Description: "fixture edge",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.DeleteRelation(ctx, createdRelation.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Delete(ctx, "work", "notes", second.ID, row.WriteOptions{
		ExpectedSchemaVersion: 1, ExpectedRevision: 1,
	}); err != nil {
		t.Fatal(err)
	}
	return service, database.ID, first.ID, second.ID, createdRelation.ID
}

type idSource struct {
	values []string
	index  int
}

func (source *idSource) Next() (string, error) {
	value := source.values[source.index]
	source.index++
	return value, nil
}

type fixedClock struct{ value time.Time }

func (clock fixedClock) Now() time.Time { return clock.value }

func assertCode(t *testing.T, err error, want result.Code) {
	t.Helper()
	var stable interface{ StableCode() string }
	if !errors.As(err, &stable) {
		t.Fatalf("error = %v has no stable code, want %q", err, want)
	}
	if got := stable.StableCode(); got != string(want) {
		t.Fatalf("error = %v, code = %q, want %q", err, got, want)
	}
}
