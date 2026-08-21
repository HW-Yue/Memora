package objectindex

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/store/page"
	"github.com/HW-Yue/Memora/internal/store/treecommit"
	"github.com/HW-Yue/Memora/internal/store/wal"
)

const testSpaceID = uint64(73)

const (
	kindRoute    = uint16(8)
	kindRelation = uint16(7)
)

func record(kind uint16, id, body string) Record {
	return Record{Kind: kind, ID: id, Body: []byte(body)}
}

// TestLeafCarriesTheObjectItself pins the point of this tree: reaching the leaf
// for (kind, id) is reaching the object. Nothing else has to be consulted to
// turn a key into bytes.
func TestLeafCarriesTheObjectItself(t *testing.T) {
	_, _, _, index := newTestIndex(t)
	if _, err := index.Put(1, []Record{
		record(kindRoute, "route_a", "route body A"),
		record(kindRelation, "rel_a", "relation body\x00with bytes"),
	}); err != nil {
		t.Fatal(err)
	}

	body, err := index.Get(kindRoute, "route_a")
	if err != nil || string(body) != "route body A" {
		t.Fatalf("Get(route_a) = %q, %v", body, err)
	}
	if body, err = index.Get(kindRelation, "rel_a"); err != nil ||
		!bytes.Equal(body, []byte("relation body\x00with bytes")) {
		t.Fatalf("Get(rel_a) = %q, %v", body, err)
	}
	if _, err := index.Get(kindRoute, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(missing) error = %v, want ErrNotFound", err)
	}
}

// TestKindsDoNotBleedIntoEachOther pins that the kind is part of the key. Two
// object types may legitimately use the same ID, and a scan of one kind must
// never return the other's records.
func TestKindsDoNotBleedIntoEachOther(t *testing.T) {
	_, _, _, index := newTestIndex(t)
	if _, err := index.Put(1, []Record{
		record(kindRoute, "shared_id", "the route"),
		record(kindRelation, "shared_id", "the relation"),
	}); err != nil {
		t.Fatal(err)
	}

	if body, err := index.Get(kindRoute, "shared_id"); err != nil || string(body) != "the route" {
		t.Fatalf("Get(route shared_id) = %q, %v", body, err)
	}
	if body, err := index.Get(kindRelation, "shared_id"); err != nil || string(body) != "the relation" {
		t.Fatalf("Get(relation shared_id) = %q, %v", body, err)
	}
	page, err := index.Page(kindRoute, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Records) != 1 || page.Records[0].ID != "shared_id" ||
		string(page.Records[0].Body) != "the route" {
		t.Fatalf("Page(route) = %#v", page.Records)
	}
}

// TestPageWalksOneKindInIDOrder pins the replacement for enumerating every
// record in the file: a bounded, ordered, resumable walk of one kind.
func TestPageWalksOneKindInIDOrder(t *testing.T) {
	_, _, _, index := newTestIndex(t)
	records := make([]Record, 0, 25)
	for number := 0; number < 25; number++ {
		records = append(records, record(kindRoute, fmt.Sprintf("route_%03d", number), fmt.Sprintf("body %d", number)))
	}
	records = append(records, record(kindRelation, "rel_noise", "must not appear"))
	if _, err := index.Put(1, records); err != nil {
		t.Fatal(err)
	}

	seen := make([]string, 0, 25)
	after := ""
	for {
		page, err := index.Page(kindRoute, after, 10)
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range page.Records {
			seen = append(seen, value.ID)
		}
		if !page.Truncated {
			break
		}
		after = page.NextAfterID
		if after == "" {
			t.Fatal("a truncated page must name where to resume")
		}
	}
	if len(seen) != 25 {
		t.Fatalf("walked %d records, want 25", len(seen))
	}
	for index, id := range seen {
		if id != fmt.Sprintf("route_%03d", index) {
			t.Fatalf("record %d = %q, out of ID order", index, id)
		}
	}
}

// TestRewritingARecordWithDifferentBytesConflicts pins that a published record
// is immutable, the same rule the append-only record log enforced with unique
// record IDs. Mutable objects get a new versioned ID; they never overwrite.
func TestRewritingARecordWithDifferentBytesConflicts(t *testing.T) {
	_, _, _, index := newTestIndex(t)
	if _, err := index.Put(1, []Record{record(kindRoute, "route_a", "original")}); err != nil {
		t.Fatal(err)
	}

	if _, err := index.Put(2, []Record{record(kindRoute, "route_a", "rewritten")}); !errors.Is(err, ErrConflict) {
		t.Fatalf("rewriting a record error = %v, want ErrConflict", err)
	}
	if body, err := index.Get(kindRoute, "route_a"); err != nil || string(body) != "original" {
		t.Fatalf("the published record must be untouched, got %q, %v", body, err)
	}
	// Re-putting identical bytes is how a retried publication converges.
	receipt, err := index.Put(3, []Record{record(kindRoute, "route_a", "original")})
	if err != nil {
		t.Fatalf("an idempotent retry must succeed: %v", err)
	}
	if receipt.Changed {
		t.Fatal("an idempotent retry must not rewrite the Tree")
	}
}

