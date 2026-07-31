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

func TestSplitLeafMaintainsLinksSeparatorAndRoundTrips(t *testing.T) {
	leftHeader := page.Header{
		Type: page.TypeBTreeLeaf, SpaceID: 7, PageID: 11, Generation: 3,
	}
	rightHeader := page.Header{
		Type: page.TypeBTreeLeaf, SpaceID: 7, PageID: 12, Generation: 3,
	}
	value := bytes.Repeat([]byte{'v'}, 6_000)

	for _, test := range []struct {
		name string
		keys []string
		key  string
	}{
		{name: "front", keys: []string{"b", "c"}, key: "a"},
		{name: "middle", keys: []string{"a", "c"}, key: "b"},
		{name: "back", keys: []string{"a", "b"}, key: "c"},
	} {
		t.Run(test.name, func(t *testing.T) {
			node := Node{Kind: KindLeaf, NextLeafPageID: 99}
			for _, key := range test.keys {
				node.LeafEntries = append(node.LeafEntries, LeafEntry{
					Key: []byte(key), Value: bytes.Clone(value),
				})
			}
			left, right, separator, replaced, err := SplitLeaf(
				leftHeader, rightHeader, node, []byte(test.key), value,
			)
			if err != nil {
				t.Fatal(err)
			}
			if replaced {
				t.Fatal("new key reported as replacement")
			}
			if left.NextLeafPageID != rightHeader.PageID || right.NextLeafPageID != 99 {
				t.Fatalf("leaf links = %d -> %d", left.NextLeafPageID, right.NextLeafPageID)
			}
			if len(right.LeafEntries) == 0 || !bytes.Equal(separator, right.LeafEntries[0].Key) {
				t.Fatalf("separator = %q, right = %+v", separator, right.LeafEntries)
			}
			assertSplitLeafKeys(t, left, right, []string{"a", "b", "c"})
			assertNodeRoundTrip(t, leftHeader, left)
			assertNodeRoundTrip(t, rightHeader, right)
		})
	}
}

func TestSplitLeafBalancesActualBytesAndDeepCopies(t *testing.T) {
	leftHeader, rightHeader := splitLeafHeaders()
	node := Node{Kind: KindLeaf, NextLeafPageID: 99, LeafEntries: []LeafEntry{
		{Key: []byte("a"), Value: bytes.Repeat([]byte{'a'}, 7_000)},
		{Key: []byte("c"), Value: bytes.Repeat([]byte{'c'}, 100)},
		{Key: []byte("d"), Value: bytes.Repeat([]byte{'d'}, 7_000)},
	}}
	before := mustEncodeNode(t, leftHeader, node)
	key, value := []byte("b"), bytes.Repeat([]byte{'b'}, 2_500)
	left, right, separator, _, err := SplitLeaf(leftHeader, rightHeader, node, key, value)
	if err != nil {
		t.Fatal(err)
	}
	assertSplitLeafKeys(t, left, right, []string{"a", "b", "c", "d"})
	if string(separator) != "c" {
		t.Fatalf("separator = %q, want byte-balanced cut before c", separator)
	}
	key[0], value[0], separator[0] = 'x', 'x', 'x'
	left.LeafEntries[0].Key[0] = 'y'
	right.LeafEntries[0].Key[0] = 'z'
	if after := mustEncodeNode(t, leftHeader, node); !bytes.Equal(after, before) {
		t.Fatal("split aliases or mutates input Node")
	}
}

