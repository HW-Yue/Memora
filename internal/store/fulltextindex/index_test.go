package fulltextindex

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/HW-Yue/Memora/internal/fulltext"
	"github.com/HW-Yue/Memora/internal/store/page"
	"github.com/HW-Yue/Memora/internal/store/treecommit"
	"github.com/HW-Yue/Memora/internal/store/wal"
)

const testSpaceID = uint64(0x4d454d465458)

func TestPersistentIndexBootstrapReopensFromDurableWAL(t *testing.T) {
	directory := t.TempDir()
	walPath := filepath.Join(directory, "wal")
	pagePath := filepath.Join(directory, "fulltext.pages")
	set, manager, runtime, index := openTestIndex(t, walPath, pagePath, false)
	documents := []fulltext.Document{
		document("row_1", 7, "Durable architecture"),
		document("row_2", 3, "数据库恢复"),
	}
	receipt, err := index.Bootstrap(1, documents)
	if err != nil || !receipt.Changed {
		t.Fatalf("Bootstrap() = %+v, %v", receipt, err)
	}
	want, err := fulltext.Build(documents)
	if err != nil {
		t.Fatal(err)
	}
	assertPostingsEqual(t, index, want.AllPostings())
	committed := runtime.State()
	if count, err := manager.PageCount(); err != nil || count != 1 {
		t.Fatalf("PageCount before crash = %d, %v", count, err)
	}
	if err := set.Close(); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}

	reopenedSet, reopenedManager, reopenedRuntime, reopened := openTestIndex(t, walPath, pagePath, true)
	defer func() { _ = reopenedSet.Close(); _ = reopenedManager.Close() }()
	if reopenedRuntime.State() != committed {
		t.Fatalf("reopened state = %+v, want %+v", reopenedRuntime.State(), committed)
	}
	assertPostingsEqual(t, reopened, want.AllPostings())
}

func TestPersistentIndexReplacementIsAtomicRevisionedAndIdempotent(t *testing.T) {
	_, _, _, index := newTestIndex(t)
	if _, err := index.Bootstrap(1, nil); err != nil {
		t.Fatal(err)
	}
	first := document("row_1", 1, "old old title")
	receipt, err := index.Replace(2, first)
	if err != nil || receipt.Added != 2 || receipt.Removed != 0 || !receipt.Changed {
		t.Fatalf("first Replace() = %+v, %v", receipt, err)
	}
	assertSinglePosting(t, index, "old", 1, 2)

	replay, err := index.Replace(3, first)
	if err != nil || !replay.Replay || replay.Changed || replay.Digest != receipt.Digest {
		t.Fatalf("replay Replace() = %+v, %v", replay, err)
	}
	second := document("row_1", 2, "new title")
	receipt, err = index.Replace(4, second)
	if err != nil || receipt.Added != 2 || receipt.Removed != 2 {
		t.Fatalf("second Replace() = %+v, %v", receipt, err)
	}
	if got, err := index.Postings("old"); err != nil || len(got) != 0 {
		t.Fatalf("stale postings = %#v, %v", got, err)
	}
	assertSinglePosting(t, index, "new", 2, 1)

	tombstone := second
	tombstone.Revision = 3
	tombstone.State = fulltext.StateDeleted
	tombstone.Fields = nil
	receipt, err = index.Replace(5, tombstone)
	if err != nil || receipt.Added != 0 || receipt.Removed != 2 {
		t.Fatalf("delete Replace() = %+v, %v", receipt, err)
	}
	if got, err := index.AllPostings(); err != nil || len(got) != 0 {
		t.Fatalf("postings after delete = %#v, %v", got, err)
	}
	if _, err := index.Replace(6, document("row_1", 5, "skipped")); !errors.Is(err, ErrConflict) {
		t.Fatalf("skipped Replace() error = %v", err)
	}
}

func document(objectID string, revision uint64, text string) fulltext.Document {
	return fulltext.Document{
		Version: fulltext.DocumentVersion, Kind: fulltext.KindRow,
		DatabaseID: "db_work", TableID: "tbl_notes", ObjectID: objectID,
		Revision: revision, SchemaRevision: 1, State: fulltext.StateLive, Complete: true,
		Fields: []fulltext.Field{{ID: "col_title", Values: []fulltext.Value{fulltext.TextValue(text)}}},
	}
}

func assertSinglePosting(t *testing.T, index *Index, term string, revision, frequency uint64) {
	t.Helper()
	got, err := index.Postings(term)
	if err != nil || len(got) != 1 || got[0].Term != term || got[0].Revision != revision ||
		got[0].Frequency != frequency {
		t.Fatalf("Postings(%q) = %#v, %v", term, got, err)
	}
}

func assertPostingsEqual(t *testing.T, index *Index, want []fulltext.Posting) {
	t.Helper()
	got, err := index.AllPostings()
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("AllPostings() = %#v, %v; want %#v", got, err, want)
	}
}

func newTestIndex(t *testing.T) (*wal.SegmentSet, *page.Manager, *treecommit.Runtime, *Index) {
	t.Helper()
	directory := t.TempDir()
	set, manager, runtime, index := openTestIndex(
		t, filepath.Join(directory, "wal"), filepath.Join(directory, "fulltext.pages"), false,
	)
	t.Cleanup(func() { _ = set.Close(); _ = manager.Close() })
	return set, manager, runtime, index
}

func openTestIndex(
	t *testing.T, walPath, pagePath string, reopen bool,
) (*wal.SegmentSet, *page.Manager, *treecommit.Runtime, *Index) {
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
