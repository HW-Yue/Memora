package btree

import (
	"bytes"
	"errors"
	"fmt"
	"sort"

	"github.com/HW-Yue/Memora/internal/store/page"
)

var ErrSplitNotRequired = errors.New("B+ Tree Node does not require a split")

func SplitLeaf(
	leftHeader page.Header,
	rightHeader page.Header,
	node Node,
	key []byte,
	value []byte,
) (Node, Node, []byte, bool, error) {
	if err := validateSplitHeaders(leftHeader, rightHeader, page.TypeBTreeLeaf); err != nil {
		return Node{}, Node{}, nil, false, err
	}
	if len(key) == 0 || len(value) == 0 {
		return Node{}, Node{}, nil, false, fmt.Errorf("%w: empty leaf key/value", ErrInvalid)
	}
	if _, err := Encode(leftHeader, node); err != nil {
		return Node{}, Node{}, nil, false, err
	}
	candidate := cloneNode(node)
	index := sort.Search(len(candidate.LeafEntries), func(index int) bool {
		return bytes.Compare(candidate.LeafEntries[index].Key, key) >= 0
	})
	replaced := index < len(candidate.LeafEntries) &&
		bytes.Equal(candidate.LeafEntries[index].Key, key)
	entry := LeafEntry{Key: bytes.Clone(key), Value: bytes.Clone(value)}
	if replaced {
		candidate.LeafEntries[index] = entry
	} else {
		candidate.LeafEntries = append(candidate.LeafEntries, LeafEntry{})
		copy(candidate.LeafEntries[index+1:], candidate.LeafEntries[index:])
		candidate.LeafEntries[index] = entry
	}
	if _, err := Encode(leftHeader, candidate); err == nil {
		return Node{}, Node{}, nil, false, ErrSplitNotRequired
	} else if !errors.Is(err, ErrNoSpace) {
		return Node{}, Node{}, nil, false, err
	}

	bestCut, bestDifference := -1, 0
	var bestLeft, bestRight Node
	for cut := 1; cut < len(candidate.LeafEntries); cut++ {
		left := Node{
			Kind: KindLeaf, NextLeafPageID: rightHeader.PageID,
			LeafEntries: cloneLeafEntries(candidate.LeafEntries[:cut]),
		}
		right := Node{
			Kind: KindLeaf, NextLeafPageID: node.NextLeafPageID,
			LeafEntries: cloneLeafEntries(candidate.LeafEntries[cut:]),
		}
		if _, err := Encode(leftHeader, left); err != nil {
			continue
		}
		if _, err := Encode(rightHeader, right); err != nil {
			continue
		}
		difference := abs(nodeUsedBytes(left) - nodeUsedBytes(right))
		if bestCut == -1 || difference < bestDifference {
			bestCut, bestDifference = cut, difference
			bestLeft, bestRight = left, right
		}
	}
	if bestCut == -1 {
		return Node{}, Node{}, nil, false, ErrNoSpace
	}
	return bestLeft, bestRight, bytes.Clone(bestRight.LeafEntries[0].Key), replaced, nil
}

