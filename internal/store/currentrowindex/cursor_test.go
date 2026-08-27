package currentrowindex

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"testing"

	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/store/btree"
)

func TestPageReturnsOrderedCurrentLocatorsAndStates(t *testing.T) {
	_, _, _, index := newTestIndex(t)
	initial := []Locator{
		tableLocator("tbl_notes", "row_c"),
		tableLocator("tbl_notes", "row_a"),
		tableLocator("tbl_notes", "row_bb"),
	}
	updates := make([]Update, 0, len(initial))
	for _, value := range initial {
		updates = append(updates, Update{Locator: value})
	}
	if _, err := index.Apply(1, updates); err != nil {
		t.Fatal(err)
	}
	deleted := initial[0]
	deleted.Revision, deleted.CommitSequence, deleted.State = 2, 20, row.StateDeleted
	superseded := initial[2]
	superseded.Revision, superseded.CommitSequence, superseded.State = 2, 21, row.StateSuperseded
	if _, err := index.Apply(2, []Update{
		{ExpectedRevision: 1, Locator: deleted},
		{ExpectedRevision: 1, Locator: superseded},
	}); err != nil {
		t.Fatal(err)
	}

	want := []Locator{initial[1], deleted, superseded}
	sort.Slice(want, func(left, right int) bool {
		leftKey, _ := RowOrderKey(want[left].RowID)
		rightKey, _ := RowOrderKey(want[right].RowID)
		return bytes.Compare(leftKey, rightKey) < 0
	})
	var got []Locator
	after := ""
	for {
		page, err := index.Page(after, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Locators) > 2 {
			t.Fatalf("Page returned %d locators", len(page.Locators))
		}
		got = append(got, page.Locators...)
		if !page.HasMore {
			if page.NextAfterRowID != "" {
				t.Fatalf("final next cursor = %q", page.NextAfterRowID)
			}
			break
		}
		if page.NextAfterRowID == "" ||
			page.NextAfterRowID != page.Locators[len(page.Locators)-1].RowID {
			t.Fatalf("page cursor = %+v", page)
		}
		after = page.NextAfterRowID
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("paged locators = %+v, want %+v", got, want)
	}
}

func TestPageAfterMissingRowStartsAtItsSortPosition(t *testing.T) {
	_, _, _, index := newTestIndex(t)
	if _, err := index.Apply(1, []Update{
		{Locator: tableLocator("tbl_notes", "row_a")},
		{Locator: tableLocator("tbl_notes", "row_c")},
	}); err != nil {
		t.Fatal(err)
	}
	page, err := index.Page("row_b", 10)
	if err != nil || len(page.Locators) != 1 || page.Locators[0].RowID != "row_c" {
		t.Fatalf("Page(after missing) = %+v, %v", page, err)
	}
}

func TestPageValidatesBoundsAndEmptyTable(t *testing.T) {
	_, _, _, index := newTestIndex(t)
	for _, limit := range []uint64{0, 1001} {
		if _, err := index.Page("", limit); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Page(limit %d) error = %v", limit, err)
		}
	}
	page, err := index.Page("", 10)
	if err != nil || len(page.Locators) != 0 || page.HasMore || page.NextAfterRowID != "" {
		t.Fatalf("empty Page = %+v, %v", page, err)
	}
}

func TestLargeTablePaginationMatchesReferenceOrderWithoutDuplicates(t *testing.T) {
	_, _, _, index := newTestIndex(t)
	updates := make([]Update, 0, 900)
	want := make([]Locator, 0, 900)
	for number := 0; number < 900; number++ {
		value := tableLocator("tbl_notes", fmt.Sprintf("row_%04d", number))
		updates = append(updates, Update{Locator: value})
		want = append(want, value)
	}
	if _, err := index.Apply(1, updates); err != nil {
		t.Fatal(err)
	}
	sort.Slice(want, func(left, right int) bool {
		leftKey, _ := RowOrderKey(want[left].RowID)
		rightKey, _ := RowOrderKey(want[right].RowID)
		return bytes.Compare(leftKey, rightKey) < 0
	})
	got := make([]Locator, 0, len(want))
	after := ""
	for {
		page, err := index.Page(after, 17)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, page.Locators...)
		if !page.HasMore {
			break
		}
		after = page.NextAfterRowID
	}
	if len(got) != len(want) {
		t.Fatalf("paged count = %d, want %d", len(got), len(want))
	}
	for position := range want {
		if got[position] != want[position] {
			t.Fatalf("paged[%d] = %+v, want %+v", position, got[position], want[position])
		}
	}
}

func TestPageSurvivesCrashBeforeFlushAndReopen(t *testing.T) {
	directory := t.TempDir()
	walPath := filepath.Join(directory, "wal")
	pagePath := filepath.Join(directory, "current.pages")
	set, manager, _, index := openTestIndex(t, walPath, pagePath, false)
	if _, err := index.Apply(1, []Update{
		{Locator: tableLocator("tbl_notes", "row_a")},
		{Locator: tableLocator("tbl_notes", "row_b")},
	}); err != nil {
		t.Fatal(err)
	}
	if count, err := manager.PageCount(); err != nil || count != 1 {
		t.Fatalf("pre-crash Page count = %d, %v", count, err)
	}
	if err := set.Close(); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	reopenedSet, reopenedManager, _, reopened := openTestIndex(t, walPath, pagePath, true)
	defer func() { _ = reopenedSet.Close(); _ = reopenedManager.Close() }()
	page, err := reopened.Page("", 1)
	if err != nil || len(page.Locators) != 1 || !page.HasMore || page.NextAfterRowID == "" {
		t.Fatalf("reopened Page = %+v, %v", page, err)
	}
}

func TestPageRejectsKeyLocatorMismatchWithoutFallback(t *testing.T) {
	directory := t.TempDir()
	walPath := filepath.Join(directory, "wal")
	pagePath := filepath.Join(directory, "current.pages")
	set, manager, runtime, index := openTestIndex(t, walPath, pagePath, false)
	value := tableLocator("tbl_notes", "row_a")
	if _, err := index.Apply(1, []Update{{Locator: value}}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.FlushDirty(16); err != nil {
		t.Fatal(err)
	}
	rootID := runtime.State().RootPageID
	root, err := manager.Read(rootID)
	if err != nil {
		t.Fatal(err)
	}
	node, err := btree.Decode(root)
	if err != nil || len(node.LeafEntries) != 1 {
		t.Fatalf("decode root = %+v, %v", node, err)
	}
	node.LeafEntries[0].Key[len(node.LeafEntries[0].Key)-1] ^= 1
	corrupt, err := btree.Encode(root.Header, node)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Write(corrupt); err != nil {
		t.Fatal(err)
	}
	if err := manager.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := set.Close(); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	reopenedSet, reopenedManager, _, reopened := openTestIndex(t, walPath, pagePath, true)
	defer func() { _ = reopenedSet.Close(); _ = reopenedManager.Close() }()
	if _, err := reopened.Page("", 10); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("corrupt Page error = %v", err)
	}
}

func tableLocator(tableID, rowID string) Locator {
	return Locator{
		DatabaseID: "db_memory", TableID: tableID, RowID: rowID,
		SchemaRevision: 3, Revision: 1, CommitSequence: 10, State: row.StateLive,
	}
}
