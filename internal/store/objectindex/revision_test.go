package objectindex

import (
	"errors"
	"testing"
)

// TestARevisionReplacesTheOneItSucceeds is E7 stage 2's first gate.
//
// Route nodes are revised: a rename, a reparent and a delete each publish a new
// revision under the same identity. The Tree keyed by identity therefore has to
// hold the current one and let it move forward — the immutable-once-published
// rule the record log enforces is about record IDs, and a Route's record ID
// carries its revision while its Tree key does not.
//
// The check is a compare-and-set, the same shape currentrowindex uses: an
// update names the revision it succeeds. The Tree is derived from the record
// log, and a derived structure that silently accepts a revision out of order
// stops being a copy of what it derives from.
func TestARevisionReplacesTheOneItSucceeds(t *testing.T) {
	_, _, _, index := newTestIndex(t)
	if _, err := index.Apply(1, []Update{{
		Record: Record{Kind: kindRoute, ID: "route_a", Revision: 1, Body: []byte("first")},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := index.Apply(2, []Update{{
		Record:           Record{Kind: kindRoute, ID: "route_a", Revision: 2, Body: []byte("second")},
		ExpectedRevision: 1,
	}}); err != nil {
		t.Fatal(err)
	}
	value, err := index.Lookup(kindRoute, "route_a")
	if err != nil {
		t.Fatal(err)
	}
	if value.Revision != 2 || string(value.Body) != "second" {
		t.Fatalf("current record = %+v, want revision 2 holding %q", value, "second")
	}
}

// TestAnUpdateOutOfOrderIsRefused covers both directions of the compare-and-set:
// naming a revision that is not the stored one, and naming none at all for a
// record that already exists.
func TestAnUpdateOutOfOrderIsRefused(t *testing.T) {
	_, _, _, index := newTestIndex(t)
	if _, err := index.Apply(1, []Update{{
		Record: Record{Kind: kindRoute, ID: "route_a", Revision: 1, Body: []byte("first")},
	}}); err != nil {
		t.Fatal(err)
	}
	for name, update := range map[string]Update{
		"skips a revision": {
			Record:           Record{Kind: kindRoute, ID: "route_a", Revision: 3, Body: []byte("third")},
			ExpectedRevision: 2,
		},
		"expects nothing stored": {
			Record: Record{Kind: kindRoute, ID: "route_a", Revision: 2, Body: []byte("second")},
		},
	} {
		if _, err := index.Apply(2, []Update{update}); !errors.Is(err, ErrConflict) {
			t.Fatalf("%s: err = %v, want ErrConflict", name, err)
		}
	}
	// An update that does not move forward is malformed on its own terms — it
	// says nothing about what the Tree holds — so it is refused before the Tree
	// is consulted at all.
	if _, err := index.Apply(2, []Update{{
		Record:           Record{Kind: kindRoute, ID: "route_a", Revision: 1, Body: []byte("rewritten")},
		ExpectedRevision: 1,
	}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an update that does not advance: err = %v, want ErrInvalid", err)
	}
	value, err := index.Lookup(kindRoute, "route_a")
	if err != nil || value.Revision != 1 || string(value.Body) != "first" {
		t.Fatalf("refused updates changed the record: %+v, %v", value, err)
	}
}

// TestRepublishingTheStoredRevisionConverges is what makes a retried publication
// safe. The caller cannot always know whether its last attempt landed, so
// re-sending the revision that is already stored, byte for byte, has to be a
// no-op rather than a conflict.
func TestRepublishingTheStoredRevisionConverges(t *testing.T) {
	_, _, _, index := newTestIndex(t)
	update := Update{Record: Record{Kind: kindRoute, ID: "route_a", Revision: 1, Body: []byte("first")}}
	if _, err := index.Apply(1, []Update{update}); err != nil {
		t.Fatal(err)
	}
	receipt, err := index.Apply(2, []Update{update})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Changed {
		t.Fatal("republishing identical bytes wrote to the Tree")
	}
}
