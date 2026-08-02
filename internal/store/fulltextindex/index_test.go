package fulltextindex

import (
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/HW-Yue/Memora/internal/fulltext"
	"github.com/HW-Yue/Memora/internal/store/btree"
	"github.com/HW-Yue/Memora/internal/store/page"
	"github.com/HW-Yue/Memora/internal/store/treecommit"
	"github.com/HW-Yue/Memora/internal/store/treecontrol"
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

func TestPersistentIndexLargeBootstrapSplitsAndMatchesReference(t *testing.T) {
	_, _, runtime, index := newTestIndex(t)
	documents := make([]fulltext.Document, 500)
	for number := range documents {
		documents[number] = document(
			fmt.Sprintf("row_%04d", number), uint64(1+number%9),
			fmt.Sprintf("shared token%04d", number),
		)
	}
	if _, err := index.Bootstrap(1, documents); err != nil {
		t.Fatal(err)
	}
	root, err := runtime.Read(runtime.State().RootPageID)
	if err != nil {
		t.Fatal(err)
	}
	node, err := btree.Decode(root)
	if err != nil || node.Kind != btree.KindInternal {
		t.Fatalf("root after split = %+v, %v", node, err)
	}
	reference, err := fulltext.Build(documents)
	if err != nil {
		t.Fatal(err)
	}
	assertPostingsEqual(t, index, reference.AllPostings())
	shared, err := index.Postings("shared")
	if err != nil || len(shared) != len(documents) {
		t.Fatalf("shared postings = %d, %v", len(shared), err)
	}
}

func TestPersistentIndexBootstrapBytesIgnoreDocumentOrder(t *testing.T) {
	documents := make([]fulltext.Document, 30)
	for number := range documents {
		documents[number] = document(
			fmt.Sprintf("row_%03d", number), uint64(number+1), fmt.Sprintf("stable value%d", number),
		)
	}
	reversed := append([]fulltext.Document(nil), documents...)
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	build := func(name string, values []fulltext.Document) ([]byte, treecontrol.State) {
		directory := filepath.Join(t.TempDir(), name)
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		pagePath := filepath.Join(directory, "fulltext.pages")
		set, manager, runtime, index := openTestIndex(t, filepath.Join(directory, "wal"), pagePath, false)
		defer func() { _ = set.Close(); _ = manager.Close() }()
		if _, err := index.Bootstrap(1, values); err != nil {
			t.Fatal(err)
		}
		if _, err := runtime.FlushDirty(1024); err != nil {
			t.Fatal(err)
		}
		if err := manager.Sync(); err != nil {
			t.Fatal(err)
		}
		encoded, err := os.ReadFile(pagePath)
		if err != nil {
			t.Fatal(err)
		}
		return encoded, runtime.State()
	}
	left, leftState := build("left", documents)
	right, rightState := build("right", reversed)
	if leftState != rightState || !reflect.DeepEqual(left, right) {
		t.Fatalf("canonical bootstrap differs: left=%+v/%d right=%+v/%d", leftState, len(left), rightState, len(right))
	}
}

func TestPersistentIndexRandomRevisionSequenceMatchesReference(t *testing.T) {
	_, _, _, index := newTestIndex(t)
	if _, err := index.Bootstrap(1, nil); err != nil {
		t.Fatal(err)
	}
	random := rand.New(rand.NewSource(171))
	current := map[string]fulltext.Document{}
	for step := 0; step < 120; step++ {
		objectID := fmt.Sprintf("row_%d", random.Intn(8))
		revision := uint64(1)
		if previous, exists := current[objectID]; exists {
			revision = previous.Revision + 1
		}
		value := document(objectID, revision, fmt.Sprintf("shared value%d", random.Intn(12)))
		if revision > 1 && random.Intn(6) == 0 {
			if random.Intn(2) == 0 {
				value.State = fulltext.StateDeleted
			} else {
				value.State = fulltext.StateSuperseded
			}
			value.Fields = nil
		}
		if _, err := index.Replace(uint64(step+2), value); err != nil {
			t.Fatalf("step %d Replace(): %v", step, err)
		}
		current[objectID] = value
		documents := make([]fulltext.Document, 0, len(current))
		for _, document := range current {
			documents = append(documents, document)
		}
		reference, err := fulltext.Build(documents)
		if err != nil {
			t.Fatal(err)
		}
		got, err := index.AllPostings()
		if err != nil || !reflect.DeepEqual(got, reference.AllPostings()) {
			t.Fatalf("step %d postings = %#v, %v; want %#v", step, got, err, reference.AllPostings())
		}
	}
}

func TestPersistentIndexRejectsScopeChangesAndOversizedTerms(t *testing.T) {
	_, _, _, index := newTestIndex(t)
	if _, err := index.Bootstrap(1, []fulltext.Document{document("row_1", 1, "initial")}); err != nil {
		t.Fatal(err)
	}
	moved := document("row_1", 2, "moved")
	moved.DatabaseID, moved.TableID = "db_other", "tbl_other"
	if _, err := index.Replace(2, moved); !errors.Is(err, ErrConflict) {
		t.Fatalf("scope change error = %v", err)
	}
	tooLarge := document("row_2", 1, strings.Repeat("a", maximumComponent+1))
	if _, err := index.Replace(3, tooLarge); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized term error = %v", err)
	}
	assertSinglePosting(t, index, "initial", 1, 1)
}