func TestSplitInternalPromotesPivotAndPreservesChildMapping(t *testing.T) {
	leftHeader := page.Header{
		Type: page.TypeBTreeInternal, SpaceID: 7, PageID: 21, Generation: 3,
	}
	rightHeader := page.Header{
		Type: page.TypeBTreeInternal, SpaceID: 7, PageID: 22, Generation: 3,
	}
	node := Node{Kind: KindInternal, Level: 2, LeftmostChild: 10}
	for index, key := range []byte{'a', 'c', 'd'} {
		node.InternalEntries = append(node.InternalEntries, InternalEntry{
			Key:        append([]byte{key}, bytes.Repeat([]byte{key}, 4_099)...),
			RightChild: uint64(11 + index),
		})
	}
	pending := append([]byte{'b'}, bytes.Repeat([]byte{'b'}, 4_099)...)
	originalBytes := mustEncodeNode(t, leftHeader, node)
	left, right, promoted, replaced, err := SplitInternal(
		leftHeader, rightHeader, node, pending, 20,
	)
	if err != nil {
		t.Fatal(err)
	}
	if replaced || left.Kind != KindInternal || right.Kind != KindInternal ||
		left.Level != 2 || right.Level != 2 || left.LeftmostChild != 10 ||
		right.LeftmostChild != 20 {
		t.Fatalf("split metadata = left %+v, right %+v, replaced %v", left, right, replaced)
	}
	if promoted[0] != 'b' || len(left.InternalEntries) != 1 ||
		left.InternalEntries[0].Key[0] != 'a' || len(right.InternalEntries) != 2 ||
		right.InternalEntries[0].Key[0] != 'c' || right.InternalEntries[1].Key[0] != 'd' {
		t.Fatalf("pivot/entries = %q / %v / %v", promoted[:1], left.InternalEntries, right.InternalEntries)
	}
	if left.InternalEntries[0].RightChild != 11 ||
		right.InternalEntries[0].RightChild != 12 || right.InternalEntries[1].RightChild != 13 {
		t.Fatalf("child mapping = %+v / %+v", left.InternalEntries, right.InternalEntries)
	}
	assertNodeRoundTrip(t, leftHeader, left)
	assertNodeRoundTrip(t, rightHeader, right)
	pending[0], promoted[0] = 'x', 'x'
	left.InternalEntries[0].Key[0] = 'y'
	right.InternalEntries[0].Key[0] = 'z'
	if after := mustEncodeNode(t, leftHeader, node); !bytes.Equal(after, originalBytes) {
		t.Fatal("internal split aliases or mutates input Node")
	}
}

func TestGrowRootBuildsEncodableParentAndRejectsIdentityErrors(t *testing.T) {
	header := page.Header{
		Type: page.TypeBTreeInternal, SpaceID: 7, PageID: 30, Generation: 3,
	}
	separator := []byte("middle")
	root, err := GrowRoot(header, 11, 12, 2, separator)
	if err != nil {
		t.Fatal(err)
	}
	if root.Kind != KindInternal || root.Level != 3 || root.LeftmostChild != 11 ||
		len(root.InternalEntries) != 1 || root.InternalEntries[0].RightChild != 12 ||
		string(root.InternalEntries[0].Key) != "middle" {
		t.Fatalf("root = %+v", root)
	}
	separator[0] = 'x'
	if string(root.InternalEntries[0].Key) != "middle" {
		t.Fatal("root separator aliases caller input")
	}
	assertNodeRoundTrip(t, header, root)

	for name, run := range map[string]func() error{
		"root is left":    func() error { _, err := GrowRoot(header, 30, 12, 0, []byte("k")); return err },
		"root is right":   func() error { _, err := GrowRoot(header, 11, 30, 0, []byte("k")); return err },
		"same children":   func() error { _, err := GrowRoot(header, 11, 11, 0, []byte("k")); return err },
		"zero child":      func() error { _, err := GrowRoot(header, 0, 12, 0, []byte("k")); return err },
		"empty separator": func() error { _, err := GrowRoot(header, 11, 12, 0, nil); return err },
		"level overflow": func() error {
			_, err := GrowRoot(header, 11, 12, ^uint16(0), []byte("k"))
			return err
		},
		"wrong header": func() error {
			wrong := header
			wrong.Type = page.TypeBTreeLeaf
			_, err := GrowRoot(wrong, 11, 12, 0, []byte("k"))
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := run(); !errors.Is(err, ErrInvalid) {
				t.Fatalf("error = %v, want ErrInvalid", err)
			}
		})
	}
	if _, err := GrowRoot(header, 11, 12, 0, bytes.Repeat([]byte{'k'}, page.Size)); !errors.Is(err, ErrNoSpace) {
		t.Fatalf("oversize root error = %v, want ErrNoSpace", err)
	}
}

