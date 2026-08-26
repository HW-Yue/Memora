package pagestoremigration

import (
	"context"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/row"
)

// TestFulltextIsNotInTheWriteTransaction is E2's gate.
//
// The Fulltext index is derived: everything in it can be recomputed from the
// authoritative data. It used to be written by the same transaction that
// changed that data, which meant a user's write could fail — or poison a
// Database — because of an index that could simply have been rebuilt.
//
// The gate is that a publication no longer touches the Fulltext Tree at all.
// Timing is not the point and never was: catch-up runs immediately after the
// write, outside its transaction, so nothing lags. What changed is that the
// write no longer depends on it.
func TestFulltextIsNotInTheWriteTransaction(t *testing.T) {
	ctx := context.Background()
	_, file, authority := newAuthorityFixture(t)
	_, _, table, _ := authorityValuesWithoutRow(t, ctx, file, authority)

	beforeVersions := treeRevision(t, authority, "versions")
	beforeFulltext := treeRevision(t, authority, "fulltext")
	committed := false
	// A publication whose caller records no change entry: the authoritative
	// Trees still advance, and the Fulltext Tree is untouched. That is the
	// decoupling stated as an observation rather than a promise.
	written := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	if err := authority.PublishRows(ctx, []row.Row{{
		ID: "row_decoupled", DatabaseID: table.DatabaseID, TableID: table.ID,
		SchemaVersion: table.SchemaVersion, Revision: 1, CommitSequence: 1,
		State: row.StateLive, Values: map[string]any{table.Columns[0].ID: "decoupled"},
		CreatedAt: written, UpdatedAt: written,
	}}, func() error {
		committed = true
		return nil
	}); err != nil || !committed {
		t.Fatalf("PublishRows() committed=%v error=%v", committed, err)
	}
	if after := treeRevision(t, authority, "versions"); after == beforeVersions {
		t.Fatal("the authoritative Row version Tree did not advance")
	}
	if after := treeRevision(t, authority, "fulltext"); after != beforeFulltext {
		t.Fatalf("Fulltext Tree advanced inside the write: %d then %d", beforeFulltext, after)
	}
}

// TestFulltextCatchesUpWithTheChangeLog is the other half: decoupled, but not
// left behind.
//
// A real write records a committed change, and catch-up runs on the back of it,
// so a search immediately afterwards sees the new Row. The cursor is what makes
// the next round incremental instead of another full projection.
func TestFulltextCatchesUpWithTheChangeLog(t *testing.T) {
	ctx := context.Background()
	_, file, authority := newAuthorityFixture(t)
	_, rows, table, _ := authorityValuesWithoutRow(t, ctx, file, authority)

	inserted, err := rows.Insert(ctx, "work", "notes", map[string]any{
		"title": "catchup subject",
	}, row.WriteOptions{ExpectedSchemaVersion: table.SchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.FulltextCatchUpError(); err != nil {
		t.Fatalf("catch-up after insert failed: %v", err)
	}
	assertRowPosting(t, authority, "catchup", inserted.ID, 1)

	behind, err := authority.FulltextBehind(ctx)
	if err != nil || behind {
		t.Fatalf("FulltextBehind() = %v, %v; want caught up", behind, err)
	}
	cursor, err := authority.generation.fulltext.Cursor()
	if err != nil || cursor == 0 {
		t.Fatalf("Cursor() = %d, %v; want the change log high-water", cursor, err)
	}

	// An update and a delete both have to reach the index the same way.
	updated, err := rows.Update(ctx, "work", "notes", inserted.ID, map[string]any{
		"title": "catchup revised",
	}, row.WriteOptions{
		ExpectedSchemaVersion: table.SchemaVersion, ExpectedRevision: inserted.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertNoRowPosting(t, authority, "subject")
	assertRowPosting(t, authority, "revised", inserted.ID, 2)

	if _, err := rows.Delete(ctx, "work", "notes", inserted.ID, row.WriteOptions{
		ExpectedSchemaVersion: table.SchemaVersion, ExpectedRevision: updated.Revision,
	}); err != nil {
		t.Fatal(err)
	}
	assertNoRowPosting(t, authority, "revised")

	advanced, err := authority.generation.fulltext.Cursor()
	if err != nil || advanced <= cursor {
		t.Fatalf("Cursor() after more writes = %d, %v; want it past %d", advanced, err, cursor)
	}
}
