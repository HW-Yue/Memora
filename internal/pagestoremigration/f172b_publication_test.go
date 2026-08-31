package pagestoremigration

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/nativecatalog"
	"github.com/HW-Yue/Memora/internal/nativemutation"
	"github.com/HW-Yue/Memora/internal/nativerouter"
	"github.com/HW-Yue/Memora/internal/nativerow"
	"github.com/HW-Yue/Memora/internal/row"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
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
	err := authority.PublishMutation(ctx, []row.Row{{
		ID: "row_invalid", DatabaseID: table.DatabaseID, TableID: table.ID,
		SchemaVersion: table.SchemaVersion, Revision: 1, State: row.StateLive,
		Values: map[string]any{"col_unknown": "not in schema"},
	}}, nil, func() error {
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

func TestAuthorityPublishesMultipleRowsInOneFulltextTransaction(t *testing.T) {
	ctx := context.Background()
	_, file, authority := newAuthorityFixture(t)
	_, _, table, _ := authorityValuesWithoutRow(t, ctx, file, authority)
	columnID := table.Columns[0].ID
	// Timestamps are part of a Row, not decoration: the clustered leaf stores the
	// encoded Row, so publishing one without them would put an incomplete Row in
	// the tree. The write path always sets them.
	written := time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC)
	values := []row.Row{
		{ID: "row_batch_a", DatabaseID: table.DatabaseID, TableID: table.ID, SchemaVersion: table.SchemaVersion,
			Revision: 1, CommitSequence: 1, State: row.StateLive,
			Values: map[string]any{columnID: "alpha shared"}, CreatedAt: written, UpdatedAt: written},
		{ID: "row_batch_b", DatabaseID: table.DatabaseID, TableID: table.ID, SchemaVersion: table.SchemaVersion,
			Revision: 1, CommitSequence: 1, State: row.StateLive,
			Values: map[string]any{columnID: "beta shared"}, CreatedAt: written, UpdatedAt: written},
	}
	// The Fulltext Tree is no longer part of a publication's transaction — it
	// is derived and catches up from the committed change log — so the batching
	// property is measured on the authoritative Trees here. That a batch
	// reaches Fulltext in one transaction is covered by the coordinator tests,
	// which drive a write that actually records a change.
	before := treeRevision(t, authority, "versions")
	committed := false
	if err := authority.PublishMutation(ctx, values, nil, func() error {
		committed = true
		return nil
	}); err != nil || !committed {
		t.Fatalf("PublishMutation(batch) committed=%v error=%v", committed, err)
	}
	if after := treeRevision(t, authority, "versions"); after != before+1 {
		t.Fatalf("Row version batch revision = %d, want %d", after, before+1)
	}
	for _, value := range values {
		if _, err := authority.generation.versions.ByRevision(value.ID, 1); err != nil {
			t.Fatalf("batch Row %s missing from the version Tree: %v", value.ID, err)
		}
	}
}

func TestCoordinatorPublishesMultiRowMutationToOneFulltextRevision(t *testing.T) {
	ctx := context.Background()
	_, file, authority := newAuthorityFixture(t)
	_, rows, table, _ := authorityValuesWithoutRow(t, ctx, file, authority)
	first, err := rows.Insert(ctx, "work", "notes", map[string]any{"title": "first old"}, row.WriteOptions{
		ExpectedSchemaVersion: table.SchemaVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := rows.Insert(ctx, "work", "notes", map[string]any{"title": "second old"}, row.WriteOptions{
		ExpectedSchemaVersion: table.SchemaVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	repository := nativerow.New(file)
	firstBody, err := repository.Read(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	secondBody, err := repository.Read(second.ID)
	if err != nil {
		t.Fatal(err)
	}
	now := firstBody.UpdatedAt.Add(time.Minute)
	firstBody.Revision, firstBody.CommitSequence, firstBody.UpdatedAt = 2, 0, now
	firstBody.Values[table.Columns[0].ID] = "first merged"
	secondBody.Revision, secondBody.CommitSequence, secondBody.UpdatedAt = 2, 0, now
	secondBody.Values[table.Columns[0].ID] = "second merged"
	before := fulltextTreeRevision(t, authority)
	err = nativemutation.New(file, repository, nativerouter.New(file), authority).Commit(nativemutation.Plan{
		Changes: []nativemutation.RowChange{
			{Row: firstBody, Operation: history.OperationMerge, RecordedAt: now},
			{Row: secondBody, Operation: history.OperationMerge, RecordedAt: now},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if after := fulltextTreeRevision(t, authority); after != before+1 {
		t.Fatalf("coordinator Fulltext revision = %d, want %d", after, before+1)
	}
	assertNoRowPosting(t, authority, "old")
	postings, err := authority.Generation().Fulltext().Postings("merged")
	if err != nil || len(postings) != 2 || postings[0].Revision != 2 || postings[1].Revision != 2 {
		t.Fatalf("coordinator batch postings = %#v, %v", postings, err)
	}
}

func TestAuthorityFulltextWALFaultPoisonsAndReopenConverges(t *testing.T) {
	ctx := context.Background()
	directory, file, authority := newAuthorityFixture(t)
	_, rows, table, inserted := authorityValues(t, ctx, file, authority)
	for _, tree := range authority.generation.trees {
		if tree.manifest.Kind == "fulltext" {
			if err := tree.set.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
	_, err := rows.Update(ctx, "work", "notes", inserted.ID, map[string]any{"title": "wal recovered"}, row.WriteOptions{
		ExpectedSchemaVersion: table.SchemaVersion, ExpectedRevision: inserted.Revision,
	})
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("Fulltext WAL fault Update() error = %v", err)
	}
	// F226: reads stay available; the affected Database fails closed for writes.
	if _, err := authority.Capture(ctx); err != nil {
		t.Fatalf("Capture() after fault = %v, want success", err)
	}
	assertDatabaseWritesPoisoned(t, ctx, authority, table.DatabaseID)
	if err := authority.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	reopenedFile, err := nativestore.Open(filepath.Join(directory, "database.memora"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedFile.Close()
	reopened, err := OpenAuthority(ctx, reopenedFile, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	assertNoRowPosting(t, reopened, "indexed")
	assertRowPosting(t, reopened, "wal", inserted.ID, 2)
	assertRowPosting(t, reopened, "recovered", inserted.ID, 2)
}

func TestAuthorityReopenFulltextReplayDoesNotAppendWAL(t *testing.T) {
	ctx := context.Background()
	directory, file, authority := newAuthorityFixture(t)
	_, _, _, inserted := authorityValues(t, ctx, file, authority)
	nextBefore, err := authority.nextTransactionID("fulltext")
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	reopenedFile, err := nativestore.Open(filepath.Join(directory, "database.memora"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedFile.Close()
	reopened, err := OpenAuthority(ctx, reopenedFile, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	nextAfter, err := reopened.nextTransactionID("fulltext")
	if err != nil {
		t.Fatal(err)
	}
	if nextAfter != nextBefore {
		t.Fatalf("replay appended Fulltext WAL transaction: before=%d after=%d", nextBefore, nextAfter)
	}
	assertRowPosting(t, reopened, "indexed", inserted.ID, 1)
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

	old, err := openLiveGeneration(filepath.Join(directory, GenerationDirectory))
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

func fulltextTreeRevision(t *testing.T, authority *Authority) uint64 {
	return treeRevision(t, authority, "fulltext")
}

func treeRevision(t *testing.T, authority *Authority, kind string) uint64 {
	t.Helper()
	for _, tree := range authority.generation.trees {
		if tree.manifest.Kind == kind {
			return tree.runtime.State().Revision
		}
	}
	t.Fatal("Authority generation has no Fulltext Tree")
	return 0
}
