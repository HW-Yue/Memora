package btree

import (
	"errors"
	"testing"

	"github.com/HW-Yue/Memora/internal/store/page"
)

func TestRangeCursorSingleLeafBoundsAndDeepCopy(t *testing.T) {
	root := rangeLeaf(t, 7, 1, 0, "a", "1", "b", "2", "c", "3", "d", "4")
	searcher := mustSearcher(t, 7, 1, &mapReader{pages: map[uint64]page.Page{1: root}})
	start, end := []byte("b"), []byte("d")
	cursor, err := searcher.NewCursor(start, end)
	if err != nil {
		t.Fatal(err)
	}
	start[0], end[0] = 'z', 'a'
	batch, err := cursor.Next(10)
	if err != nil {
		t.Fatal(err)
	}
	assertRangeBatch(t, batch, true, "b", "2", "c", "3")
	batch.Entries[0].Key[0], batch.Entries[0].Value[0] = 'X', 'X'
	done, err := cursor.Next(10)
	if err != nil || !done.Done || len(done.Entries) != 0 {
		t.Fatalf("Next after Done = %+v, %v", done, err)
	}
}

func TestRangeCursorPaginatesLeafChainWithoutRereading(t *testing.T) {
	pages := rangeTree(t)
	reader := &mapReader{pages: pages}
	cursor, err := mustSearcher(t, 7, 1, reader).NewCursor(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for index, want := range []struct {
		values []string
		done   bool
	}{
		{[]string{"a", "1", "b", "2"}, false},
		{[]string{"c", "3", "d", "4"}, false},
		{[]string{"e", "5", "f", "6"}, true},
	} {
		batch, err := cursor.Next(2)
		if err != nil {
			t.Fatalf("batch %d: %v", index, err)
		}
		assertRangeBatch(t, batch, want.done, want.values...)
	}
	if !equalIDs(reader.reads, []uint64{1, 2, 3, 4}) {
		t.Fatalf("reads = %v, want root and each leaf once", reader.reads)
	}
}

func TestRangeCursorUsesSeparatorStartAndExclusiveEnd(t *testing.T) {
	reader := &mapReader{pages: rangeTree(t)}
	cursor, err := mustSearcher(t, 7, 1, reader).NewCursor([]byte("c"), []byte("e"))
	if err != nil {
		t.Fatal(err)
	}
	batch, err := cursor.Next(10)
	if err != nil {
		t.Fatal(err)
	}
	assertRangeBatch(t, batch, true, "c", "3", "d", "4")
	if !equalIDs(reader.reads, []uint64{1, 3, 4}) {
		t.Fatalf("reads = %v, want root, start leaf, boundary leaf", reader.reads)
	}
}

func TestRangeCursorEmptyTreeAndInputValidation(t *testing.T) {
	searcher := mustSearcher(t, 7, 1, &mapReader{pages: map[uint64]page.Page{
		1: mustNodePage(t, 7, 1, Node{Kind: KindLeaf}),
	}})
	cursor, err := searcher.NewCursor(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := cursor.Next(1)
	if err != nil || !batch.Done || len(batch.Entries) != 0 {
		t.Fatalf("empty Next = %+v, %v", batch, err)
	}
	if _, err := searcher.NewCursor([]byte("z"), []byte("a")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("reversed bounds error = %v, want ErrInvalid", err)
	}
	cursor, _ = searcher.NewCursor(nil, nil)
	if _, err := cursor.Next(0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Next(0) error = %v, want ErrInvalid", err)
	}
}

func TestRangeCursorPoisonsOnLeafChainCorruption(t *testing.T) {
	internal := mustNodePage(t, 7, 3, Node{Kind: KindInternal, Level: 1, LeftmostChild: 4})
	wrongIdentity := rangeLeaf(t, 7, 3, 0, "b", "2")
	wrongIdentity.Header.PageID = 99
	for name, pages := range map[string]map[uint64]page.Page{
		"cycle": {
			1: rangeLeaf(t, 7, 1, 1, "a", "1"),
		},
		"backward": {
			1: rangeLeaf(t, 7, 1, 2, "b", "2"),
			2: rangeLeaf(t, 7, 2, 0, "a", "1"),
		},
		"empty successor": {
			1: rangeLeaf(t, 7, 1, 2, "a", "1"),
			2: mustNodePage(t, 7, 2, Node{Kind: KindLeaf}),
		},
		"wrong identity": {
			1: rangeLeaf(t, 7, 1, 3, "a", "1"),
			3: wrongIdentity,
		},
		"wrong type": {
			1: rangeLeaf(t, 7, 1, 3, "a", "1"),
			3: internal,
		},
	} {
		t.Run(name, func(t *testing.T) {
			cursor, err := mustSearcher(t, 7, 1, &mapReader{pages: pages}).NewCursor(nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			batch, err := cursor.Next(10)
			if !errors.Is(err, ErrCorrupt) || len(batch.Entries) != 0 {
				t.Fatalf("Next() = %+v, %v; want atomic ErrCorrupt", batch, err)
			}
			again, againErr := cursor.Next(10)
			if !errors.Is(againErr, ErrCorrupt) || len(again.Entries) != 0 {
				t.Fatalf("poison Next() = %+v, %v", again, againErr)
			}
		})
	}
}

func TestRangeCursorPoisonsOnReaderFaultAfterPartialBatch(t *testing.T) {
	wantErr := errors.New("leaf read fault")
	root := rangeLeaf(t, 7, 1, 2, "a", "1")
	reader := ReaderFunc(func(pageID uint64) (page.Page, error) {
		if pageID == 1 {
			return clonePage(root), nil
		}
		return page.Page{}, wantErr
	})
	cursor, _ := mustSearcher(t, 7, 1, reader).NewCursor(nil, nil)
	batch, err := cursor.Next(10)
	if !errors.Is(err, wantErr) || len(batch.Entries) != 0 {
		t.Fatalf("Next() = %+v, %v; want atomic Reader error", batch, err)
	}
	if _, err := cursor.Next(10); !errors.Is(err, wantErr) {
		t.Fatalf("poison error = %v, want original Reader error", err)
	}
}

func TestRangeCursorValidatesInitialDescentLevel(t *testing.T) {
	pages := map[uint64]page.Page{
		1: mustNodePage(t, 7, 1, Node{Kind: KindInternal, Level: 2, LeftmostChild: 2}),
		2: rangeLeaf(t, 7, 2, 0, "a", "1"),
	}
	cursor, _ := mustSearcher(t, 7, 1, &mapReader{pages: pages}).NewCursor(nil, nil)
	if _, err := cursor.Next(1); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Next() error = %v, want level ErrCorrupt", err)
	}
}

func rangeTree(t *testing.T) map[uint64]page.Page {
	t.Helper()
	return map[uint64]page.Page{
		1: mustNodePage(t, 7, 1, Node{Kind: KindInternal, Level: 1, LeftmostChild: 2, InternalEntries: []InternalEntry{
			{Key: []byte("c"), RightChild: 3}, {Key: []byte("e"), RightChild: 4},
		}}),
		2: rangeLeaf(t, 7, 2, 3, "a", "1", "b", "2"),
		3: rangeLeaf(t, 7, 3, 4, "c", "3", "d", "4"),
		4: rangeLeaf(t, 7, 4, 0, "e", "5", "f", "6"),
	}
}

func rangeLeaf(t *testing.T, spaceID, pageID, next uint64, values ...string) page.Page {
	t.Helper()
	node := Node{Kind: KindLeaf, NextLeafPageID: next}
	for index := 0; index < len(values); index += 2 {
		node.LeafEntries = append(node.LeafEntries, LeafEntry{
			Key: []byte(values[index]), Value: []byte(values[index+1]),
		})
	}
	return mustNodePage(t, spaceID, pageID, node)
}

func assertRangeBatch(t *testing.T, batch RangeBatch, done bool, values ...string) {
	t.Helper()
	if batch.Done != done || len(batch.Entries)*2 != len(values) {
		t.Fatalf("batch = %+v, want done=%v values=%v", batch, done, values)
	}
	for index, entry := range batch.Entries {
		if string(entry.Key) != values[index*2] || string(entry.Value) != values[index*2+1] {
			t.Fatalf("entry %d = %q/%q", index, entry.Key, entry.Value)
		}
	}
}
