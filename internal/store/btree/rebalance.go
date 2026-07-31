package btree

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/HW-Yue/Memora/internal/store/page"
)

var ErrRebalanceNotRequired = errors.New("B+ Tree rebalance is not required")

type RebalanceAction uint8

const (
	RebalanceMerged RebalanceAction = iota + 1
	RebalanceRedistributed
)

type RebalanceResult struct {
	Parent        Node
	Left          Node
	Right         Node
	Action        RebalanceAction
	RemovedPageID uint64
}

func RebalanceChildren(
	parentHeader page.Header,
	parent Node,
	separatorIndex int,
	leftHeader page.Header,
	left Node,
	rightHeader page.Header,
	right Node,
) (RebalanceResult, error) {
	if err := validateRebalanceInput(
		parentHeader, parent, separatorIndex, leftHeader, left, rightHeader, right,
	); err != nil {
		return RebalanceResult{}, err
	}
	if left.Kind != KindLeaf {
		return rebalanceInternals(
			parentHeader, parent, separatorIndex, leftHeader, left, rightHeader, right,
		)
	}
	merged := Node{
		Kind: KindLeaf, NextLeafPageID: right.NextLeafPageID,
		LeafEntries: append(cloneLeafEntries(left.LeafEntries), cloneLeafEntries(right.LeafEntries)...),
	}
	if _, err := Encode(leftHeader, merged); err != nil {
		if errors.Is(err, ErrNoSpace) {
			return redistributeLeaves(
				parentHeader, parent, separatorIndex, leftHeader, left, rightHeader, right,
			)
		}
		return RebalanceResult{}, err
	}
	updatedParent := removeParentSeparator(parent, separatorIndex)
	if _, err := Encode(parentHeader, updatedParent); err != nil {
		return RebalanceResult{}, err
	}
	return RebalanceResult{
		Parent: updatedParent, Left: merged, Action: RebalanceMerged,
		RemovedPageID: rightHeader.PageID,
	}, nil
}

func ShrinkRoot(header page.Header, root Node) (uint64, error) {
	if header.Type != page.TypeBTreeInternal || root.Kind != KindInternal ||
		header.PageID == root.LeftmostChild {
		return 0, fmt.Errorf("%w: root shrink shape", ErrInvalid)
	}
	if _, err := Encode(header, root); err != nil {
		return 0, err
	}
	if len(root.InternalEntries) != 0 {
		return 0, ErrRebalanceNotRequired
	}
	return root.LeftmostChild, nil
}

func redistributeLeaves(
	parentHeader page.Header,
	parent Node,
	separatorIndex int,
	leftHeader page.Header,
	left Node,
	rightHeader page.Header,
	right Node,
) (RebalanceResult, error) {
	entries := append(cloneLeafEntries(left.LeafEntries), cloneLeafEntries(right.LeafEntries)...)
	bestCut, bestDifference := -1, 0
	var bestLeft, bestRight Node
	for cut := 1; cut < len(entries); cut++ {
		candidateLeft := Node{
			Kind: KindLeaf, NextLeafPageID: rightHeader.PageID,
			LeafEntries: cloneLeafEntries(entries[:cut]),
		}
		candidateRight := Node{
			Kind: KindLeaf, NextLeafPageID: right.NextLeafPageID,
			LeafEntries: cloneLeafEntries(entries[cut:]),
		}
		if _, err := Encode(leftHeader, candidateLeft); err != nil {
			continue
		}
		if _, err := Encode(rightHeader, candidateRight); err != nil {
			continue
		}
		difference := abs(nodeUsedBytes(candidateLeft) - nodeUsedBytes(candidateRight))
		if bestCut == -1 || difference < bestDifference {
			bestCut, bestDifference = cut, difference
			bestLeft, bestRight = candidateLeft, candidateRight
		}
	}
	if bestCut == -1 {
		return RebalanceResult{}, ErrNoSpace
	}
	updatedParent := cloneNode(parent)
	updatedParent.InternalEntries[separatorIndex].Key =
		bytes.Clone(bestRight.LeafEntries[0].Key)
	if _, err := Encode(parentHeader, updatedParent); err != nil {
		return RebalanceResult{}, err
	}
	return RebalanceResult{
		Parent: updatedParent, Left: bestLeft, Right: bestRight,
		Action: RebalanceRedistributed,
	}, nil
}

