package btree

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand"
	"testing"

	"github.com/HW-Yue/Memora/internal/store/page"
)

func TestRebalanceLeafMergeUpdatesParentAndLeafChain(t *testing.T) {
	parentHeader, leftHeader, rightHeader := rebalanceHeaders(1, 2, 3)
	parent := Node{Kind: KindInternal, Level: 1, LeftmostChild: 2, InternalEntries: []InternalEntry{
		{Key: []byte("m"), RightChild: 3},
		{Key: []byte("z"), RightChild: 4},
	}}
	left := Node{Kind: KindLeaf, NextLeafPageID: 3, LeafEntries: []LeafEntry{
		{Key: []byte("a"), Value: []byte("1")},
		{Key: []byte("b"), Value: []byte("2")},
	}}
	right := Node{Kind: KindLeaf, NextLeafPageID: 4, LeafEntries: []LeafEntry{
		{Key: []byte("m"), Value: []byte("3")},
		{Key: []byte("n"), Value: []byte("4")},
	}}

	got, err := RebalanceChildren(
		parentHeader, parent, 0, leftHeader, left, rightHeader, right,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != RebalanceMerged || got.RemovedPageID != 3 || got.Left.NextLeafPageID != 4 {
		t.Fatalf("action/removed/next = %d/%d/%d", got.Action, got.RemovedPageID, got.Left.NextLeafPageID)
	}
	assertLeafKeys(t, got.Left, []string{"a", "b", "m", "n"})
	if got.Parent.LeftmostChild != 2 || len(got.Parent.InternalEntries) != 1 ||
		string(got.Parent.InternalEntries[0].Key) != "z" || got.Parent.InternalEntries[0].RightChild != 4 {
		t.Fatalf("parent = %+v", got.Parent)
	}
	if got.Right.Kind != 0 {
		t.Fatalf("merged right = %+v, want zero handoff", got.Right)
	}
	assertNodeRoundTrip(t, parentHeader, got.Parent)
	assertNodeRoundTrip(t, leftHeader, got.Left)
}

func TestRebalanceRejectsIdentityShapeBoundaryAndLinkErrorsAtomically(t *testing.T) {
	parentHeader, leftHeader, rightHeader := rebalanceHeaders(1, 2, 3)
	parent := Node{Kind: KindInternal, Level: 1, LeftmostChild: 2, InternalEntries: []InternalEntry{
		{Key: []byte("m"), RightChild: 3},
	}}
	left := Node{Kind: KindLeaf, NextLeafPageID: 3, LeafEntries: []LeafEntry{
		{Key: []byte("a"), Value: []byte("1")},
	}}
	right := Node{Kind: KindLeaf, LeafEntries: []LeafEntry{
		{Key: []byte("n"), Value: []byte("2")},
	}}
	parentBefore := mustEncodeNode(t, parentHeader, parent)
	leftBefore := mustEncodeNode(t, leftHeader, left)
	rightBefore := mustEncodeNode(t, rightHeader, right)

	tests := map[string]func() error{
		"negative index": func() error {
			_, err := RebalanceChildren(parentHeader, parent, -1, leftHeader, left, rightHeader, right)
			return err
		},
		"index overflow": func() error {
			_, err := RebalanceChildren(parentHeader, parent, 1, leftHeader, left, rightHeader, right)
			return err
		},
		"other space": func() error {
			wrong := rightHeader
			wrong.SpaceID++
			_, err := RebalanceChildren(parentHeader, parent, 0, leftHeader, left, wrong, right)
			return err
		},
		"other generation": func() error {
			wrong := rightHeader
			wrong.Generation++
			_, err := RebalanceChildren(parentHeader, parent, 0, leftHeader, left, wrong, right)
			return err
		},
		"wrong child type": func() error {
			wrong := rightHeader
			wrong.Type = page.TypeBTreeInternal
			_, err := RebalanceChildren(parentHeader, parent, 0, leftHeader, left, wrong, right)
			return err
		},
		"wrong level": func() error {
			wrong := right
			wrong.Level = 1
			_, err := RebalanceChildren(parentHeader, parent, 0, leftHeader, left, rightHeader, wrong)
			return err
		},
		"wrong child identity": func() error {
			wrong := parent
			wrong.InternalEntries = cloneInternalEntries(parent.InternalEntries)
			wrong.InternalEntries[0].RightChild = 9
			_, err := RebalanceChildren(parentHeader, wrong, 0, leftHeader, left, rightHeader, right)
			return err
		},
		"broken leaf link": func() error {
			wrong := left
			wrong.NextLeafPageID = 9
			_, err := RebalanceChildren(parentHeader, parent, 0, leftHeader, wrong, rightHeader, right)
			return err
		},
		"left boundary": func() error {
			wrong := parent
			wrong.InternalEntries = cloneInternalEntries(parent.InternalEntries)
			wrong.InternalEntries[0].Key = []byte("a")
			_, err := RebalanceChildren(parentHeader, wrong, 0, leftHeader, left, rightHeader, right)
			return err
		},
		"right boundary": func() error {
			wrong := parent
			wrong.InternalEntries = cloneInternalEntries(parent.InternalEntries)
			wrong.InternalEntries[0].Key = []byte("z")
			_, err := RebalanceChildren(parentHeader, wrong, 0, leftHeader, left, rightHeader, right)
			return err
		},
	}
	for name, run := range tests {
		t.Run(name, func(t *testing.T) {
			if err := run(); !errors.Is(err, ErrInvalid) {
				t.Fatalf("error = %v, want ErrInvalid", err)
			}
		})
	}
	if !bytes.Equal(mustEncodeNode(t, parentHeader, parent), parentBefore) ||
		!bytes.Equal(mustEncodeNode(t, leftHeader, left), leftBefore) ||
		!bytes.Equal(mustEncodeNode(t, rightHeader, right), rightBefore) {
		t.Fatal("failed rebalance mutated an input")
	}
}

func TestRebalanceRejectsInternalKeyBoundary(t *testing.T) {
	parentHeader, leftHeader, rightHeader := rebalanceInternalHeaders(1, 2, 3)
	parent := Node{Kind: KindInternal, Level: 2, LeftmostChild: 2, InternalEntries: []InternalEntry{
		{Key: []byte("m"), RightChild: 3},
	}}
	validLeft := Node{Kind: KindInternal, Level: 1, LeftmostChild: 10, InternalEntries: []InternalEntry{
		{Key: []byte("a"), RightChild: 11},
	}}
	validRight := Node{Kind: KindInternal, Level: 1, LeftmostChild: 20, InternalEntries: []InternalEntry{
		{Key: []byte("z"), RightChild: 21},
	}}
	leftEqual := cloneNode(validLeft)
	leftEqual.InternalEntries[0].Key = []byte("m")
	rightEqual := cloneNode(validRight)
	rightEqual.InternalEntries[0].Key = []byte("m")
	for name, pair := range map[string][2]Node{
		"left equal separator":  {leftEqual, validRight},
		"right equal separator": {validLeft, rightEqual},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := RebalanceChildren(
				parentHeader, parent, 0, leftHeader, pair[0], rightHeader, pair[1],
			); !errors.Is(err, ErrInvalid) {
				t.Fatalf("error = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestRebalanceOutputsDeepCopyInputs(t *testing.T) {
	parentHeader, leftHeader, rightHeader := rebalanceHeaders(1, 2, 3)
	parent := Node{Kind: KindInternal, Level: 1, LeftmostChild: 2, InternalEntries: []InternalEntry{
		{Key: []byte("m"), RightChild: 3},
	}}
	left := Node{Kind: KindLeaf, NextLeafPageID: 3, LeafEntries: []LeafEntry{
		{Key: []byte("a"), Value: []byte("1")},
	}}
	right := Node{Kind: KindLeaf, LeafEntries: []LeafEntry{
		{Key: []byte("n"), Value: []byte("2")},
	}}
	parentBefore := mustEncodeNode(t, parentHeader, parent)
	leftBefore := mustEncodeNode(t, leftHeader, left)
	rightBefore := mustEncodeNode(t, rightHeader, right)
	got, err := RebalanceChildren(parentHeader, parent, 0, leftHeader, left, rightHeader, right)
	if err != nil {
		t.Fatal(err)
	}
	got.Left.LeafEntries[0].Key[0], got.Left.LeafEntries[1].Value[0] = 'x', 'x'
	if !bytes.Equal(mustEncodeNode(t, parentHeader, parent), parentBefore) ||
		!bytes.Equal(mustEncodeNode(t, leftHeader, left), leftBefore) ||
		!bytes.Equal(mustEncodeNode(t, rightHeader, right), rightBefore) {
		t.Fatal("rebalance output aliases an input")
	}
}

func TestRebalanceLeafMatchesSeededReferenceCorpus(t *testing.T) {
	const seed = int64(9601)
	random := rand.New(rand.NewSource(seed))
	parentHeader, leftHeader, rightHeader := rebalanceHeaders(1, 2, 3)
	for trial := 0; trial < 100; trial++ {
		count := 4 + random.Intn(9)
		entries := make([]LeafEntry, count)
		for index := range entries {
			key := fmt.Sprintf("k%02d", index)
			entries[index] = LeafEntry{
				Key:   []byte(key),
				Value: bytes.Repeat([]byte{byte(index + 1)}, 200+random.Intn(3_501)),
			}
		}
		cut := 1 + random.Intn(count-1)
		left := Node{Kind: KindLeaf, NextLeafPageID: 3, LeafEntries: cloneLeafEntries(entries[:cut])}
		right := Node{Kind: KindLeaf, NextLeafPageID: 4, LeafEntries: cloneLeafEntries(entries[cut:])}
		if _, err := Encode(leftHeader, left); err != nil {
			trial--
			continue
		}
		if _, err := Encode(rightHeader, right); err != nil {
			trial--
			continue
		}
		parent := Node{Kind: KindInternal, Level: 1, LeftmostChild: 2, InternalEntries: []InternalEntry{
			{Key: bytes.Clone(right.LeafEntries[0].Key), RightChild: 3},
			{Key: []byte("z"), RightChild: 4},
		}}
		got, err := RebalanceChildren(parentHeader, parent, 0, leftHeader, left, rightHeader, right)
		if err != nil {
			t.Fatalf("seed=%d trial=%d: %v", seed, trial, err)
		}
		actual := got.Left.LeafEntries
		if got.Action == RebalanceRedistributed {
			actual = append(append([]LeafEntry(nil), actual...), got.Right.LeafEntries...)
			if !bytes.Equal(got.Parent.InternalEntries[0].Key, got.Right.LeafEntries[0].Key) ||
				got.Left.NextLeafPageID != 3 || got.Right.NextLeafPageID != 4 {
				t.Fatalf("seed=%d trial=%d redistributed metadata", seed, trial)
			}
		} else if got.Action != RebalanceMerged || got.Left.NextLeafPageID != 4 || got.RemovedPageID != 3 {
			t.Fatalf("seed=%d trial=%d merge metadata", seed, trial)
		}
		assertLeafEntriesEqual(t, actual, entries, seed, trial)
		assertNodeRoundTrip(t, parentHeader, got.Parent)
		assertNodeRoundTrip(t, leftHeader, got.Left)
		if got.Action == RebalanceRedistributed {
			assertNodeRoundTrip(t, rightHeader, got.Right)
		}
	}
}

func TestRebalanceInternalMatchesSeededReferenceCorpus(t *testing.T) {
	const seed = int64(9602)
	random := rand.New(rand.NewSource(seed))
	parentHeader, leftHeader, rightHeader := rebalanceInternalHeaders(1, 2, 3)
	for trial := 0; trial < 100; trial++ {
		count := 5 + random.Intn(7)
		keys := make([][]byte, count)
		children := make([]uint64, count+1)
		for index := range children {
			children[index] = uint64(100 + index)
		}
		for index := range keys {
			prefix := fmt.Sprintf("k%02d-", index)
			keys[index] = append([]byte(prefix), bytes.Repeat(
				[]byte{byte('a' + index)}, 200+random.Intn(3_501),
			)...)
		}
		pivot := 1 + random.Intn(count-2)
		left := Node{Kind: KindInternal, Level: 1, LeftmostChild: children[0]}
		for index := 0; index < pivot; index++ {
			left.InternalEntries = append(left.InternalEntries, InternalEntry{
				Key: bytes.Clone(keys[index]), RightChild: children[index+1],
			})
		}
		right := Node{Kind: KindInternal, Level: 1, LeftmostChild: children[pivot+1]}
		for index := pivot + 1; index < count; index++ {
			right.InternalEntries = append(right.InternalEntries, InternalEntry{
				Key: bytes.Clone(keys[index]), RightChild: children[index+1],
			})
		}
		if _, err := Encode(leftHeader, left); err != nil {
			trial--
			continue
		}
		if _, err := Encode(rightHeader, right); err != nil {
			trial--
			continue
		}
		parent := Node{Kind: KindInternal, Level: 2, LeftmostChild: 2, InternalEntries: []InternalEntry{
			{Key: bytes.Clone(keys[pivot]), RightChild: 3},
			{Key: []byte("z"), RightChild: 4},
		}}
		got, err := RebalanceChildren(parentHeader, parent, 0, leftHeader, left, rightHeader, right)
		if err != nil {
			t.Fatalf("seed=%d trial=%d: %v", seed, trial, err)
		}
		actualKeys, actualChildren := rebuildRebalancedInternal(got)
		if len(actualKeys) != len(keys) || len(actualChildren) != len(children) {
			t.Fatalf("seed=%d trial=%d reconstructed shape", seed, trial)
		}
		for index := range keys {
			if !bytes.Equal(actualKeys[index], keys[index]) {
				t.Fatalf("seed=%d trial=%d key=%d mismatch", seed, trial, index)
			}
		}
		for index := range children {
			if actualChildren[index] != children[index] {
				t.Fatalf("seed=%d trial=%d child=%d mismatch", seed, trial, index)
			}
		}
		assertNodeRoundTrip(t, parentHeader, got.Parent)
		assertNodeRoundTrip(t, leftHeader, got.Left)
		if got.Action == RebalanceRedistributed {
			assertNodeRoundTrip(t, rightHeader, got.Right)
		}
	}
}

func TestRebalanceLeafRedistributesByBytesAndUpdatesSeparator(t *testing.T) {
	parentHeader, leftHeader, rightHeader := rebalanceHeaders(1, 2, 3)
	parent := Node{Kind: KindInternal, Level: 1, LeftmostChild: 2, InternalEntries: []InternalEntry{
		{Key: []byte("b"), RightChild: 3},
	}}
	left := Node{Kind: KindLeaf, NextLeafPageID: 3, LeafEntries: []LeafEntry{
		{Key: []byte("a"), Value: bytes.Repeat([]byte{'a'}, 7_000)},
	}}
	right := Node{Kind: KindLeaf, NextLeafPageID: 4, LeafEntries: []LeafEntry{
		{Key: []byte("b"), Value: bytes.Repeat([]byte{'b'}, 2_500)},
		{Key: []byte("c"), Value: bytes.Repeat([]byte{'c'}, 100)},
		{Key: []byte("d"), Value: bytes.Repeat([]byte{'d'}, 7_000)},
	}}
	parentBefore := mustEncodeNode(t, parentHeader, parent)
	leftBefore := mustEncodeNode(t, leftHeader, left)
	rightBefore := mustEncodeNode(t, rightHeader, right)

	got, err := RebalanceChildren(
		parentHeader, parent, 0, leftHeader, left, rightHeader, right,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != RebalanceRedistributed || got.RemovedPageID != 0 ||
		string(got.Parent.InternalEntries[0].Key) != "c" {
		t.Fatalf("action/removed/parent = %d/%d/%+v", got.Action, got.RemovedPageID, got.Parent)
	}
	assertLeafKeys(t, got.Left, []string{"a", "b"})
	assertLeafKeys(t, got.Right, []string{"c", "d"})
	if got.Left.NextLeafPageID != 3 || got.Right.NextLeafPageID != 4 {
		t.Fatalf("links = %d/%d", got.Left.NextLeafPageID, got.Right.NextLeafPageID)
	}
	assertNodeRoundTrip(t, parentHeader, got.Parent)
	assertNodeRoundTrip(t, leftHeader, got.Left)
	assertNodeRoundTrip(t, rightHeader, got.Right)
	got.Parent.InternalEntries[0].Key[0] = 'x'
	got.Left.LeafEntries[0].Value[0] = 'x'
	got.Right.LeafEntries[0].Value[0] = 'x'
	if !bytes.Equal(mustEncodeNode(t, parentHeader, parent), parentBefore) ||
		!bytes.Equal(mustEncodeNode(t, leftHeader, left), leftBefore) ||
		!bytes.Equal(mustEncodeNode(t, rightHeader, right), rightBefore) {
		t.Fatal("redistribution output aliases an input")
	}
}

func TestShrinkRootReturnsOnlyChildAndRejectsOtherShapes(t *testing.T) {
	header := page.Header{Type: page.TypeBTreeInternal, SpaceID: 7, PageID: 1, Generation: 3}
	root := Node{Kind: KindInternal, Level: 1, LeftmostChild: 2}
	child, err := ShrinkRoot(header, root)
	if err != nil || child != 2 {
		t.Fatalf("ShrinkRoot = %d, %v", child, err)
	}
	nonEmpty := root
	nonEmpty.InternalEntries = []InternalEntry{{Key: []byte("m"), RightChild: 3}}
	if _, err := ShrinkRoot(header, nonEmpty); !errors.Is(err, ErrRebalanceNotRequired) {
		t.Fatalf("non-empty root error = %v", err)
	}
	leafHeader := header
	leafHeader.Type = page.TypeBTreeLeaf
	if _, err := ShrinkRoot(leafHeader, Node{Kind: KindLeaf}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("leaf root error = %v", err)
	}
	sameIdentity := root
	sameIdentity.LeftmostChild = header.PageID
	if _, err := ShrinkRoot(header, sameIdentity); !errors.Is(err, ErrInvalid) {
		t.Fatalf("same identity error = %v", err)
	}
}

func TestRebalanceInternalMergePreservesAllChildren(t *testing.T) {
	parentHeader, leftHeader, rightHeader := rebalanceInternalHeaders(1, 2, 3)
	parent := Node{Kind: KindInternal, Level: 2, LeftmostChild: 2, InternalEntries: []InternalEntry{
		{Key: []byte("m"), RightChild: 3},
		{Key: []byte("z"), RightChild: 4},
	}}
	left := Node{Kind: KindInternal, Level: 1, LeftmostChild: 10, InternalEntries: []InternalEntry{
		{Key: []byte("a"), RightChild: 11},
		{Key: []byte("b"), RightChild: 12},
	}}
	right := Node{Kind: KindInternal, Level: 1, LeftmostChild: 20, InternalEntries: []InternalEntry{
		{Key: []byte("n"), RightChild: 21},
		{Key: []byte("o"), RightChild: 22},
	}}
	got, err := RebalanceChildren(parentHeader, parent, 0, leftHeader, left, rightHeader, right)
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != RebalanceMerged || got.RemovedPageID != 3 ||
		got.Left.Level != 1 || got.Left.LeftmostChild != 10 {
		t.Fatalf("merge metadata = %+v", got)
	}
	assertInternalKeysAndChildren(t, got.Left,
		[]string{"a", "b", "m", "n", "o"}, []uint64{10, 11, 12, 20, 21, 22})
	if len(got.Parent.InternalEntries) != 1 || string(got.Parent.InternalEntries[0].Key) != "z" {
		t.Fatalf("parent = %+v", got.Parent)
	}
	assertNodeRoundTrip(t, parentHeader, got.Parent)
	assertNodeRoundTrip(t, leftHeader, got.Left)
}

func TestRebalanceInternalRedistributesPivotAndChildMapping(t *testing.T) {
	parentHeader, leftHeader, rightHeader := rebalanceInternalHeaders(1, 2, 3)
	parent := Node{Kind: KindInternal, Level: 2, LeftmostChild: 2, InternalEntries: []InternalEntry{
		{Key: append([]byte{'b'}, bytes.Repeat([]byte{'b'}, 4_099)...), RightChild: 3},
	}}
	left := Node{Kind: KindInternal, Level: 1, LeftmostChild: 10, InternalEntries: []InternalEntry{
		{Key: append([]byte{'a'}, bytes.Repeat([]byte{'a'}, 4_099)...), RightChild: 11},
	}}
	right := Node{Kind: KindInternal, Level: 1, LeftmostChild: 20, InternalEntries: []InternalEntry{
		{Key: append([]byte{'c'}, bytes.Repeat([]byte{'c'}, 99)...), RightChild: 21},
		{Key: append([]byte{'d'}, bytes.Repeat([]byte{'d'}, 8_199)...), RightChild: 22},
	}}
	parentBefore := mustEncodeNode(t, parentHeader, parent)
	leftBefore := mustEncodeNode(t, leftHeader, left)
	rightBefore := mustEncodeNode(t, rightHeader, right)
	got, err := RebalanceChildren(parentHeader, parent, 0, leftHeader, left, rightHeader, right)
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != RebalanceRedistributed || got.RemovedPageID != 0 ||
		got.Parent.InternalEntries[0].Key[0] != 'c' || got.Right.LeftmostChild != 21 {
		t.Fatalf("action/removed/separator/right-leftmost = %d/%d/%q/%d",
			got.Action, got.RemovedPageID, got.Parent.InternalEntries[0].Key[:1], got.Right.LeftmostChild)
	}
	assertInternalKeysAndChildren(t, got.Left,
		[]string{"a", "b"}, []uint64{10, 11, 20})
	assertInternalKeysAndChildren(t, got.Right,
		[]string{"d"}, []uint64{21, 22})
	assertNodeRoundTrip(t, parentHeader, got.Parent)
	assertNodeRoundTrip(t, leftHeader, got.Left)
	assertNodeRoundTrip(t, rightHeader, got.Right)
	got.Parent.InternalEntries[0].Key[0] = 'x'
	got.Left.InternalEntries[0].Key[0] = 'x'
	got.Right.InternalEntries[0].Key[0] = 'x'
	if !bytes.Equal(mustEncodeNode(t, parentHeader, parent), parentBefore) ||
		!bytes.Equal(mustEncodeNode(t, leftHeader, left), leftBefore) ||
		!bytes.Equal(mustEncodeNode(t, rightHeader, right), rightBefore) {
		t.Fatal("internal redistribution output aliases an input")
	}
}

func rebalanceHeaders(parentID, leftID, rightID uint64) (page.Header, page.Header, page.Header) {
	return page.Header{Type: page.TypeBTreeInternal, SpaceID: 7, PageID: parentID, Generation: 3},
		page.Header{Type: page.TypeBTreeLeaf, SpaceID: 7, PageID: leftID, Generation: 3},
		page.Header{Type: page.TypeBTreeLeaf, SpaceID: 7, PageID: rightID, Generation: 3}
}

func rebalanceInternalHeaders(parentID, leftID, rightID uint64) (page.Header, page.Header, page.Header) {
	parent, left, right := rebalanceHeaders(parentID, leftID, rightID)
	left.Type, right.Type = page.TypeBTreeInternal, page.TypeBTreeInternal
	return parent, left, right
}

func assertInternalKeysAndChildren(t *testing.T, node Node, wantKeys []string, wantChildren []uint64) {
	t.Helper()
	if len(node.InternalEntries) != len(wantKeys) || len(wantChildren) != len(wantKeys)+1 {
		t.Fatalf("shape = %d entries, %d children", len(node.InternalEntries), len(wantChildren))
	}
	children := []uint64{node.LeftmostChild}
	for index, entry := range node.InternalEntries {
		if len(entry.Key) == 0 || string(entry.Key[:1]) != wantKeys[index] {
			t.Fatalf("key %d = %q, want prefix %q", index, entry.Key, wantKeys[index])
		}
		children = append(children, entry.RightChild)
	}
	for index := range children {
		if children[index] != wantChildren[index] {
			t.Fatalf("child %d = %d, want %d", index, children[index], wantChildren[index])
		}
	}
}

func assertLeafEntriesEqual(t *testing.T, got, want []LeafEntry, seed int64, trial int) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("seed=%d trial=%d leaf entries=%d want=%d", seed, trial, len(got), len(want))
	}
	for index := range want {
		if !bytes.Equal(got[index].Key, want[index].Key) ||
			!bytes.Equal(got[index].Value, want[index].Value) {
			t.Fatalf("seed=%d trial=%d leaf entry=%d mismatch", seed, trial, index)
		}
	}
}

func rebuildRebalancedInternal(result RebalanceResult) ([][]byte, []uint64) {
	keys := make([][]byte, 0)
	children := []uint64{result.Left.LeftmostChild}
	for _, entry := range result.Left.InternalEntries {
		keys = append(keys, entry.Key)
		children = append(children, entry.RightChild)
	}
	if result.Action == RebalanceRedistributed {
		keys = append(keys, result.Parent.InternalEntries[0].Key)
		children = append(children, result.Right.LeftmostChild)
		for _, entry := range result.Right.InternalEntries {
			keys = append(keys, entry.Key)
			children = append(children, entry.RightChild)
		}
	}
	return keys, children
}
