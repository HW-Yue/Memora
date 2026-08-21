package rowversionindex

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/row"
)

func bodied(id string, revision, sequence uint64, state row.State, body string) Locator {
	value := locator(id, revision, sequence, state)
	value.Body = body
	return value
}

// TestRevisionKeyCarriesTheRowBody pins the clustered index: the leaf under
// (rowID, revision) holds the Row itself, so a reader that reached the leaf has
// the data and needs no second store to resolve.
func TestRevisionKeyCarriesTheRowBody(t *testing.T) {
	_, _, _, index := newTestIndex(t)
	first := bodied("row_one", 1, 10, row.StateLive, "first body\x00with bytes")
	second := bodied("row_one", 2, 20, row.StateLive, "second body")
	if _, err := index.Append(1, []Locator{first, second}); err != nil {
		t.Fatal(err)
	}

	got, err := index.ByRevision("row_one", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got != first {
		t.Fatalf("ByRevision(1) = %+v, want %+v", got, first)
	}
	if got, err = index.ByRevision("row_one", 2); err != nil || got.Body != "second body" {
		t.Fatalf("ByRevision(2) = %+v, %v", got, err)
	}
}

// TestSecondaryKeysDoNotCopyTheBody pins that the Row is stored once. The
// identity, commit and legacy keys are secondary indexes: they carry identity
// and point at the revision, and duplicating the body under each of them would
// multiply the file size by the number of keys.
func TestSecondaryKeysDoNotCopyTheBody(t *testing.T) {
	_, _, _, index := newTestIndex(t)
	value := bodied("row_one", 1, 10, row.StateLive, strings.Repeat("payload", 64))
	if _, err := index.Append(1, []Locator{value}); err != nil {
		t.Fatal(err)
	}

	byCommit, err := index.ByCommit("row_one", 10)
	if err != nil {
		t.Fatal(err)
	}
	if byCommit.Body != "" {
		t.Fatalf("secondary key must not copy the body, got %d bytes", len(byCommit.Body))
	}
	if byCommit.RowID != "row_one" || byCommit.Revision != 1 {
		t.Fatalf("secondary key must still identify the revision: %+v", byCommit)
	}
	asOf, err := index.AsOfCommit("row_one", 10)
	if err != nil {
		t.Fatal(err)
	}
	if asOf.Body != "" || asOf.Revision != 1 {
		t.Fatalf("AsOfCommit = %+v", asOf)
	}
}

// TestBodyIsImmutableOncePublished pins that re-appending a revision with
// different content is a conflict, not a silent overwrite. A published revision
// is the permanent record of what the Row was.
func TestBodyIsImmutableOncePublished(t *testing.T) {
	_, _, _, index := newTestIndex(t)
	if _, err := index.Append(1, []Locator{bodied("row_one", 1, 10, row.StateLive, "original")}); err != nil {
		t.Fatal(err)
	}

	rewritten := bodied("row_one", 1, 10, row.StateLive, "rewritten")
	if _, err := index.Append(2, []Locator{rewritten}); !errors.Is(err, ErrConflict) {
		t.Fatalf("re-appending a revision with a different body error = %v, want ErrConflict", err)
	}
	unchanged, err := index.ByRevision("row_one", 1)
	if err != nil || unchanged.Body != "original" {
		t.Fatalf("the published body must be untouched, got %+v, %v", unchanged, err)
	}
}

// TestOversizedBodyIsRefusedByName pins the page budget. A leaf entry has to fit
// in a 16 KiB Page alongside its neighbours, so the tablespace design caps one
// encoded record at 8 KiB. Exceeding it must fail loudly and name the Row rather
// than corrupt a Page.
func TestOversizedBodyIsRefusedByName(t *testing.T) {
	_, _, _, index := newTestIndex(t)
	huge := bodied("row_huge", 1, 10, row.StateLive, strings.Repeat("x", 8<<10+1))

	_, err := index.Append(1, []Locator{huge})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized body error = %v, want ErrInvalid", err)
	}
	if !strings.Contains(err.Error(), "row_huge") {
		t.Fatalf("the error must name the Row, got %q", err)
	}
}

// TestBodySurvivesReopen pins that the Row lives in the tree on disk, not in the
// writer's memory: a fresh process reading the same pages gets the same bytes.
func TestBodySurvivesReopen(t *testing.T) {
	directory := t.TempDir()
	walPath, pagePath := filepath.Join(directory, "wal"), filepath.Join(directory, "versions.pages")
	set, manager, _, index := openTestIndex(t, walPath, pagePath, false)
	value := bodied("row_one", 1, 10, row.StateLive, "durable body")
	if _, err := index.Append(1, []Locator{value}); err != nil {
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
	got, err := reopened.ByRevision("row_one", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got != value {
		t.Fatalf("after reopen ByRevision = %+v, want %+v", got, value)
	}
}
