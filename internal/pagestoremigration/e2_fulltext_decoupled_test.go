package pagestoremigration

import (
	"context"
	"errors"
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

// TestFulltextCatchUpSurvivesAFailedRound is the regression for the way a
// derived index can wedge itself.
//
// A catch-up round fails; writes carry on. The next round now spans several
// revisions of the same Row, and projects one document at the final revision.
// While the index demanded revisions advance by exactly one — the rule that
// made sense when the writer pushed each one inline — that document was
// refused, and every later round was refused for the same reason. One transient
// failure and the index never caught up again.
func TestFulltextCatchUpSurvivesAFailedRound(t *testing.T) {
	ctx := context.Background()
	_, file, authority := newAuthorityFixture(t)
	_, rows, table, _ := authorityValuesWithoutRow(t, ctx, file, authority)

	inserted, err := rows.Insert(ctx, "work", "notes", map[string]any{
		"title": "wedge original",
	}, row.WriteOptions{ExpectedSchemaVersion: table.SchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	assertRowPosting(t, authority, "original", inserted.ID, 1)
	stalled, err := authority.generation.fulltext.Cursor()
	if err != nil {
		t.Fatal(err)
	}

	injected := errors.New("injected catch-up fault")
	authority.checkpoint = func(phase authorityPhase) error {
		if phase == phaseFulltextCatchUp {
			return injected
		}
		return nil
	}
	revision := inserted.Revision
	for _, title := range []string{"wedge second", "wedge third"} {
		updated, err := rows.Update(ctx, "work", "notes", inserted.ID, map[string]any{
			"title": title,
		}, row.WriteOptions{
			ExpectedSchemaVersion: table.SchemaVersion, ExpectedRevision: revision,
		})
		if err != nil {
			t.Fatalf("update %q: %v", title, err)
		}
		revision = updated.Revision
	}
	// The writes succeeded even though the derived index could not keep up.
	// That is the point of the decoupling.
	if authority.FulltextCatchUpError() == nil {
		t.Fatal("a failed catch-up left no trace")
	}
	if cursor, err := authority.generation.fulltext.Cursor(); err != nil || cursor != stalled {
		t.Fatalf("Cursor() while stalled = %d, %v; want %d", cursor, err, stalled)
	}

	authority.checkpoint = nil
	if err := authority.EnsureFulltextCurrent(ctx); err != nil {
		t.Fatalf("catch-up after the stall: %v", err)
	}
	if err := authority.FulltextCatchUpError(); err != nil {
		t.Fatalf("catch-up still failing: %v", err)
	}
	// Two revisions in one round, and the stale terms are gone.
	assertRowPosting(t, authority, "third", inserted.ID, 3)
	assertNoRowPosting(t, authority, "original")
	assertNoRowPosting(t, authority, "second")
	if behind, err := authority.FulltextBehind(ctx); err != nil || behind {
		t.Fatalf("FulltextBehind() after recovery = %v, %v", behind, err)
	}
}