func rebalanceInternals(
	parentHeader page.Header,
	parent Node,
	separatorIndex int,
	leftHeader page.Header,
	left Node,
	rightHeader page.Header,
	right Node,
) (RebalanceResult, error) {
	boundary := parent.InternalEntries[separatorIndex]
	entries := cloneInternalEntries(left.InternalEntries)
	entries = append(entries, InternalEntry{
		Key: bytes.Clone(boundary.Key), RightChild: right.LeftmostChild,
	})
	entries = append(entries, cloneInternalEntries(right.InternalEntries)...)
	merged := Node{
		Kind: KindInternal, Level: left.Level, LeftmostChild: left.LeftmostChild,
		InternalEntries: cloneInternalEntries(entries),
	}
	if _, err := Encode(leftHeader, merged); err == nil {
		updatedParent := removeParentSeparator(parent, separatorIndex)
		if _, err := Encode(parentHeader, updatedParent); err != nil {
			return RebalanceResult{}, err
		}
		return RebalanceResult{
			Parent: updatedParent, Left: merged, Action: RebalanceMerged,
			RemovedPageID: rightHeader.PageID,
		}, nil
	} else if !errors.Is(err, ErrNoSpace) {
		return RebalanceResult{}, err
	}

	bestPivot, bestDifference := -1, 0
	var bestLeft, bestRight Node
	for pivot := 1; pivot < len(entries)-1; pivot++ {
		candidateLeft := Node{
			Kind: KindInternal, Level: left.Level, LeftmostChild: left.LeftmostChild,
			InternalEntries: cloneInternalEntries(entries[:pivot]),
		}
		candidateRight := Node{
			Kind: KindInternal, Level: right.Level,
			LeftmostChild:   entries[pivot].RightChild,
			InternalEntries: cloneInternalEntries(entries[pivot+1:]),
		}
		if _, err := Encode(leftHeader, candidateLeft); err != nil {
			continue
		}
		if _, err := Encode(rightHeader, candidateRight); err != nil {
			continue
		}
		difference := abs(nodeUsedBytes(candidateLeft) - nodeUsedBytes(candidateRight))
		if bestPivot == -1 || difference < bestDifference {
			bestPivot, bestDifference = pivot, difference
			bestLeft, bestRight = candidateLeft, candidateRight
		}
	}
	if bestPivot == -1 {
		return RebalanceResult{}, ErrNoSpace
	}
	updatedParent := cloneNode(parent)
	updatedParent.InternalEntries[separatorIndex].Key = bytes.Clone(entries[bestPivot].Key)
	if _, err := Encode(parentHeader, updatedParent); err != nil {
		return RebalanceResult{}, err
	}
	return RebalanceResult{
		Parent: updatedParent, Left: bestLeft, Right: bestRight,
		Action: RebalanceRedistributed,
	}, nil
}

func removeParentSeparator(parent Node, separatorIndex int) Node {
	updated := cloneNode(parent)
	copy(updated.InternalEntries[separatorIndex:], updated.InternalEntries[separatorIndex+1:])
	updated.InternalEntries[len(updated.InternalEntries)-1] = InternalEntry{}
	updated.InternalEntries = updated.InternalEntries[:len(updated.InternalEntries)-1]
	return updated
}

func validateRebalanceInput(
	parentHeader page.Header,
	parent Node,
	separatorIndex int,
	leftHeader page.Header,
	left Node,
	rightHeader page.Header,
	right Node,
) error {
	if parentHeader.Type != page.TypeBTreeInternal || parent.Kind != KindInternal ||
		left.Kind != right.Kind || separatorIndex < 0 ||
		separatorIndex >= len(parent.InternalEntries) ||
		parentHeader.PageID == leftHeader.PageID || parentHeader.PageID == rightHeader.PageID ||
		leftHeader.PageID == rightHeader.PageID || parentHeader.SpaceID != leftHeader.SpaceID ||
		parentHeader.SpaceID != rightHeader.SpaceID ||
		parentHeader.Generation != leftHeader.Generation ||
		parentHeader.Generation != rightHeader.Generation ||
		left.Level != right.Level || uint32(left.Level)+1 != uint32(parent.Level) {
		return fmt.Errorf("%w: rebalance identity or shape", ErrInvalid)
	}
	wantType := page.TypeBTreeLeaf
	if left.Kind == KindInternal {
		wantType = page.TypeBTreeInternal
	}
	if leftHeader.Type != wantType || rightHeader.Type != wantType {
		return fmt.Errorf("%w: rebalance child Page type", ErrInvalid)
	}
	if _, err := Encode(parentHeader, parent); err != nil {
		return err
	}
	if _, err := Encode(leftHeader, left); err != nil {
		return err
	}
	if _, err := Encode(rightHeader, right); err != nil {
		return err
	}
	wantLeftID := parent.LeftmostChild
	if separatorIndex > 0 {
		wantLeftID = parent.InternalEntries[separatorIndex-1].RightChild
	}
	separator := parent.InternalEntries[separatorIndex]
	if wantLeftID != leftHeader.PageID || separator.RightChild != rightHeader.PageID {
		return fmt.Errorf("%w: parent child identity", ErrInvalid)
	}
	if left.Kind == KindLeaf {
		if left.NextLeafPageID != rightHeader.PageID ||
			(len(left.LeafEntries) > 0 && bytes.Compare(
				left.LeafEntries[len(left.LeafEntries)-1].Key, separator.Key,
			) >= 0) ||
			(len(right.LeafEntries) > 0 && bytes.Compare(separator.Key, right.LeafEntries[0].Key) > 0) {
			return fmt.Errorf("%w: leaf rebalance boundary", ErrInvalid)
		}
	} else if (len(left.InternalEntries) > 0 && bytes.Compare(
		left.InternalEntries[len(left.InternalEntries)-1].Key, separator.Key,
	) >= 0) || (len(right.InternalEntries) > 0 && bytes.Compare(
		separator.Key, right.InternalEntries[0].Key,
	) >= 0) {
		return fmt.Errorf("%w: internal rebalance boundary", ErrInvalid)
	}
	return nil
}
