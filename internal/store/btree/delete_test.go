package btree

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"testing"

	"github.com/HW-Yue/Memora/internal/store/page"
)

func TestDeleteLeafFrontMiddleBackMakesOnlyTargetInvisible(t *testing.T) {
	header := page.Header{Type: page.TypeBTreeLeaf, SpaceID: 7, PageID: 41, Generation: 3}
	node := Node{Kind: KindLeaf, NextLeafPageID: 42, LeafEntries: []LeafEntry{
		{Key: []byte("a"), Value: []byte("one")},
		{Key: []byte("b"), Value: []byte("two")},
		{Key: []byte("c"), Value: []byte("three")},
	}}
	before := mustEncodeNode(t, header, node)
	for _, test := range []struct {
		key, removed string
		want         []string
	}{
		{key: "a", removed: "one", want: []string{"b", "c"}},
		{key: "b", removed: "two", want: []string{"a", "c"}},
		{key: "c", removed: "three", want: []string{"a", "b"}},
	} {
		t.Run(test.key, func(t *testing.T) {
			got, removed, err := DeleteLeaf(header, node, []byte(test.key))
			if err != nil {
				t.Fatal(err)
			}
			if string(removed) != test.removed || got.NextLeafPageID != 42 {
				t.Fatalf("removed/next = %q/%d", removed, got.NextLeafPageID)
			}
			assertLeafKeys(t, got, test.want)
			encoded, err := Encode(header, got)
			if err != nil {
				t.Fatal(err)
			}
			searcher := mustSearcher(t, 7, 41, &mapReader{pages: map[uint64]page.Page{41: encoded}})
			if _, err := searcher.Get([]byte(test.key)); !errors.Is(err, ErrNotFound) {
				t.Fatalf("deleted Get(%q) error = %v", test.key, err)
			}
			if value, err := searcher.Get(got.LeafEntries[0].Key); err != nil || len(value) == 0 {
				t.Fatalf("neighbor Get() = %q, %v", value, err)
			}
			got.LeafEntries[0].Key[0], got.LeafEntries[0].Value[0], removed[0] = 'x', 'x', 'x'
			if after := mustEncodeNode(t, header, node); !bytes.Equal(after, before) {
				t.Fatal("delete result aliases input Node")
			}
		})
	}
}

func TestDeleteLeafSingleEntryAndDeepCopyHandoff(t *testing.T) {
	header := page.Header{Type: page.TypeBTreeLeaf, SpaceID: 7, PageID: 41, Generation: 3}
	node := Node{Kind: KindLeaf, NextLeafPageID: 42, LeafEntries: []LeafEntry{
		{Key: []byte("row"), Value: []byte{0, 1, 2, 0xff}},
	}}
	before := mustEncodeNode(t, header, node)
	key := []byte("row")
	got, removed, err := DeleteLeaf(header, node, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.LeafEntries) != 0 || got.Kind != KindLeaf || got.NextLeafPageID != 42 {
		t.Fatalf("empty result = %+v", got)
	}
	if !bytes.Equal(removed, []byte{0, 1, 2, 0xff}) {
		t.Fatalf("removed = %v", removed)
	}
	assertNodeRoundTrip(t, header, got)
	key[0], removed[0] = 'X', 9
	if after := mustEncodeNode(t, header, node); !bytes.Equal(after, before) {
		t.Fatal("delete output or handoff aliases input")
	}

	if _, _, err := DeleteLeaf(header, got, []byte("row")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty leaf delete error = %v, want ErrNotFound", err)
	}
}

func TestDeleteLeafRejectsAbsentInvalidAndWrongShapeAtomically(t *testing.T) {
	leafHeader := page.Header{Type: page.TypeBTreeLeaf, SpaceID: 7, PageID: 41, Generation: 3}
	internalHeader := page.Header{Type: page.TypeBTreeInternal, SpaceID: 7, PageID: 42, Generation: 3}
	node := Node{Kind: KindLeaf, NextLeafPageID: 50, LeafEntries: []LeafEntry{
		{Key: []byte("a"), Value: []byte("one")},
		{Key: []byte("c"), Value: []byte("three")},
	}}
	before := mustEncodeNode(t, leafHeader, node)
	internal := Node{Kind: KindInternal, Level: 1, LeftmostChild: 1}
	for name, test := range map[string]struct {
		header page.Header
		node   Node
		key    []byte
		want   error
	}{
		"absent middle": {leafHeader, node, []byte("b"), ErrNotFound},
		"absent back":   {leafHeader, node, []byte("z"), ErrNotFound},
		"empty key":     {leafHeader, node, nil, ErrInvalid},
		"wrong header":  {internalHeader, node, []byte("a"), ErrInvalid},
		"wrong kind":    {internalHeader, internal, []byte("a"), ErrInvalid},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := DeleteLeaf(test.header, test.node, test.key); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
	if after := mustEncodeNode(t, leafHeader, node); !bytes.Equal(after, before) {
		t.Fatal("failed delete mutated input")
	}
}

func TestDeleteLeafMatchesSeededReferenceModel(t *testing.T) {
	const seed = int64(9501)
	random := rand.New(rand.NewSource(seed))
	header := page.Header{Type: page.TypeBTreeLeaf, SpaceID: 7, PageID: 41, Generation: 3}
	node := Node{Kind: KindLeaf, NextLeafPageID: 42}
	model := map[string][]byte{}
	for step := 0; step < 2_000; step++ {
		key := fmt.Sprintf("k%02d", random.Intn(50))
		if random.Intn(3) != 0 {
			value := []byte(fmt.Sprintf("v%04d", random.Intn(10_000)))
			next, _, err := UpsertLeaf(header, node, []byte(key), value)
			if err != nil {
				t.Fatalf("seed=%d step=%d upsert: %v", seed, step, err)
			}
			node, model[key] = next, bytes.Clone(value)
		} else {
			want, exists := model[key]
			next, removed, err := DeleteLeaf(header, node, []byte(key))
			if !exists {
				if !errors.Is(err, ErrNotFound) {
					t.Fatalf("seed=%d step=%d missing delete: %v", seed, step, err)
				}
			} else {
				if err != nil || !bytes.Equal(removed, want) {
					t.Fatalf("seed=%d step=%d removed=%q want=%q err=%v", seed, step, removed, want, err)
				}
				node = next
				delete(model, key)
			}
		}
		encoded, err := Encode(header, node)
		if err != nil {
			t.Fatalf("seed=%d step=%d encode: %v", seed, step, err)
		}
		node, err = Decode(encoded)
		if err != nil {
			t.Fatalf("seed=%d step=%d decode: %v", seed, step, err)
		}
		assertDeleteReferenceModel(t, node, model, seed, step)
	}
}

func assertDeleteReferenceModel(t *testing.T, node Node, model map[string][]byte, seed int64, step int) {
	t.Helper()
	keys := make([]string, 0, len(model))
	for key := range model {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(node.LeafEntries) != len(keys) {
		t.Fatalf("seed=%d step=%d entries=%d want=%d", seed, step, len(node.LeafEntries), len(keys))
	}
	for index, key := range keys {
		entry := node.LeafEntries[index]
		if string(entry.Key) != key || !bytes.Equal(entry.Value, model[key]) {
			t.Fatalf("seed=%d step=%d entry=%d mismatch", seed, step, index)
		}
	}
}