func TestSplitRejectsNotRequiredNoLegalCutAndWrongHeadersAtomically(t *testing.T) {
	leftHeader, rightHeader := splitLeafHeaders()
	small := Node{Kind: KindLeaf, LeafEntries: []LeafEntry{{Key: []byte("a"), Value: []byte("1")}}}
	if _, _, _, _, err := SplitLeaf(leftHeader, rightHeader, small, []byte("b"), []byte("2")); !errors.Is(err, ErrSplitNotRequired) {
		t.Fatalf("small split error = %v, want ErrSplitNotRequired", err)
	}

	original := Node{Kind: KindLeaf, LeafEntries: []LeafEntry{
		{Key: []byte("a"), Value: bytes.Repeat([]byte{'a'}, 8_000)},
		{Key: []byte("c"), Value: bytes.Repeat([]byte{'c'}, 8_000)},
	}}
	before := mustEncodeNode(t, leftHeader, original)
	if _, _, _, _, err := SplitLeaf(
		leftHeader, rightHeader, original, []byte("b"), bytes.Repeat([]byte{'b'}, 9_000),
	); !errors.Is(err, ErrNoSpace) {
		t.Fatalf("no legal cut error = %v, want ErrNoSpace", err)
	}
	if after := mustEncodeNode(t, leftHeader, original); !bytes.Equal(after, before) {
		t.Fatal("failed split mutated input")
	}
	if _, _, _, _, err := SplitLeaf(
		leftHeader, rightHeader, small, []byte("b"), make([]byte, page.Size),
	); !errors.Is(err, ErrNoSpace) {
		t.Fatalf("oversize error = %v, want ErrNoSpace", err)
	}

	for name, mutate := range map[string]func(*page.Header){
		"wrong type":       func(header *page.Header) { header.Type = page.TypeBTreeInternal },
		"same page":        func(header *page.Header) { header.PageID = leftHeader.PageID },
		"other space":      func(header *page.Header) { header.SpaceID++ },
		"other generation": func(header *page.Header) { header.Generation++ },
	} {
		t.Run(name, func(t *testing.T) {
			wrong := rightHeader
			mutate(&wrong)
			if _, _, _, _, err := SplitLeaf(leftHeader, wrong, small, []byte("b"), []byte("2")); !errors.Is(err, ErrInvalid) {
				t.Fatalf("error = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestSplitLeafReplacementAndSeededReferenceModel(t *testing.T) {
	leftHeader, rightHeader := splitLeafHeaders()
	replacementBase := Node{Kind: KindLeaf, LeafEntries: []LeafEntry{
		{Key: []byte("a"), Value: bytes.Repeat([]byte{'a'}, 8_000)},
		{Key: []byte("c"), Value: bytes.Repeat([]byte{'c'}, 8_000)},
	}}
	left, right, _, replaced, err := SplitLeaf(
		leftHeader, rightHeader, replacementBase, []byte("a"), bytes.Repeat([]byte{'x'}, 9_000),
	)
	if err != nil || !replaced {
		t.Fatalf("replacement split = %v, replaced %v", err, replaced)
	}
	if len(left.LeafEntries)+len(right.LeafEntries) != 2 ||
		len(left.LeafEntries[0].Value) != 9_000 {
		t.Fatal("replacement was not preserved exactly once")
	}

	const seed = int64(9401)
	random := rand.New(rand.NewSource(seed))
	for trial := 0; trial < 20; trial++ {
		node := Node{Kind: KindLeaf}
		model := map[string][]byte{}
		for step := 0; ; step++ {
			key := fmt.Sprintf("%04d", step)
			value := bytes.Repeat([]byte{byte(random.Intn(251) + 1)}, 500+random.Intn(1_001))
			next, _, upsertErr := UpsertLeaf(leftHeader, node, []byte(key), value)
			if upsertErr == nil {
				node, model[key] = next, bytes.Clone(value)
				continue
			}
			if !errors.Is(upsertErr, ErrNoSpace) {
				t.Fatalf("seed=%d trial=%d step=%d: %v", seed, trial, step, upsertErr)
			}
			model[key] = bytes.Clone(value)
			left, right, _, _, splitErr := SplitLeaf(leftHeader, rightHeader, node, []byte(key), value)
			if splitErr != nil {
				t.Fatalf("seed=%d trial=%d step=%d split: %v", seed, trial, step, splitErr)
			}
			assertLeafReferenceModel(t, left, right, model, seed, trial)
			assertNodeRoundTrip(t, leftHeader, left)
			assertNodeRoundTrip(t, rightHeader, right)
			break
		}
	}
}

func TestSplitInternalMatchesSeededReferenceModel(t *testing.T) {
	const seed = int64(9402)
	random := rand.New(rand.NewSource(seed))
	leftHeader := page.Header{
		Type: page.TypeBTreeInternal, SpaceID: 7, PageID: 21, Generation: 3,
	}
	rightHeader := page.Header{
		Type: page.TypeBTreeInternal, SpaceID: 7, PageID: 22, Generation: 3,
	}
	for trial := 0; trial < 20; trial++ {
		node := Node{Kind: KindInternal, Level: 2, LeftmostChild: 100}
		keys := make([][]byte, 0)
		children := []uint64{100}
		for step := 0; ; step++ {
			prefix := fmt.Sprintf("%04d-", step)
			key := append([]byte(prefix), bytes.Repeat(
				[]byte{byte('a' + random.Intn(26))}, 500+random.Intn(1_001),
			)...)
			child := uint64(101 + step)
			next, _, upsertErr := UpsertInternal(leftHeader, node, key, child)
			if upsertErr == nil {
				node = next
				keys = append(keys, bytes.Clone(key))
				children = append(children, child)
				continue
			}
			if !errors.Is(upsertErr, ErrNoSpace) {
				t.Fatalf("seed=%d trial=%d step=%d: %v", seed, trial, step, upsertErr)
			}
			keys = append(keys, bytes.Clone(key))
			children = append(children, child)
			left, right, promoted, _, splitErr := SplitInternal(
				leftHeader, rightHeader, node, key, child,
			)
			if splitErr != nil {
				t.Fatalf("seed=%d trial=%d step=%d split: %v", seed, trial, step, splitErr)
			}
			assertInternalReferenceModel(t, left, right, promoted, keys, children, seed, trial)
			assertNodeRoundTrip(t, leftHeader, left)
			assertNodeRoundTrip(t, rightHeader, right)
			break
		}
	}
}

func assertSplitLeafKeys(t *testing.T, left Node, right Node, want []string) {
	t.Helper()
	entries := append(append([]LeafEntry(nil), left.LeafEntries...), right.LeafEntries...)
	if len(entries) != len(want) {
		t.Fatalf("entry count = %d, want %d", len(entries), len(want))
	}
	for index := range want {
		if string(entries[index].Key) != want[index] {
			t.Fatalf("entry %d key = %q, want %q", index, entries[index].Key, want[index])
		}
	}
}

func assertNodeRoundTrip(t *testing.T, header page.Header, node Node) {
	t.Helper()
	encoded, err := Encode(header, node)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if _, err := Decode(encoded); err != nil {
		t.Fatalf("Decode: %v", err)
	}
}

func mustEncodeNode(t *testing.T, header page.Header, node Node) []byte {
	t.Helper()
	value, err := Encode(header, node)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := page.Encode(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func splitLeafHeaders() (page.Header, page.Header) {
	return page.Header{Type: page.TypeBTreeLeaf, SpaceID: 7, PageID: 11, Generation: 3},
		page.Header{Type: page.TypeBTreeLeaf, SpaceID: 7, PageID: 12, Generation: 3}
}

func assertLeafReferenceModel(
	t *testing.T,
	left Node,
	right Node,
	model map[string][]byte,
	seed int64,
	trial int,
) {
	t.Helper()
	entries := append(append([]LeafEntry(nil), left.LeafEntries...), right.LeafEntries...)
	keys := make([]string, 0, len(model))
	for key := range model {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(entries) != len(keys) {
		t.Fatalf("seed=%d trial=%d entries=%d want=%d", seed, trial, len(entries), len(keys))
	}
	for index, key := range keys {
		if string(entries[index].Key) != key || !bytes.Equal(entries[index].Value, model[key]) {
			t.Fatalf("seed=%d trial=%d entry=%d mismatch", seed, trial, index)
		}
	}
}

func assertInternalReferenceModel(
	t *testing.T,
	left Node,
	right Node,
	promoted []byte,
	wantKeys [][]byte,
	wantChildren []uint64,
	seed int64,
	trial int,
) {
	t.Helper()
	keys := make([][]byte, 0, len(wantKeys))
	children := []uint64{left.LeftmostChild}
	for _, entry := range left.InternalEntries {
		keys = append(keys, entry.Key)
		children = append(children, entry.RightChild)
	}
	keys = append(keys, promoted)
	children = append(children, right.LeftmostChild)
	for _, entry := range right.InternalEntries {
		keys = append(keys, entry.Key)
		children = append(children, entry.RightChild)
	}
	if len(keys) != len(wantKeys) || len(children) != len(wantChildren) {
		t.Fatalf("seed=%d trial=%d reconstructed lengths = %d/%d", seed, trial, len(keys), len(children))
	}
	for index := range wantKeys {
		if !bytes.Equal(keys[index], wantKeys[index]) {
			t.Fatalf("seed=%d trial=%d key=%d mismatch", seed, trial, index)
		}
	}
	for index := range wantChildren {
		if children[index] != wantChildren[index] {
			t.Fatalf("seed=%d trial=%d child=%d got=%d want=%d", seed, trial, index, children[index], wantChildren[index])
		}
	}
}
