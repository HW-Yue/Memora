package btree

import (
	"errors"
	"testing"

	"github.com/HW-Yue/Memora/internal/store/page"
)

func TestPointSearchEmptySingleAndDeepCopy(t *testing.T) {
	empty := mustNodePage(t, 7, 1, Node{Kind: KindLeaf})
	searcher := mustSearcher(t, 7, 1, &mapReader{pages: map[uint64]page.Page{1: empty}})
	if _, err := searcher.Get([]byte("a")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty Get() error = %v", err)
	}

	leaf := mustNodePage(t, 7, 2, Node{Kind: KindLeaf, LeafEntries: []LeafEntry{
		{Key: []byte("a"), Value: []byte("one")}, {Key: []byte("c"), Value: []byte("three")},
	}})
	reader := &mapReader{pages: map[uint64]page.Page{2: leaf}}
	searcher = mustSearcher(t, 7, 2, reader)
	value, err := searcher.Get([]byte("c"))
	if err != nil || string(value) != "three" {
		t.Fatalf("Get(c) = %q, %v", value, err)
	}
	value[0] = 'X'
	again, _ := searcher.Get([]byte("c"))
	if string(again) != "three" {
		t.Fatalf("Get value aliases cached Page: %q", again)
	}
	if _, err := searcher.Get([]byte("b")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(b) error = %v", err)
	}
}

func TestPointSearchSeparatorBoundariesReadOnePath(t *testing.T) {
	pages := map[uint64]page.Page{
		1: mustNodePage(t, 7, 1, Node{Kind: KindInternal, Level: 1, LeftmostChild: 2, InternalEntries: []InternalEntry{
			{Key: []byte("m"), RightChild: 3}, {Key: []byte("t"), RightChild: 4},
		}}),
		2: leafPage(t, 7, 2, "a", "left"),
		3: leafPage(t, 7, 3, "m", "middle"),
		4: leafPage(t, 7, 4, "t", "right"),
	}
	for _, test := range []struct {
		key, value string
		child      uint64
	}{
		{"a", "left", 2}, {"m", "middle", 3}, {"t", "right", 4},
	} {
		reader := &mapReader{pages: pages}
		got, err := mustSearcher(t, 7, 1, reader).Get([]byte(test.key))
		if err != nil || string(got) != test.value {
			t.Fatalf("Get(%q) = %q, %v", test.key, got, err)
		}
		if !equalIDs(reader.reads, []uint64{1, test.child}) {
			t.Fatalf("reads = %v, want root + %d", reader.reads, test.child)
		}
	}
}

func TestPointSearchMultiLevelAndStructuralCorruption(t *testing.T) {
	valid := map[uint64]page.Page{
		1: mustNodePage(t, 7, 1, Node{Kind: KindInternal, Level: 2, LeftmostChild: 2}),
		2: mustNodePage(t, 7, 2, Node{Kind: KindInternal, Level: 1, LeftmostChild: 3}),
		3: leafPage(t, 7, 3, "k", "value"),
	}
	got, err := mustSearcher(t, 7, 1, &mapReader{pages: valid}).Get([]byte("k"))
	if err != nil || string(got) != "value" {
		t.Fatalf("deep Get = %q, %v", got, err)
	}

	wrongIdentity := clonePage(valid[2])
	wrongIdentity.Header.PageID = 99
	wrongLevel := mustNodePage(t, 7, 2, Node{Kind: KindInternal, Level: 2, LeftmostChild: 3})
	cycle := mustNodePage(t, 7, 1, Node{Kind: KindInternal, Level: 1, LeftmostChild: 1})
	for name, pages := range map[string]map[uint64]page.Page{
		"identity": {1: valid[1], 2: wrongIdentity},
		"level":    {1: valid[1], 2: wrongLevel},
		"cycle":    {1: cycle},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := mustSearcher(t, 7, 1, &mapReader{pages: pages}).Get([]byte("k")); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("Get() error = %v, want ErrCorrupt", err)
			}
		})
	}
}

func TestPointSearchPreservesReaderErrorAndValidatesInputs(t *testing.T) {
	wantErr := errors.New("read fault")
	searcher := mustSearcher(t, 7, 1, ReaderFunc(func(uint64) (page.Page, error) { return page.Page{}, wantErr }))
	if _, err := searcher.Get([]byte("k")); !errors.Is(err, wantErr) {
		t.Fatalf("Get() error = %v, want read fault", err)
	}
	if _, err := searcher.Get(nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Get(nil) error = %v, want ErrInvalid", err)
	}
	if _, err := NewSearcher(7, 0, ReaderFunc(func(uint64) (page.Page, error) { return page.Page{}, nil })); !errors.Is(err, ErrInvalid) {
		t.Fatalf("NewSearcher zero root error = %v", err)
	}
	if _, err := NewSearcher(7, 1, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("NewSearcher nil Reader error = %v", err)
	}
}

func TestPointSearchRejectsDepthOverflow(t *testing.T) {
	pages := make(map[uint64]page.Page, maxSearchDepth)
	for index := 0; index < maxSearchDepth; index++ {
		pageID := uint64(index + 1)
		pages[pageID] = mustNodePage(t, 7, pageID, Node{
			Kind: KindInternal, Level: uint16(maxSearchDepth - index), LeftmostChild: pageID + 1,
		})
	}
	reader := &mapReader{pages: pages}
	if _, err := mustSearcher(t, 7, 1, reader).Get([]byte("k")); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Get() error = %v, want depth ErrCorrupt", err)
	}
	if len(reader.reads) != maxSearchDepth {
		t.Fatalf("reads = %d, want bounded %d", len(reader.reads), maxSearchDepth)
	}
}

type mapReader struct {
	pages map[uint64]page.Page
	reads []uint64
}

func (reader *mapReader) Read(pageID uint64) (page.Page, error) {
	reader.reads = append(reader.reads, pageID)
	value, exists := reader.pages[pageID]
	if !exists {
		return page.Page{}, page.ErrNotFound
	}
	return clonePage(value), nil
}

func mustSearcher(t *testing.T, spaceID, rootPageID uint64, reader Reader) *Searcher {
	t.Helper()
	searcher, err := NewSearcher(spaceID, rootPageID, reader)
	if err != nil {
		t.Fatal(err)
	}
	return searcher
}

func mustNodePage(t *testing.T, spaceID, pageID uint64, node Node) page.Page {
	t.Helper()
	typeValue := page.TypeBTreeLeaf
	if node.Kind == KindInternal {
		typeValue = page.TypeBTreeInternal
	}
	value, err := Encode(page.Header{Type: typeValue, SpaceID: spaceID, PageID: pageID}, node)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func leafPage(t *testing.T, spaceID, pageID uint64, key, value string) page.Page {
	return mustNodePage(t, spaceID, pageID, Node{Kind: KindLeaf, LeafEntries: []LeafEntry{{Key: []byte(key), Value: []byte(value)}}})
}

func equalIDs(left, right []uint64) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
