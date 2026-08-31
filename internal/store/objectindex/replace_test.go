package objectindex

import (
	"errors"
	"testing"
)

const kindDatabase = uint16(2)

// TestReplacingAKindRemovesWhatIsNoLongerThere is E7 stage 4's first gate.
//
// The Catalog is published whole: one write hands over every Database, Table and
// Column there is. DROP COLUMN is a real statement, so a family that only ever
// grew would keep bodies nothing points at any more — the Tree has to lose what
// the Catalog lost.
func TestReplacingAKindRemovesWhatIsNoLongerThere(t *testing.T) {
	_, _, _, index := newTestIndex(t)
	first := []Record{
		{Kind: kindDatabase, ID: "db_a", Revision: 1, Body: []byte("a")},
		{Kind: kindDatabase, ID: "db_b", Revision: 1, Body: []byte("b")},
	}
	if _, err := index.ReplaceKinds(1, []uint16{kindDatabase}, first); err != nil {
		t.Fatal(err)
	}
	// db_b is gone and db_a moved on: exactly what a Catalog publication looks
	// like after a drop plus an edit.
	second := []Record{{Kind: kindDatabase, ID: "db_a", Revision: 2, Body: []byte("a2")}}
	if _, err := index.ReplaceKinds(2, []uint16{kindDatabase}, second); err != nil {
		t.Fatal(err)
	}
	value, err := index.Lookup(kindDatabase, "db_a")
	if err != nil || value.Revision != 2 || string(value.Body) != "a2" {
		t.Fatalf("db_a after the replacement = %+v, %v", value, err)
	}
	if _, err := index.Lookup(kindDatabase, "db_b"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("db_b survived the replacement: %v", err)
	}
}

// TestReplacingAKindLeavesOtherKindsAlone is what lets Routes and the Catalog
// share one Tree. A Route is revised one at a time and never wholly replaced;
// the Catalog is only ever published whole. A replacement that swept the whole
// Tree would delete every Route the moment a Table was created.
func TestReplacingAKindLeavesOtherKindsAlone(t *testing.T) {
	_, _, _, index := newTestIndex(t)
	if _, err := index.Apply(1, []Update{{
		Record: Record{Kind: kindRoute, ID: "route_a", Revision: 1, Body: []byte("route")},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := index.ReplaceKinds(2, []uint16{kindDatabase}, []Record{
		{Kind: kindDatabase, ID: "db_a", Revision: 1, Body: []byte("a")},
	}); err != nil {
		t.Fatal(err)
	}
	// An empty replacement of that kind: still nothing to do with Routes.
	if _, err := index.ReplaceKinds(3, []uint16{kindDatabase}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := index.Lookup(kindRoute, "route_a"); err != nil {
		t.Fatalf("replacing the Catalog kinds removed a Route: %v", err)
	}
	if _, err := index.Lookup(kindDatabase, "db_a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an empty replacement left db_a behind: %v", err)
	}
}

// TestReplacingRefusesARecordItDoesNotOwn keeps the scope honest: a record of a
// kind the replacement does not name would be written and then never cleaned up,
// because nothing would ever look at that kind again.
func TestReplacingRefusesARecordItDoesNotOwn(t *testing.T) {
	_, _, _, index := newTestIndex(t)
	_, err := index.ReplaceKinds(1, []uint16{kindDatabase}, []Record{
		{Kind: kindRoute, ID: "route_a", Revision: 1, Body: []byte("route")},
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}

// TestReplacingWithNoChangeWritesNothing keeps a Catalog publication that
// changes nothing from advancing the Tree, the same way every other write path
// converges on a retry.
func TestReplacingWithNoChangeWritesNothing(t *testing.T) {
	_, _, _, index := newTestIndex(t)
	records := []Record{{Kind: kindDatabase, ID: "db_a", Revision: 1, Body: []byte("a")}}
	if _, err := index.ReplaceKinds(1, []uint16{kindDatabase}, records); err != nil {
		t.Fatal(err)
	}
	receipt, err := index.ReplaceKinds(2, []uint16{kindDatabase}, records)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Changed {
		t.Fatal("an unchanged Catalog wrote to the Tree")
	}
}