func SplitInternal(
	leftHeader page.Header,
	rightHeader page.Header,
	node Node,
	key []byte,
	rightChild uint64,
) (Node, Node, []byte, bool, error) {
	if err := validateSplitHeaders(leftHeader, rightHeader, page.TypeBTreeInternal); err != nil {
		return Node{}, Node{}, nil, false, err
	}
	if len(key) == 0 || rightChild == 0 {
		return Node{}, Node{}, nil, false, fmt.Errorf("%w: empty separator or zero child", ErrInvalid)
	}
	if _, err := Encode(leftHeader, node); err != nil {
		return Node{}, Node{}, nil, false, err
	}
	candidate := cloneNode(node)
	index := sort.Search(len(candidate.InternalEntries), func(index int) bool {
		return bytes.Compare(candidate.InternalEntries[index].Key, key) >= 0
	})
	replaced := index < len(candidate.InternalEntries) &&
		bytes.Equal(candidate.InternalEntries[index].Key, key)
	entry := InternalEntry{Key: bytes.Clone(key), RightChild: rightChild}
	if replaced {
		candidate.InternalEntries[index] = entry
	} else {
		candidate.InternalEntries = append(candidate.InternalEntries, InternalEntry{})
		copy(candidate.InternalEntries[index+1:], candidate.InternalEntries[index:])
		candidate.InternalEntries[index] = entry
	}
	if _, err := Encode(leftHeader, candidate); err == nil {
		return Node{}, Node{}, nil, false, ErrSplitNotRequired
	} else if !errors.Is(err, ErrNoSpace) {
		return Node{}, Node{}, nil, false, err
	}

	bestPivot, bestDifference := -1, 0
	var bestLeft, bestRight Node
	for pivot := 1; pivot < len(candidate.InternalEntries)-1; pivot++ {
		left := Node{
			Kind: KindInternal, Level: node.Level, LeftmostChild: node.LeftmostChild,
			InternalEntries: cloneInternalEntries(candidate.InternalEntries[:pivot]),
		}
		right := Node{
			Kind: KindInternal, Level: node.Level,
			LeftmostChild:   candidate.InternalEntries[pivot].RightChild,
			InternalEntries: cloneInternalEntries(candidate.InternalEntries[pivot+1:]),
		}
		if _, err := Encode(leftHeader, left); err != nil {
			continue
		}
		if _, err := Encode(rightHeader, right); err != nil {
			continue
		}
		difference := abs(nodeUsedBytes(left) - nodeUsedBytes(right))
		if bestPivot == -1 || difference < bestDifference {
			bestPivot, bestDifference = pivot, difference
			bestLeft, bestRight = left, right
		}
	}
	if bestPivot == -1 {
		return Node{}, Node{}, nil, false, ErrNoSpace
	}
	return bestLeft, bestRight,
		bytes.Clone(candidate.InternalEntries[bestPivot].Key), replaced, nil
}

func GrowRoot(
	header page.Header,
	leftPageID uint64,
	rightPageID uint64,
	childLevel uint16,
	separator []byte,
) (Node, error) {
	if header.Type != page.TypeBTreeInternal || header.PageID == 0 || header.Flags != 0 ||
		leftPageID == 0 || rightPageID == 0 || leftPageID == rightPageID ||
		header.PageID == leftPageID || header.PageID == rightPageID || len(separator) == 0 ||
		childLevel == ^uint16(0) {
		return Node{}, fmt.Errorf("%w: root identity or shape", ErrInvalid)
	}
	root := Node{
		Kind: KindInternal, Level: childLevel + 1, LeftmostChild: leftPageID,
		InternalEntries: []InternalEntry{{
			Key: bytes.Clone(separator), RightChild: rightPageID,
		}},
	}
	if _, err := Encode(header, root); err != nil {
		return Node{}, err
	}
	return root, nil
}

func validateSplitHeaders(left page.Header, right page.Header, kind page.Type) error {
	if left.Type != kind || right.Type != kind || left.PageID == 0 || right.PageID == 0 ||
		left.PageID == right.PageID || left.SpaceID != right.SpaceID ||
		left.Generation != right.Generation || left.Flags != 0 || right.Flags != 0 {
		return fmt.Errorf("%w: split Page headers", ErrInvalid)
	}
	return nil
}

func cloneLeafEntries(entries []LeafEntry) []LeafEntry {
	cloned := make([]LeafEntry, len(entries))
	for index, entry := range entries {
		cloned[index] = LeafEntry{Key: bytes.Clone(entry.Key), Value: bytes.Clone(entry.Value)}
	}
	return cloned
}

func cloneInternalEntries(entries []InternalEntry) []InternalEntry {
	cloned := make([]InternalEntry, len(entries))
	for index, entry := range entries {
		cloned[index] = InternalEntry{
			Key: bytes.Clone(entry.Key), RightChild: entry.RightChild,
		}
	}
	return cloned
}

func nodeUsedBytes(node Node) int {
	count := len(node.LeafEntries)
	if node.Kind == KindInternal {
		count = len(node.InternalEntries)
	}
	used := nodeHeaderSize + count*nodeSlotSize
	for index := 0; index < count; index++ {
		_, recordLength := entryLengths(node, index)
		used += recordLength
	}
	return used
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