// TestOversizedBodyIsRefusedByName pins the page budget, the same 8 KiB record
// cap the Row version tree enforces.
func TestOversizedBodyIsRefusedByName(t *testing.T) {
	_, _, _, index := newTestIndex(t)
	huge := record(kindRoute, "route_huge", strings.Repeat("x", 8<<10+1))

	_, err := index.Put(1, []Record{huge})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized body error = %v, want ErrInvalid", err)
	}
	if !strings.Contains(err.Error(), "route_huge") {
		t.Fatalf("the error must name the record, got %q", err)
	}
}

// TestRecordsSurviveReopen pins that the objects live in the tree on disk: a
// fresh process reading the same pages gets the same bytes.
func TestRecordsSurviveReopen(t *testing.T) {
	directory := t.TempDir()
	walPath, pagePath := filepath.Join(directory, "wal"), filepath.Join(directory, "objects.pages")
	set, manager, _, index := openTestIndex(t, walPath, pagePath, false)
	if _, err := index.Put(1, []Record{
		record(kindRoute, "route_a", "durable route"),
		record(kindRelation, "rel_a", "durable relation"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := set.Close(); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}

	reopenedSet, reopenedManager, _, reopened := openTestIndex(t, walPath, pagePath, true)
	t.Cleanup(func() { _ = reopenedSet.Close(); _ = reopenedManager.Close() })
	if body, err := reopened.Get(kindRoute, "route_a"); err != nil || string(body) != "durable route" {
		t.Fatalf("after reopen Get(route_a) = %q, %v", body, err)
	}
	if body, err := reopened.Get(kindRelation, "rel_a"); err != nil || string(body) != "durable relation" {
		t.Fatalf("after reopen Get(rel_a) = %q, %v", body, err)
	}
}

// TestEmptyTreeReadsCleanly pins that a tree with no records answers lookups and
// walks without erroring — an empty Database is not a corrupt one.
func TestEmptyTreeReadsCleanly(t *testing.T) {
	_, _, _, index := newTestIndex(t)

	if _, err := index.Get(kindRoute, "route_a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get on an empty Tree error = %v, want ErrNotFound", err)
	}
	page, err := index.Page(kindRoute, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Records) != 0 || page.Truncated {
		t.Fatalf("Page on an empty Tree = %#v", page)
	}
}

func newTestIndex(t *testing.T) (*wal.SegmentSet, *page.Manager, *treecommit.Runtime, *Index) {
	t.Helper()
	directory := t.TempDir()
	set, manager, runtime, index := openTestIndex(
		t, filepath.Join(directory, "wal"), filepath.Join(directory, "objects.pages"), false,
	)
	t.Cleanup(func() { _ = set.Close(); _ = manager.Close() })
	return set, manager, runtime, index
}

func openTestIndex(t *testing.T, walPath, pagePath string, reopen bool) (*wal.SegmentSet, *page.Manager, *treecommit.Runtime, *Index) {
	t.Helper()
	var (
		set     *wal.SegmentSet
		manager *page.Manager
		err     error
	)
	if reopen {
		set, err = wal.OpenSegmentSet(walPath, 0)
	} else {
		set, err = wal.CreateSegmentSet(walPath, 0)
	}
	if err != nil {
		t.Fatal(err)
	}
	if reopen {
		manager, err = page.Open(pagePath, testSpaceID)
	} else {
		manager, err = page.Create(pagePath, testSpaceID)
	}
	if err != nil {
		_ = set.Close()
		t.Fatal(err)
	}
	runtime, _, err := treecommit.OpenRuntime(set, manager, treecommit.RuntimeConfig{
		SpaceID: testSpaceID, Capacity: 128, OldFrames: 64,
	})
	if err != nil {
		_ = set.Close()
		_ = manager.Close()
		t.Fatal(err)
	}
	index, err := Open(runtime)
	if err != nil {
		_ = set.Close()
		_ = manager.Close()
		t.Fatal(err)
	}
	return set, manager, runtime, index
}
