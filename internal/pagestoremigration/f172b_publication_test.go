package pagestoremigration

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/nativecatalog"
	"github.com/HW-Yue/Memora/internal/nativerow"
	"github.com/HW-Yue/Memora/internal/row"
)

func TestAuthorityPublishesInsertUpdateAndDeletePostings(t *testing.T) {
	ctx := context.Background()
	_, file, authority := newAuthorityFixture(t)
	_, rows, table, inserted := authorityValues(t, ctx, file, authority)
	assertRowPosting(t, authority, "indexed", inserted.ID, 1)

	updated, err := rows.Update(ctx, "work", "notes", inserted.ID, map[string]any{"title": "revised"}, row.WriteOptions{
		ExpectedSchemaVersion: table.SchemaVersion, ExpectedRevision: inserted.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertNoRowPosting(t, authority, "indexed")
	assertRowPosting(t, authority, "revised", inserted.ID, updated.Revision)

	if _, err := rows.Delete(ctx, "work", "notes", inserted.ID, row.WriteOptions{
		ExpectedSchemaVersion: table.SchemaVersion, ExpectedRevision: updated.Revision,
	}); err != nil {
		t.Fatal(err)
	}
	assertNoRowPosting(t, authority, "revised")
}

func TestAuthorityRejectsInvalidRowProjectionBeforeBodyCommit(t *testing.T) {
	ctx := context.Background()
	_, file, authority := newAuthorityFixture(t)
	_, _, table, _ := authorityValuesWithoutRow(t, ctx, file, authority)
	committed := false
	err := authority.PublishRows(ctx, []row.Row{{
		ID: "row_invalid", DatabaseID: table.DatabaseID, TableID: table.ID,
		SchemaVersion: table.SchemaVersion, Revision: 1, State: row.StateLive,
		Values: map[string]any{"col_unknown": "not in schema"},
	}}, func() error {
		committed = true
		return nil
	})
	if err == nil || committed {
		t.Fatalf("invalid projection commit=%v error=%v", committed, err)
	}
	if _, err := authority.Capture(ctx); err != nil {
		t.Fatalf("preflight error poisoned Authority: %v", err)
	}
}

func TestAuthorityReopenUsesCOWForSkippedFulltextRevisions(t *testing.T) {
	ctx := context.Background()
	directory, file, authority := newAuthorityFixture(t)
	_, _, table, inserted := authorityValues(t, ctx, file, authority)
	if err := authority.Close(); err != nil {
		t.Fatal(err)
	}
	dictionary := nativecatalog.NewService(nativecatalog.New(file), nativecatalog.ServiceOptions{})
	legacyRows := nativerow.NewService(nativerow.New(file), dictionary, nativerow.ServiceOptions{})
	second, err := legacyRows.Update(ctx, "work", "notes", inserted.ID, map[string]any{"title": "second"}, row.WriteOptions{
		ExpectedSchemaVersion: table.SchemaVersion, ExpectedRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacyRows.Update(ctx, "work", "notes", inserted.ID, map[string]any{"title": "third"}, row.WriteOptions{
		ExpectedSchemaVersion: table.SchemaVersion, ExpectedRevision: second.Revision,
	}); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenAuthority(ctx, file, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.marker.Epoch != 1 {
		t.Fatalf("revision gap did not publish COW generation: %+v", reopened.marker)
	}
	assertNoRowPosting(t, reopened, "indexed")
	assertNoRowPosting(t, reopened, "second")
	assertRowPosting(t, reopened, "third", inserted.ID, 3)

	old, err := OpenGeneration(filepath.Join(directory, GenerationDirectory))
	if err != nil {
		t.Fatalf("old generation was not preserved: %v", err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertRowPosting(t *testing.T, authority *Authority, term, rowID string, revision uint64) {
	t.Helper()
	postings, err := authority.Generation().Fulltext().Postings(term)
	if err != nil || len(postings) != 1 || postings[0].ObjectID != rowID || postings[0].Revision != revision {
		t.Fatalf("Postings(%q) = %#v, %v", term, postings, err)
	}
}

func assertNoRowPosting(t *testing.T, authority *Authority, term string) {
	t.Helper()
	postings, err := authority.Generation().Fulltext().Postings(term)
	if err != nil || len(postings) != 0 {
		t.Fatalf("Postings(%q) = %#v, %v", term, postings, err)
	}
}
