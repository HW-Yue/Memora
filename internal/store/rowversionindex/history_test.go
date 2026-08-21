package rowversionindex

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/row"
)

func versioned(id string, revision, sequence uint64, body, history string) Locator {
	value := locator(id, revision, sequence, row.StateLive)
	value.Body, value.History = body, history
	return value
}

// TestOneLeafCarriesBothTheRowAndItsHistory pins that a revision is one thing on
// disk. The key (rowID, revision) is the same key History was stored under in the
// record log, so keeping them apart bought a second lookup and nothing else.
func TestOneLeafCarriesBothTheRowAndItsHistory(t *testing.T) {
	_, _, _, index := newTestIndex(t)
	value := versioned("row_one", 1, 10, "the row", "insert by agent")
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

// TestSecondaryKeysCarryNeitherBodyNorHistory pins that a revision's content is
// stored once. The commit and identity keys point at the revision; copying the
// content under each of them would multiply the file size by the number of keys.
func TestSecondaryKeysCarryNeitherBodyNorHistory(t *testing.T) {
	_, _, _, index := newTestIndex(t)
	value := versioned("row_one", 1, 10, strings.Repeat("body", 64), strings.Repeat("meta", 64))
	if _, err := index.Append(1, []Locator{value}); err != nil {
		t.Fatal(err)
	}

	byCommit, err := index.ByCommit("row_one", 10)
	if err != nil {
		t.Fatal(err)
	}
	if byCommit.Body != "" || byCommit.History != "" {
		t.Fatalf("secondary key copied content: %d body bytes, %d history bytes",
			len(byCommit.Body), len(byCommit.History))
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
			fmt.Sprintf("body %d", revision), fmt.Sprintf("history %d", revision)))
	}
	// A second Row interleaves in key order and must never appear in the walk.
	values = append(values, versioned("row_other", 1, 1, "other body", "other history"))
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
			fmt.Sprintf("body %d", revision), fmt.Sprintf("history %d", revision)))
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
		if page.Locators[position].Body == "" || page.Locators[position].History == "" {
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
	if _, err := index.Append(1, []Locator{versioned("row_one", 1, 10, "body", "history")}); err != nil {
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
