package fulltextindex

import (
	"errors"
	"testing"

	"github.com/HW-Yue/Memora/internal/fulltext"
)

// TestCursorAdvancesInTheSameTransactionAsTheDocuments is E2's foundation.
//
// This index is derived: it is rebuilt from the committed change log rather
// than written by the transaction that changed the data. Catching up
// incrementally needs to know where it got to, and unlike the change index —
// whose keys are commit sequences, so its highest key IS the answer — nothing
// in this Tree's keyspace means "commit sequence 47". The value has to be
// stored.
//
// It is stored HERE, in the same Tree and the same transaction as the
// documents, so a crash can never leave a cursor that claims more than the
// index holds. A cursor that over-claims makes the next catch-up skip the gap,
// which loses index entries silently — the worst failure available.
func TestCursorAdvancesInTheSameTransactionAsTheDocuments(t *testing.T) {
	_, _, _, index := newTestIndex(t)
	if _, err := index.Bootstrap(1, nil); err != nil {
		t.Fatal(err)
	}
	if cursor, err := index.Cursor(); err != nil || cursor != 0 {
		t.Fatalf("initial Cursor() = %d, %v; want 0", cursor, err)
	}

	receipt, err := index.AdvanceThrough(2, []fulltext.Document{document("row_1", 1, "first entry")}, 7)
	if err != nil || !receipt.Changed {
		t.Fatalf("AdvanceThrough() = %+v, %v", receipt, err)
	}
	if cursor, err := index.Cursor(); err != nil || cursor != 7 {
		t.Fatalf("Cursor() after catch-up = %d, %v; want 7", cursor, err)
	}
	assertSinglePosting(t, index, "first", 1, 1)

	// A batch of changes that touches nothing indexable still has to move the
	// cursor, or catch-up would reconsider the same range forever.
	if _, err := index.AdvanceThrough(3, nil, 11); err != nil {
		t.Fatalf("empty AdvanceThrough() error = %v", err)
	}
	if cursor, err := index.Cursor(); err != nil || cursor != 11 {
		t.Fatalf("Cursor() after empty round = %d, %v; want 11", cursor, err)
	}

	// Re-running a round that is already applied is a replay, not a conflict:
	// catch-up must be safe to repeat after a crash.
	replay, err := index.AdvanceThrough(4, []fulltext.Document{document("row_1", 1, "first entry")}, 11)
	if err != nil || replay.Changed {
		t.Fatalf("replayed AdvanceThrough() = %+v, %v", replay, err)
	}

	if _, err := index.AdvanceThrough(5, nil, 10); !errors.Is(err, ErrConflict) {
		t.Fatalf("backwards cursor error = %v, want ErrConflict", err)
	}
}

// TestCursorIsInvisibleToTheObjectAndPostingSurfaces is why the cursor is a
// separate key kind rather than a sixth fulltext.ObjectKind: bookkeeping must
// not appear as something a caller stored.
func TestCursorIsInvisibleToTheObjectAndPostingSurfaces(t *testing.T) {
	_, _, _, index := newTestIndex(t)
	if _, err := index.Bootstrap(1, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := index.AdvanceThrough(2, []fulltext.Document{document("row_1", 1, "visible entry")}, 42); err != nil {
		t.Fatal(err)
	}
	objects, err := index.Objects()
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 1 || objects[0].ObjectID != "row_1" {
		t.Fatalf("Objects() = %#v, want only the Row", objects)
	}
	postings, err := index.AllPostings()
	if err != nil {
		t.Fatal(err)
	}
	for _, posting := range postings {
		if posting.ObjectID != "row_1" {
			t.Fatalf("AllPostings() leaked bookkeeping: %#v", posting)
		}
	}
}
