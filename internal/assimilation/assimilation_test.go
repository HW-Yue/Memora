package assimilation_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/assimilation"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/store"
	nativekvstore "github.com/HW-Yue/Memora/internal/store/nativekv"
)

func TestStructuredInventoryMergesDuplicateWindowsAndBlocksIncompleteFinish(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	database, err := nativekvstore.Open(filepath.Join(t.TempDir(), "coverage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	processor := assimilation.New(database)

	created, err := processor.Process(ctx, inventoryEvent("inventory-1"))
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != assimilation.StatusInProgress || created.Revision != 1 ||
		created.TotalUnits != 4 || created.UnreadCount != 4 {
		t.Fatalf("created receipt = %#v", created)
	}
	replayed, err := processor.Process(ctx, inventoryEvent("inventory-1"))
	if err != nil || !replayed.Replayed {
		t.Fatalf("replayed inventory = %#v, %v", replayed, err)
	}
	changed := inventoryEvent("inventory-1")
	changed.Inventory.Source.Title = "Changed source"
	_, err = processor.Process(ctx, changed)
	assertCode(t, err, result.CodeRevisionConflict)

	revision := created.Revision
	for index, window := range []assimilation.Window{
		{UnitID: "chapter-intro", Start: 0, End: 3, Fingerprint: digest("1")},
		{UnitID: "chapter-intro", Start: 2, End: 5, Fingerprint: digest("2")},
	} {
		receipt, err := processor.Process(ctx, windowEvent("window-"+string(rune('1'+index)), revision, window))
		if err != nil {
			t.Fatal(err)
		}
		revision = receipt.Revision
	}
	if revision != 3 {
		t.Fatalf("revision after merged windows = %d, want 3", revision)
	}
	duplicate, err := processor.Process(ctx, windowEvent("window-duplicate", revision, assimilation.Window{
		UnitID: "chapter-intro", Start: 2, End: 5, Fingerprint: digest("2"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !duplicate.DuplicateWindow || duplicate.Revision != revision {
		t.Fatalf("duplicate receipt = %#v", duplicate)
	}
	_, err = processor.Process(ctx, windowEvent("window-changed", revision, assimilation.Window{
		UnitID: "chapter-intro", Start: 2, End: 5, Fingerprint: digest("9"),
	}))
	assertCode(t, err, result.CodeRevisionConflict)

	for index, window := range []assimilation.Window{
		{UnitID: "page-2", Start: 0, End: 1, Fingerprint: digest("3")},
		{UnitID: "table-1", Start: 0, End: 1, Fingerprint: digest("4")},
	} {
		receipt, err := processor.Process(ctx, windowEvent("partial-"+string(rune('1'+index)), revision, window))
		if err != nil {
			t.Fatal(err)
		}
		revision = receipt.Revision
	}
	incomplete, err := processor.Process(ctx, assimilation.Event{
		Version: assimilation.EventVersion, EventID: "finish-incomplete", TaskID: "book-task",
		Workspace: "project-memora", Kind: assimilation.KindFinish, ExpectedRevision: revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if incomplete.Status != assimilation.StatusIncomplete || incomplete.UnreadCount != 2 ||
		!containsUnread(incomplete.Unread, "page-2", 1, 2) ||
		!containsUnread(incomplete.Unread, "attachment-a", 0, 3) {
		t.Fatalf("incomplete receipt = %#v", incomplete)
	}

	for index, window := range []assimilation.Window{
		{UnitID: "page-2", Start: 1, End: 2, Fingerprint: digest("5")},
		{UnitID: "attachment-a", Start: 0, End: 3, Fingerprint: digest("6")},
	} {
		receipt, err := processor.Process(ctx, windowEvent("complete-"+string(rune('1'+index)), revision, window))
		if err != nil {
			t.Fatal(err)
		}
		revision = receipt.Revision
	}
	complete, err := processor.Process(ctx, assimilation.Event{
		Version: assimilation.EventVersion, EventID: "finish-complete", TaskID: "book-task",
		Workspace: "project-memora", Kind: assimilation.KindFinish, ExpectedRevision: revision,
	})
	if err != nil || complete.Status != assimilation.StatusCoverageComplete || complete.UnreadCount != 0 {
		t.Fatalf("complete receipt = %#v, %v", complete, err)
	}
}

func TestCheckpointAndUnreadRangesRecoverAcrossRestart(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "resume.db")
	database, err := nativekvstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	processor := assimilation.New(database)
	created, err := processor.Process(ctx, simpleInventoryEvent("resume-inventory"))
	if err != nil {
		t.Fatal(err)
	}
	covered, err := processor.Process(ctx, windowEvent("resume-window", created.Revision, assimilation.Window{
		UnitID: "chapter", Start: 0, End: 2, Fingerprint: digest("a"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	checkpointed, err := processor.Process(ctx, assimilation.Event{
		Version: assimilation.EventVersion, EventID: "resume-checkpoint", TaskID: "book-task",
		Workspace: "project-memora", Kind: assimilation.KindCheckpoint, ExpectedRevision: covered.Revision,
		Checkpoint: &assimilation.Checkpoint{
			ActiveUnitID: "chapter", Offset: 2, HostCursor: "pdf:page=2", LastWindowEventID: "resume-window",
		},
	})
	if err != nil || checkpointed.Revision != 3 {
		t.Fatalf("checkpoint receipt = %#v, %v", checkpointed, err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := nativekvstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	status, err := assimilation.New(reopened).Process(ctx, assimilation.Event{
		Version: assimilation.EventVersion, EventID: "resume-status", TaskID: "book-task",
		Workspace: "project-memora", Kind: assimilation.KindStatus,
	})
	if err != nil {
		t.Fatal(err)
	}
	if status.Revision != 3 || status.Checkpoint == nil || status.Checkpoint.Offset != 2 ||
		status.Checkpoint.HostCursor != "pdf:page=2" || status.UnreadCount != 1 ||
		!containsUnread(status.Unread, "chapter", 2, 4) {
		t.Fatalf("recovered status = %#v", status)
	}

	replayed, err := assimilation.New(reopened).Process(ctx, assimilation.Event{
		Version: assimilation.EventVersion, EventID: "resume-status", TaskID: "book-task",
		Workspace: "project-memora", Kind: assimilation.KindStatus,
	})
	if err != nil || !replayed.Replayed {
		t.Fatalf("replayed status = %#v, %v", replayed, err)
	}
}

func TestInventoryValidationAndExplicitClear(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	database, err := nativekvstore.Open(filepath.Join(t.TempDir(), "validation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	processor := assimilation.New(database)

	cyclic := inventoryEvent("cycle")
	cyclic.Inventory.Units[0].ParentID = "directory"
	if _, err := processor.Process(ctx, cyclic); err == nil {
		t.Fatal("cyclic inventory was accepted")
	}
	oversized := inventoryEvent("oversized")
	oversized.Inventory.Units[0].Label = strings.Repeat("x", 201)
	if _, err := processor.Process(ctx, oversized); err == nil {
		t.Fatal("raw-content-sized metadata was accepted")
	}

	created, err := processor.Process(ctx, simpleInventoryEvent("clear-inventory"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = processor.Process(ctx, assimilation.Event{
		Version: assimilation.EventVersion, EventID: "bad-checkpoint", TaskID: "book-task",
		Workspace: "project-memora", Kind: assimilation.KindCheckpoint, ExpectedRevision: created.Revision,
		Checkpoint: &assimilation.Checkpoint{
			ActiveUnitID: "chapter", Offset: 1, LastWindowEventID: "window-never-recorded",
		},
	})
	if err == nil {
		t.Fatal("checkpoint accepted an unknown last window event")
	}
	cleared, err := processor.Process(ctx, assimilation.Event{
		Version: assimilation.EventVersion, EventID: "clear-task", TaskID: "book-task",
		Workspace: "project-memora", Kind: assimilation.KindClear, ExpectedRevision: created.Revision,
	})
	if err != nil || cleared.Status != assimilation.StatusCleared {
		t.Fatalf("clear receipt = %#v, %v", cleared, err)
	}
	_, err = processor.Process(ctx, assimilation.Event{
		Version: assimilation.EventVersion, EventID: "status-after-clear", TaskID: "book-task",
		Workspace: "project-memora", Kind: assimilation.KindStatus,
	})
	assertCode(t, err, result.CodeNotFound)
	read, err := database.Begin(ctx, store.ReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	defer read.Rollback()
	events, err := read.Scan(ctx, "assimilation.events.v1")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || strings.Contains(string(events[0].Value), "chapter") ||
		strings.Contains(string(events[0].Value), "host_cursor") {
		t.Fatalf("event metadata after clear = %#v", events)
	}
}

func inventoryEvent(eventID string) assimilation.Event {
	return assimilation.Event{
		Version: assimilation.EventVersion, EventID: eventID, TaskID: "book-task",
		Workspace: "project-memora", Kind: assimilation.KindInventory,
		Inventory: &assimilation.Inventory{
			Source: assimilation.Source{
				ID: "source-book", Title: "Architecture Book", Locator: "book.pdf", ContentHash: digest("0"),
			},
			Units: []assimilation.Unit{
				{ID: "source", Kind: assimilation.UnitSource, Label: "Architecture Book"},
				{ID: "directory", ParentID: "source", Kind: assimilation.UnitDirectory, Label: "Contents"},
				{ID: "chapter-intro", ParentID: "directory", Kind: assimilation.UnitChapter, Label: "Introduction", Extent: 5},
				{ID: "chapter-details", ParentID: "directory", Kind: assimilation.UnitChapter, Label: "Details"},
				{ID: "page-2", ParentID: "chapter-details", Kind: assimilation.UnitPage, Label: "Page 2", Anchor: "p.2", Extent: 2},
				{ID: "table-1", ParentID: "page-2", Kind: assimilation.UnitTable, Label: "Table 1", Anchor: "p.2/table-1", Extent: 1},
				{ID: "attachment-a", ParentID: "source", Kind: assimilation.UnitAttachment, Label: "Appendix.csv", Extent: 3},
				{ID: "optional-page", ParentID: "source", Kind: assimilation.UnitPage, Label: "Optional ad", Extent: 1, Optional: true},
			},
		},
	}
}

func simpleInventoryEvent(eventID string) assimilation.Event {
	event := inventoryEvent(eventID)
	event.Inventory.Units = []assimilation.Unit{
		{ID: "source", Kind: assimilation.UnitSource, Label: "Architecture Book"},
		{ID: "chapter", ParentID: "source", Kind: assimilation.UnitChapter, Label: "Chapter", Extent: 4},
	}
	return event
}

func windowEvent(eventID string, revision uint64, window assimilation.Window) assimilation.Event {
	return assimilation.Event{
		Version: assimilation.EventVersion, EventID: eventID, TaskID: "book-task",
		Workspace: "project-memora", Kind: assimilation.KindWindow,
		ExpectedRevision: revision, Window: &window,
	}
}

func digest(character string) string {
	return "sha256:" + strings.Repeat(character, 64)
}

func containsUnread(values []assimilation.UnreadRange, unitID string, start, end uint64) bool {
	for _, value := range values {
		if value.UnitID == unitID && value.Start == start && value.End == end {
			return true
		}
	}
	return false
}

func assertCode(t *testing.T, err error, code result.Code) {
	t.Helper()
	var stable interface{ StableCode() string }
	if !errors.As(err, &stable) || stable.StableCode() != string(code) {
		t.Fatalf("error = %v, want code %s", err, code)
	}
}