func TestPersistentIndexCommitFaultKeepsOldRootReadable(t *testing.T) {
	set, _, _, index := newTestIndex(t)
	if _, err := index.Bootstrap(1, []fulltext.Document{document("row_1", 1, "old")}); err != nil {
		t.Fatal(err)
	}
	if err := set.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := index.Replace(2, document("row_1", 2, "new")); err == nil {
		t.Fatal("Replace() succeeded with closed WAL")
	}
	assertSinglePosting(t, index, "old", 1, 1)
	if got, err := index.Postings("new"); err != nil || len(got) != 0 {
		t.Fatalf("new postings after failed commit = %#v, %v", got, err)
	}
}

func TestPersistentIndexRejectsCorruptTreeWithoutFallback(t *testing.T) {
	directory := t.TempDir()
	walPath := filepath.Join(directory, "wal")
	pagePath := filepath.Join(directory, "fulltext.pages")
	set, manager, runtime, index := openTestIndex(t, walPath, pagePath, false)
	if _, err := index.Bootstrap(1, []fulltext.Document{document("row_1", 1, "durable")}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.FlushDirty(64); err != nil {
		t.Fatal(err)
	}
	root, err := manager.Read(runtime.State().RootPageID)
	if err != nil {
		t.Fatal(err)
	}
	root.Payload[0] ^= 0xff
	if err := manager.Write(root); err != nil {
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
	if _, err := reopened.Postings("durable"); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("corrupt Postings() error = %v", err)
	}
}

func TestPersistentIndexConcurrentReadersObserveCompleteRevisions(t *testing.T) {
	_, _, _, index := newTestIndex(t)
	if _, err := index.Bootstrap(1, []fulltext.Document{document("row_1", 1, "shared value1")}); err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	errorsSeen := make(chan error, 8)
	for reader := 0; reader < 8; reader++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for range 100 {
				postings, err := index.Postings("shared")
				if err != nil || len(postings) != 1 || postings[0].Revision < 1 || postings[0].Revision > 40 {
					errorsSeen <- fmt.Errorf("shared postings = %#v: %w", postings, err)
					return
				}
			}
		}()
	}
	for revision := uint64(2); revision <= 40; revision++ {
		if _, err := index.Replace(revision, document("row_1", revision, fmt.Sprintf("shared value%d", revision))); err != nil {
			t.Fatal(err)
		}
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		t.Fatal(err)
	}
	assertSinglePosting(t, index, "shared", 40, 1)
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
