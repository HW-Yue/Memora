package rowversionindex

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/row"
)

func versioned(id string, revision, sequence uint64, body string) Locator {
	value := locator(id, revision, sequence, row.StateLive)
	value.Body = body
	return value
}

// TestTheClusteredLeafCarriesTheRow pins that reaching a revision's leaf is
// reaching its data — the key (rowID, revision) is the clustered index.
//
// The leaf used to carry the revision's attribution beside the Row. Nothing ever
// read it: attribution belongs to the transaction and is resolved from the
// Change Log by change sequence, so the copy here was bytes in every leaf
// answering a question nobody asked it. See docs/storage/per-table-tree-v1.md §5.8.
func TestTheClusteredLeafCarriesTheRow(t *testing.T) {
	_, _, _, index := newTestIndex(t)
	value := versioned("row_one", 1, 10, "the row")
	if _, err := index.Append(1, []Locator{value}); err != nil {
		t.Fatal(err)
	}

	got, err := index.ByRevision("row_one", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got != value {
		t.Fatalf("ByRevision = %+v, want %+v", got, value)
	}
}

// TestSecondaryKeysCarryNoBody pins that a revision's content is stored once.
// The commit and identity keys point at the revision; copying the content under
// each of them would multiply the file size by the number of keys.
func TestSecondaryKeysCarryNoBody(t *testing.T) {
	_, _, _, index := newTestIndex(t)
	value := versioned("row_one", 1, 10, strings.Repeat("body", 64))
	if _, err := index.Append(1, []Locator{value}); err != nil {
		t.Fatal(err)
	}

	byCommit, err := index.ByCommit("row_one", 10)
	if err != nil {
		t.Fatal(err)
	}
	if byCommit.Body != "" {
		t.Fatalf("secondary key copied content: %d body bytes", len(byCommit.Body))
	}
}

// TestRevisionsPageWalksOneRowNewestFirst pins the replacement for reading every
// history record in the Database: a bounded walk of one Row's revisions, newest
// first, that costs the revisions it returns.
func TestRevisionsPageWalksOneRowNewestFirst(t *testing.T) {
	_, _, _, index := newTestIndex(t)
	values := make([]Locator, 0, 40)
	for revision := uint64(1); revision <= 40; revision++ {
		values = append(values, versioned("row_one", revision, revision,
			fmt.Sprintf("body %d", revision)))
	}
	// A second Row interleaves in key order and must never appear in the walk.
	values = append(values, versioned("row_other", 1, 1, "other body"))
	if _, err := index.Append(1, values); err != nil {
		t.Fatal(err)
	}

	seen := make([]uint64, 0, 40)
	after := uint64(0)
	for {
		page, err := index.RevisionsPage("row_one", after, 12)
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range page.Locators {
			if value.RowID != "row_one" {
				t.Fatalf("walk leaked another Row: %+v", value)
			}
			seen = append(seen, value.Revision)
		}
		if !page.Truncated {
			break
		}
		after = page.NextBeforeRevision
		if after == 0 {
			t.Fatal("a truncated page must name where to resume")
		}
	}
	if len(seen) != 40 {
		t.Fatalf("walked %d revisions, want 40", len(seen))
	}
	for position, revision := range seen {
		if revision != uint64(40-position) {
			t.Fatalf("position %d = revision %d, want %d", position, revision, 40-position)
		}
	}
}

// TestRevisionsPageStopsEarlyForASmallPage pins that asking for a few of many
// revisions reads a few: the walk is bounded by the page, not by the history.
func TestRevisionsPageStopsEarlyForASmallPage(t *testing.T) {
	_, _, _, index := newTestIndex(t)
	values := make([]Locator, 0, 40)
	for revision := uint64(1); revision <= 40; revision++ {
		values = append(values, versioned("row_one", revision, revision,
			fmt.Sprintf("body %d", revision)))
	}
	if _, err := index.Append(1, values); err != nil {
		t.Fatal(err)
	}

	page, err := index.RevisionsPage("row_one", 0, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !page.Truncated || len(page.Locators) != 3 {
		t.Fatalf("RevisionsPage(3) = %d locators, truncated=%v", len(page.Locators), page.Truncated)
	}
	for position, want := range []uint64{40, 39, 38} {
		if page.Locators[position].Revision != want {
			t.Fatalf("position %d = revision %d, want %d", position, page.Locators[position].Revision, want)
		}
		if page.Locators[position].Body == "" {
			t.Fatalf("position %d lost its content: %+v", position, page.Locators[position])
		}
	}
	if page.NextBeforeRevision != 38 {
		t.Fatalf("NextBeforeRevision = %d, want 38", page.NextBeforeRevision)
	}
}

// TestRevisionsPageOnAnUnknownRowIsEmptyNotAnError pins that a Row with no
// revisions walks cleanly — callers distinguish "no history" from "broken".
func TestRevisionsPageOnAnUnknownRowIsEmptyNotAnError(t *testing.T) {
	_, _, _, index := newTestIndex(t)
	if _, err := index.Append(1, []Locator{versioned("row_one", 1, 10, "body")}); err != nil {
		t.Fatal(err)
	}

	page, err := index.RevisionsPage("row_missing", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Locators) != 0 || page.Truncated {
		t.Fatalf("RevisionsPage(unknown Row) = %#v", page)
	}
	if _, err := index.RevisionsPage("", 0, 10); !errors.Is(err, ErrInvalid) {
		t.Fatalf("RevisionsPage(no Row) error = %v, want ErrInvalid", err)
	}
}

// TestALeafWrittenWithHistoryBytesStillDecodes is the compatibility gate for
// dropping the leaf's attribution copy.
//
// The history length keeps its slot in the fixed header and is now always
// written as zero, so a leaf written before this change still declares a
// non-zero length and still has those bytes on disk. The decoder counts them and
// skips them. That is what makes this a field going quiet rather than a format
// change: no generation version bump, no rebuild, and databases written by every
// earlier version open unchanged.
//
// The old bytes are built here rather than taken from a fixture, so the test
// reads a leaf no current code can produce — which is exactly the case at risk.
func TestALeafWrittenWithHistoryBytesStillDecodes(t *testing.T) {
	t.Parallel()

	value := versioned("row_one", 3, 30, "the row")
	encoded, err := encodeLocator(value)
	if err != nil {
		t.Fatal(err)
	}
	metadata := "insert by agent:test"
	legacy := append(append([]byte(nil), encoded...), metadata...)
	binary.LittleEndian.PutUint16(legacy[46:48], uint16(len(metadata)))

	decoded, err := decodeLocator(legacy)
	if err != nil {
		t.Fatalf("a leaf carrying history bytes must still decode: %v", err)
	}
	if decoded != value {
		t.Fatalf("decoded = %+v, want %+v", decoded, value)
	}
	// And the length still has to be honest: a declared length the bytes do not
	// back is corruption, not something to tolerate.
	truncated := append([]byte(nil), encoded...)
	binary.LittleEndian.PutUint16(truncated[46:48], uint16(len(metadata)))
	if _, err := decodeLocator(truncated); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("a history length with no bytes behind it = %v, want ErrCorrupt", err)
	}
}
