package wikiexport_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/snapshot"
	"github.com/HW-Yue/Memora/internal/store"
	sqlitestore "github.com/HW-Yue/Memora/internal/store/sqlite"
	"github.com/HW-Yue/Memora/internal/wikiexport"
)

func TestSameSnapshotAndProfileProduceIdenticalWikiHashWithStableCrossDatabaseLinks(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, dictionary, rows := wikiFixture(t, ctx)
	defer databaseStore.Close()
	encoded, err := snapshot.New(databaseStore).Export(ctx)
	if err != nil {
		t.Fatal(err)
	}
	profile := wikiProfile()
	firstRoot, secondRoot := filepath.Join(t.TempDir(), "first"), filepath.Join(t.TempDir(), "second")
	first, err := wikiexport.ExportSnapshot(encoded, firstRoot, profile)
	if err != nil {
		t.Fatal(err)
	}
	second, err := wikiexport.ExportSnapshot(encoded, secondRoot, profile)
	if err != nil {
		t.Fatal(err)
	}
	firstHash, err := wikiexport.HashDirectory(firstRoot)
	if err != nil {
		t.Fatal(err)
	}
	secondHash, err := wikiexport.HashDirectory(secondRoot)
	if err != nil {
		t.Fatal(err)
	}
	if firstHash != secondHash || !reflect.DeepEqual(first, second) || len(first.Objects) != 2 {
		t.Fatalf("deterministic exports = %s/%s, %#v/%#v", firstHash, secondHash, first, second)
	}
	workPath := objectPath(t, first, "row_note")
	workPage, err := os.ReadFile(filepath.Join(firstRoot, filepath.FromSlash(workPath)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(workPage), "# Storage decision") ||
		!strings.Contains(string(workPage), "tags: [\"architecture\"]") ||
		!strings.Contains(string(workPage), "A portable semantic note") ||
		!strings.Contains(string(workPage), "[[db_personal/tbl_people/row_alice.md|Alice]]") ||
		!strings.Contains(string(workPage), "relation_type: knows") {
		t.Fatalf("work page =\n%s", workPage)
	}
	_ = dictionary
	_ = rows
}

func TestIncrementalExportKeepsStablePathsAcrossRenameAndRemovesDeletedRows(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, dictionary, rows := wikiFixture(t, ctx)
	defer databaseStore.Close()
	root := filepath.Join(t.TempDir(), "vault")
	firstSnapshot, err := snapshot.New(databaseStore).Export(ctx)
	if err != nil {
		t.Fatal(err)
	}
	first, err := wikiexport.ExportSnapshot(firstSnapshot, root, wikiProfile())
	if err != nil {
		t.Fatal(err)
	}
	notePath := objectPath(t, first, "row_note")
	noteFile := filepath.Join(root, filepath.FromSlash(notePath))
	old := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(noteFile, old, old); err != nil {
		t.Fatal(err)
	}
	unchanged, err := wikiexport.ExportSnapshot(firstSnapshot, root, wikiProfile())
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(noteFile); err != nil || !info.ModTime().Equal(old) || !reflect.DeepEqual(unchanged, first) {
		t.Fatalf("unchanged incremental export rewrote page: %#v, %v", info, err)
	}

	if _, err := dictionary.RenameDatabase(ctx, "work", "renamed-work"); err != nil {
		t.Fatal(err)
	}
	if _, err := dictionary.RenameTable(ctx, "renamed-work", "notes", "decisions"); err != nil {
		t.Fatal(err)
	}
	if _, err := dictionary.RenameColumn(ctx, "renamed-work", "decisions", "body", "content"); err != nil {
		t.Fatal(err)
	}
	renamedSnapshot, err := snapshot.New(databaseStore).Export(ctx)
	if err != nil {
		t.Fatal(err)
	}
	renamed, err := wikiexport.ExportSnapshot(renamedSnapshot, root, wikiProfile())
	if err != nil {
		t.Fatal(err)
	}
	if got := objectPath(t, renamed, "row_note"); got != notePath {
		t.Fatalf("renamed path = %q, want stable %q", got, notePath)
	}
	page, err := os.ReadFile(noteFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(page), `database: "renamed-work"`) ||
		!strings.Contains(string(page), `table: "decisions"`) ||
		!strings.Contains(string(page), "## content") {
		t.Fatalf("renamed page =\n%s", page)
	}

	userFile := filepath.Join(root, "my-notes.md")
	if err := os.WriteFile(userFile, []byte("not managed by Memora\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := rows.Delete(ctx, "personal", "people", "row_alice", row.WriteOptions{ExpectedSchemaVersion: 1, ExpectedRevision: 1}); err != nil {
		t.Fatal(err)
	}
	deletedSnapshot, err := snapshot.New(databaseStore).Export(ctx)
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := wikiexport.ExportSnapshot(deletedSnapshot, root, wikiProfile())
	if err != nil {
		t.Fatal(err)
	}
	if len(deleted.Objects) != 1 {
		t.Fatalf("objects after deletion = %#v", deleted.Objects)
	}
	if _, err := os.Stat(filepath.Join(root, "db_personal", "tbl_people", "row_alice.md")); !os.IsNotExist(err) {
		t.Fatalf("deleted Row page remains: %v", err)
	}
	page, err = os.ReadFile(noteFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(page), "row_alice.md") {
		t.Fatalf("deleted relation target remains in source page:\n%s", page)
	}
	if content, err := os.ReadFile(userFile); err != nil || string(content) != "not managed by Memora\n" {
		t.Fatalf("unmanaged user file changed: %q, %v", content, err)
	}
}

func TestExportProfileRejectsUnknownOrDuplicatedStableFields(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, _, _ := wikiFixture(t, ctx)
	defer databaseStore.Close()
	encoded, err := snapshot.New(databaseStore).Export(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, fields := range [][]string{{"col_missing"}, {"col_body", "col_body"}} {
		profile := wikiProfile()
		table := profile.Tables["tbl_notes"]
		table.BodyFieldIDs = fields
		profile.Tables["tbl_notes"] = table
		_, err := wikiexport.ExportSnapshot(encoded, filepath.Join(t.TempDir(), "vault"), profile)
		assertCode(t, err, result.CodeValidation)
	}

	profile := wikiProfile()
	table := profile.Tables["tbl_notes"]
	table.PropertyFieldIDs = []string{"col_body"}
	table.FieldOrder = []string{"col_body"}
	profile.Tables["tbl_notes"] = table
	_, err = wikiexport.ExportSnapshot(encoded, filepath.Join(t.TempDir(), "vault"), profile)
	assertCode(t, err, result.CodeValidation)
}

func wikiFixture(t *testing.T, ctx context.Context) (store.Store, *catalog.Service, *row.Service) {
	t.Helper()
	databaseStore, err := sqlitestore.Open(filepath.Join(t.TempDir(), "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	dictionary := catalog.New(databaseStore, catalog.Options{IDs: &idSource{values: []string{
		"work", "notes", "title", "body", "tag", "personal", "people", "name",
	}}})
	if _, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{Name: "work", Purpose: "work", Scope: "reviewed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := dictionary.CreateTable(ctx, "work", catalog.TableDefinition{
		Name: "notes", Purpose: "notes", RowSemantics: "one note", Columns: []catalog.ColumnDefinition{
			{Name: "title", Type: "TEXT", Purpose: "title"},
			{Name: "body", Type: "TEXT", Purpose: "body"},
			{Name: "tag", Type: "TEXT", Purpose: "Obsidian tag"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{Name: "personal", Purpose: "people", Scope: "contacts"}); err != nil {
		t.Fatal(err)
	}
	if _, err := dictionary.CreateTable(ctx, "personal", catalog.TableDefinition{
		Name: "people", Purpose: "people", RowSemantics: "one person",
		Columns: []catalog.ColumnDefinition{{Name: "name", Type: "TEXT", Purpose: "name"}},
	}); err != nil {
		t.Fatal(err)
	}
	rows := row.New(databaseStore, dictionary, row.Options{
		IDs: &idSource{values: []string{"note", "alice"}}, RelationIDs: &idSource{values: []string{"knows"}},
		RelationPolicy: row.RelationPolicyFunc(func(context.Context, catalog.Table, catalog.Table) bool { return true }),
	})
	if _, err := rows.Insert(ctx, "work", "notes", map[string]any{
		"title": "Storage decision", "body": "A portable semantic note", "tag": "architecture",
	}, row.WriteOptions{ExpectedSchemaVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := rows.Insert(ctx, "personal", "people", map[string]any{"name": "Alice"}, row.WriteOptions{ExpectedSchemaVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := rows.Relate(ctx, row.RelationDefinition{
		Source: row.RelationEndpoint{Database: "work", Table: "notes", RowID: "row_note"},
		Type:   "knows", Target: row.RelationEndpoint{Database: "personal", Table: "people", RowID: "row_alice"},
		Description: "cross-database evidence",
	}); err != nil {
		t.Fatal(err)
	}
	return databaseStore, dictionary, rows
}

func wikiProfile() wikiexport.Profile {
	return wikiexport.Profile{Version: wikiexport.ProfileVersion, Tables: map[string]wikiexport.TableProfile{
		"tbl_notes": {
			TitleFieldID: "col_title", BodyFieldIDs: []string{"col_body"},
			TagFieldIDs: []string{"col_tag"},
		},
		"tbl_people": {TitleFieldID: "col_name", BodyFieldIDs: []string{}},
	}}
}

func objectPath(t *testing.T, manifest wikiexport.Manifest, rowID string) string {
	t.Helper()
	for _, object := range manifest.Objects {
		if object.RowID == rowID {
			return object.Path
		}
	}
	t.Fatalf("manifest has no %s: %#v", rowID, manifest.Objects)
	return ""
}

func assertCode(t *testing.T, err error, want result.Code) {
	t.Helper()
	var stable interface{ StableCode() string }
	if !errors.As(err, &stable) || stable.StableCode() != string(want) {
		t.Fatalf("error = %v, want stable code %q", err, want)
	}
}

type idSource struct{ values []string }

func (source *idSource) Next() (string, error) {
	value := source.values[0]
	source.values = source.values[1:]
	return value, nil
}
