package btree

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/HW-Yue/Memora/internal/store/page"
	"github.com/HW-Yue/Memora/internal/store/treecontrol"
)

var ErrPageIDOverflow = errors.New("B+ Tree Page ID allocation overflow")

type PageChange struct {
	Page        page.Page
	ExpectedLSN uint64
}

type MutationPlan struct {
	RootPageID uint64
	NextPageID uint64
	Changes    []PageChange
	Allocated  []uint64
	Reused     []uint64
	Recycled   []uint64
	Retired    []uint64
}

type MutationPlanner struct {
	spaceID    uint64
	generation uint64
	rootPageID uint64
	nextPageID uint64
	reader     Reader
	loaded     map[uint64]page.Page
	changes    map[uint64]PageChange
	allocated  map[uint64]struct{}
	reused     map[uint64]struct{}
	recycled   map[uint64]struct{}
	reusable   []uint64
	retired    map[uint64]struct{}
}

type mutationPathFrame struct {
	pageID     uint64
	level      uint16
	childIndex int
	lower      []byte
	upper      []byte
}

func NewMutationPlanner(
	spaceID, generation, rootPageID, nextPageID uint64,
	reader Reader,
	reusablePageIDs ...uint64,
) (*MutationPlanner, error) {
	if spaceID == 0 || rootPageID == 0 || nextPageID == 0 || rootPageID >= nextPageID || reader == nil {
		return nil, fmt.Errorf("%w: Mutation Planner configuration", ErrInvalid)
	}
	var previous uint64
	for index, pageID := range reusablePageIDs {
		if pageID < treecontrol.FirstDataPageID || pageID >= nextPageID || pageID == rootPageID ||
			(index > 0 && pageID <= previous) {
			return nil, fmt.Errorf("%w: reusable Page IDs", ErrInvalid)
		}
		previous = pageID
	}
	return &MutationPlanner{
		spaceID:    spaceID,
		generation: generation,
		rootPageID: rootPageID,
		nextPageID: nextPageID,
		reader:     reader,
		loaded:     make(map[uint64]page.Page),
		changes:    make(map[uint64]PageChange),
		allocated:  make(map[uint64]struct{}),
		reused:     make(map[uint64]struct{}),
		recycled:   make(map[uint64]struct{}),
		reusable:   append([]uint64(nil), reusablePageIDs...),
		retired:    make(map[uint64]struct{}),
	}, nil
}

func (planner *MutationPlanner) Upsert(key, value []byte) error {
	if planner == nil || len(key) == 0 || len(value) == 0 {
		return fmt.Errorf("%w: empty upsert key/value", ErrInvalid)
	}
	work := planner.clone()
	if err := work.upsert(bytes.Clone(key), bytes.Clone(value)); err != nil {
		return err
	}
	*planner = *work
	return nil
}

func (planner *MutationPlanner) Delete(key []byte) error {
	if planner == nil || len(key) == 0 {
		return fmt.Errorf("%w: empty delete key", ErrInvalid)
	}
	work := planner.clone()
	if err := work.delete(bytes.Clone(key)); err != nil {
		return err
	}
	*planner = *work
	return nil
}

func (planner *MutationPlanner) Plan() MutationPlan {
	if planner == nil {
		return MutationPlan{}
	}
	result := MutationPlan{
		RootPageID: planner.rootPageID,
		NextPageID: planner.nextPageID,
		Changes:    make([]PageChange, 0, len(planner.changes)),
		Allocated:  sortedIDs(planner.allocated),
		Reused:     sortedIDs(planner.reused),
		Recycled:   sortedIDs(planner.recycled),
		Retired:    sortedIDs(planner.retired),
	}
	changeIDs := make([]uint64, 0, len(planner.changes))
	for pageID := range planner.changes {
		changeIDs = append(changeIDs, pageID)
	}
	sort.Slice(changeIDs, func(left, right int) bool { return changeIDs[left] < changeIDs[right] })
	for _, pageID := range changeIDs {
		result.Changes = append(result.Changes, clonePageChange(planner.changes[pageID]))
	}
	return result
}

func (planner *MutationPlanner) upsert(key, value []byte) error {
	leafID, leaf, path, err := planner.descend(key)
	if err != nil {
		return err
	}
	header := planner.nodeHeader(leafID, KindLeaf)
	updated, _, err := UpsertLeaf(header, leaf, key, value)
	if err == nil {
		return planner.stageNode(leafID, updated)
	}
	if !errors.Is(err, ErrNoSpace) {
		return err
	}
	rightID, err := planner.allocatePage()
	if err != nil {
		return err
	}
	left, right, separator, _, err := SplitLeaf(
		header, planner.nodeHeader(rightID, KindLeaf), leaf, key, value,
	)
	if err != nil {
		return err
	}
	if err := planner.stageNode(leafID, left); err != nil {
		return err
	}
	if err := planner.stageNode(rightID, right); err != nil {
		return err
	}

	leftID := leafID
	childLevel := uint16(0)
	for index := len(path) - 1; index >= 0; index-- {
		frame := path[index]
		parent, err := planner.loadNode(frame.pageID, int(frame.level), frame.lower, frame.upper)
		if err != nil {
			return err
		}
		parentHeader := planner.nodeHeader(frame.pageID, KindInternal)
		updatedParent, _, err := UpsertInternal(parentHeader, parent, separator, rightID)
		if err == nil {
			return planner.stageNode(frame.pageID, updatedParent)
		}
		if !errors.Is(err, ErrNoSpace) {
			return err
		}
		newRightID, err := planner.allocatePage()
		if err != nil {
			return err
		}
		leftParent, rightParent, promoted, _, err := SplitInternal(
			parentHeader, planner.nodeHeader(newRightID, KindInternal),
			parent, separator, rightID,
		)
		if err != nil {
			return err
		}
		if err := planner.stageNode(frame.pageID, leftParent); err != nil {
			return err
		}
		if err := planner.stageNode(newRightID, rightParent); err != nil {
			return err
		}
		leftID, rightID, separator = frame.pageID, newRightID, promoted
		childLevel = parent.Level
	}

	newRootID, err := planner.allocatePage()
	if err != nil {
		return err
	}
	root, err := GrowRoot(
		planner.nodeHeader(newRootID, KindInternal),
		leftID, rightID, childLevel, separator,
	)
	if err != nil {
		return err
	}
	if err := planner.stageNode(newRootID, root); err != nil {
		return err
	}
	planner.rootPageID = newRootID
	return nil
}

func (planner *MutationPlanner) delete(key []byte) error {
	leafID, leaf, path, err := planner.descend(key)
	if err != nil {
		return err
	}
	updated, _, err := DeleteLeaf(planner.nodeHeader(leafID, KindLeaf), leaf, key)
	if err != nil {
		return err
	}
	if err := planner.stageNode(leafID, updated); err != nil {
		return err
	}
	if leafID == planner.rootPageID || !nodeUnderflows(updated) {
		return nil
	}

	childID, child := leafID, updated
	for index := len(path) - 1; index >= 0; index-- {
		frame := path[index]
		parent, err := planner.loadNode(frame.pageID, int(frame.level), frame.lower, frame.upper)
		if err != nil {
			return err
		}
		childIndex := frame.childIndex
		if childIndex < 0 || childIndex > len(parent.InternalEntries) ||
			planner.childPageID(parent, childIndex) != childID {
			return fmt.Errorf("%w: mutation path changed at Page %d", ErrCorrupt, frame.pageID)
		}

		separatorIndex := childIndex
		leftID, left := childID, child
		rightIndex := childIndex + 1
		if rightIndex > len(parent.InternalEntries) {
			if childIndex == 0 {
				return fmt.Errorf("%w: underflow child has no sibling", ErrCorrupt)
			}
			separatorIndex = childIndex - 1
			leftID = planner.childPageID(parent, childIndex-1)
			lower, upper := childBounds(parent, childIndex-1, frame.lower, frame.upper)
			left, err = planner.loadNode(leftID, int(child.Level), lower, upper)
			if err != nil {
				return err
			}
			rightIndex = childIndex
		}
		rightID := planner.childPageID(parent, rightIndex)
		right := child
		if rightID != childID {
			lower, upper := childBounds(parent, rightIndex, frame.lower, frame.upper)
			right, err = planner.loadNode(rightID, int(child.Level), lower, upper)
			if err != nil {
				return err
			}
		}
		result, err := RebalanceChildren(
			planner.nodeHeader(frame.pageID, KindInternal), parent, separatorIndex,
			planner.nodeHeader(leftID, left.Kind), left,
			planner.nodeHeader(rightID, right.Kind), right,
		)
		if err != nil {
			if errors.Is(err, ErrInvalid) {
				return fmt.Errorf("%w: rebalance Page %d: %v", ErrCorrupt, frame.pageID, err)
			}
			return err
		}
		if err := planner.stageNode(leftID, result.Left); err != nil {
			return err
		}
		if result.Action == RebalanceRedistributed {
			if err := planner.stageNode(rightID, result.Right); err != nil {
				return err
			}
			return planner.stageNode(frame.pageID, result.Parent)
		}
		planner.retirePage(result.RemovedPageID)
		if frame.pageID == planner.rootPageID {
			if len(result.Parent.InternalEntries) == 0 {
				newRoot, err := ShrinkRoot(
					planner.nodeHeader(frame.pageID, KindInternal), result.Parent,
				)
				if err != nil {
					return err
				}
				planner.rootPageID = newRoot
				planner.retirePage(frame.pageID)
				return nil
			}
			return planner.stageNode(frame.pageID, result.Parent)
		}
		if err := planner.stageNode(frame.pageID, result.Parent); err != nil {
			return err
		}
		if !nodeUnderflows(result.Parent) {
			return nil
		}
		childID, child = frame.pageID, result.Parent
	}
	return fmt.Errorf("%w: underflow propagation did not reach root", ErrCorrupt)
}

func (planner *MutationPlanner) descend(key []byte) (uint64, Node, []mutationPathFrame, error) {
	pageID := planner.rootPageID
	expectedLevel := -1
	var lower, upper []byte
	path := make([]mutationPathFrame, 0, 4)
	seen := make(map[uint64]struct{})
	for depth := 0; depth < maxSearchDepth; depth++ {
		if _, duplicate := seen[pageID]; duplicate {
			return 0, Node{}, nil, fmt.Errorf("%w: repeated Page %d", ErrCorrupt, pageID)
		}
		seen[pageID] = struct{}{}
		node, err := planner.loadNode(pageID, expectedLevel, lower, upper)
		if err != nil {
			return 0, Node{}, nil, err
		}
		if node.Kind == KindLeaf {
			return pageID, node, path, nil
		}
		childIndex := sort.Search(len(node.InternalEntries), func(index int) bool {
			return bytes.Compare(key, node.InternalEntries[index].Key) < 0
		})
		path = append(path, mutationPathFrame{
			pageID: pageID, level: node.Level, childIndex: childIndex,
			lower: bytes.Clone(lower), upper: bytes.Clone(upper),
		})
		lower, upper = childBounds(node, childIndex, lower, upper)
		pageID = planner.childPageID(node, childIndex)
		expectedLevel = int(node.Level) - 1
	}
	return 0, Node{}, nil, fmt.Errorf("%w: depth exceeds %d", ErrCorrupt, maxSearchDepth)
}

func (planner *MutationPlanner) loadNode(
	pageID uint64,
	expectedLevel int,
	lower, upper []byte,
) (Node, error) {
	if pageID == 0 || pageID >= planner.nextPageID {
		return Node{}, fmt.Errorf("%w: Page ID %d is outside allocator high-water %d", ErrCorrupt, pageID, planner.nextPageID)
	}
	if _, retired := planner.retired[pageID]; retired {
		return Node{}, fmt.Errorf("%w: reference to retired Page %d", ErrCorrupt, pageID)
	}
	value, exists := planner.pageValue(pageID)
	if !exists {
		loaded, err := planner.reader.Read(pageID)
		if err != nil {
			return Node{}, fmt.Errorf("read B+ Tree Page %d: %w", pageID, err)
		}
		value = clonePlanPage(loaded)
		planner.loaded[pageID] = clonePlanPage(loaded)
	}
	if value.Header.SpaceID != planner.spaceID || value.Header.Generation != planner.generation ||
		value.Header.PageID != pageID {
		return Node{}, fmt.Errorf("%w: Page identity at %d", ErrCorrupt, pageID)
	}
	node, err := Decode(value)
	if err != nil {
		return Node{}, err
	}
	if expectedLevel >= 0 && int(node.Level) != expectedLevel {
		return Node{}, fmt.Errorf("%w: level %d, want %d", ErrCorrupt, node.Level, expectedLevel)
	}
	if err := planner.validateNodeReferences(pageID, node, lower, upper); err != nil {
		return Node{}, err
	}
	return node, nil
}

func (planner *MutationPlanner) validateNodeReferences(
	pageID uint64,
	node Node,
	lower, upper []byte,
) error {
	checkKey := func(key []byte) error {
		if lower != nil && bytes.Compare(key, lower) < 0 {
			return fmt.Errorf("%w: Page %d key below lower boundary", ErrCorrupt, pageID)
		}
		if upper != nil && bytes.Compare(key, upper) >= 0 {
			return fmt.Errorf("%w: Page %d key crosses upper boundary", ErrCorrupt, pageID)
		}
		return nil
	}
	if node.Kind == KindLeaf {
		for _, entry := range node.LeafEntries {
			if err := checkKey(entry.Key); err != nil {
				return err
			}
		}
		if node.NextLeafPageID == pageID || node.NextLeafPageID >= planner.nextPageID {
			return fmt.Errorf("%w: invalid next leaf from Page %d", ErrCorrupt, pageID)
		}
		return nil
	}
	children := make(map[uint64]struct{}, len(node.InternalEntries)+1)
	for index := 0; index <= len(node.InternalEntries); index++ {
		child := planner.childPageID(node, index)
		if child == pageID || child == 0 || child >= planner.nextPageID {
			return fmt.Errorf("%w: invalid child %d from Page %d", ErrCorrupt, child, pageID)
		}
		if _, duplicate := children[child]; duplicate {
			return fmt.Errorf("%w: duplicate child %d in Page %d", ErrCorrupt, child, pageID)
		}
		children[child] = struct{}{}
	}
	for _, entry := range node.InternalEntries {
		if err := checkKey(entry.Key); err != nil {
			return err
		}
	}
	return nil
}

func (planner *MutationPlanner) pageValue(pageID uint64) (page.Page, bool) {
	if change, exists := planner.changes[pageID]; exists {
		return clonePlanPage(change.Page), true
	}
	if value, exists := planner.loaded[pageID]; exists {
		return clonePlanPage(value), true
	}
	return page.Page{}, false
}

func (planner *MutationPlanner) stageNode(pageID uint64, node Node) error {
	change, alreadyChanged := planner.changes[pageID]
	expectedLSN := change.ExpectedLSN
	if !alreadyChanged {
		if _, isNew := planner.allocated[pageID]; !isNew {
			original, exists := planner.loaded[pageID]
			if !exists {
				return fmt.Errorf("%w: Page %d was staged before load", ErrInvalid, pageID)
			}
			expectedLSN = original.Header.LSN
		}
	}
	encoded, err := Encode(planner.nodeHeader(pageID, node.Kind), node)
	if err != nil {
		return err
	}
	planner.changes[pageID] = PageChange{Page: clonePlanPage(encoded), ExpectedLSN: expectedLSN}
	return nil
}

func (planner *MutationPlanner) allocatePage() (uint64, error) {
	if len(planner.retired) > 0 {
		pageID := sortedIDs(planner.retired)[0]
		delete(planner.retired, pageID)
		planner.recycled[pageID] = struct{}{}
		return pageID, nil
	}
	if len(planner.reusable) > 0 {
		pageID := planner.reusable[0]
		planner.reusable = planner.reusable[1:]
		value, err := planner.reader.Read(pageID)
		if err != nil {
			return 0, fmt.Errorf("read reusable Page %d: %w", pageID, err)
		}
		if value.Header.Type != page.TypeFree || value.Header.SpaceID != planner.spaceID ||
			value.Header.PageID != pageID || value.Header.Generation != planner.generation ||
			value.Header.LSN == 0 {
			return 0, fmt.Errorf("%w: reusable Page %d state", ErrCorrupt, pageID)
		}
		planner.loaded[pageID] = clonePlanPage(value)
		planner.reused[pageID] = struct{}{}
		return pageID, nil
	}
	if planner.nextPageID == math.MaxUint64 {
		return 0, ErrPageIDOverflow
	}
	pageID := planner.nextPageID
	planner.nextPageID++
	planner.allocated[pageID] = struct{}{}
	return pageID, nil
}

func (planner *MutationPlanner) retirePage(pageID uint64) {
	if _, wasRecycled := planner.recycled[pageID]; wasRecycled {
		delete(planner.recycled, pageID)
		delete(planner.changes, pageID)
		planner.retired[pageID] = struct{}{}
		return
	}
	if _, wasReused := planner.reused[pageID]; wasReused {
		delete(planner.reused, pageID)
		delete(planner.changes, pageID)
		return
	}
	if _, wasAllocated := planner.allocated[pageID]; wasAllocated {
		// Page IDs reserved by this plan remain consumed and initialized even when
		// a later mutation makes them unreachable. This keeps the allocator advance
		// contiguous without placing one ID in both Allocated and Retired.
		return
	}
	delete(planner.changes, pageID)
	planner.retired[pageID] = struct{}{}
}

func (planner *MutationPlanner) nodeHeader(pageID uint64, kind Kind) page.Header {
	pageType := page.TypeBTreeLeaf
	if kind == KindInternal {
		pageType = page.TypeBTreeInternal
	}
	return page.Header{
		Type: pageType, SpaceID: planner.spaceID, PageID: pageID,
		Generation: planner.generation,
	}
}

func (planner *MutationPlanner) childPageID(node Node, childIndex int) uint64 {
	if childIndex == 0 {
		return node.LeftmostChild
	}
	return node.InternalEntries[childIndex-1].RightChild
}

func childBounds(node Node, childIndex int, inheritedLower, inheritedUpper []byte) ([]byte, []byte) {
	lower, upper := bytes.Clone(inheritedLower), bytes.Clone(inheritedUpper)
	if childIndex > 0 {
		lower = bytes.Clone(node.InternalEntries[childIndex-1].Key)
	}
	if childIndex < len(node.InternalEntries) {
		upper = bytes.Clone(node.InternalEntries[childIndex].Key)
	}
	return lower, upper
}

func nodeUnderflows(node Node) bool {
	entryCount := len(node.LeafEntries)
	if node.Kind == KindInternal {
		entryCount = len(node.InternalEntries)
	}
	return entryCount == 0 || nodeUsedBytes(node) < nodePayloadSize/2
}

func (planner *MutationPlanner) clone() *MutationPlanner {
	cloned := &MutationPlanner{
		spaceID: planner.spaceID, generation: planner.generation,
		rootPageID: planner.rootPageID, nextPageID: planner.nextPageID,
		reader:    planner.reader,
		loaded:    make(map[uint64]page.Page, len(planner.loaded)),
		changes:   make(map[uint64]PageChange, len(planner.changes)),
		allocated: make(map[uint64]struct{}, len(planner.allocated)),
		reused:    make(map[uint64]struct{}, len(planner.reused)),
		recycled:  make(map[uint64]struct{}, len(planner.recycled)),
		reusable:  append([]uint64(nil), planner.reusable...),
		retired:   make(map[uint64]struct{}, len(planner.retired)),
	}
	for pageID, value := range planner.loaded {
		cloned.loaded[pageID] = clonePlanPage(value)
	}
	for pageID, change := range planner.changes {
		cloned.changes[pageID] = clonePageChange(change)
	}
	for pageID := range planner.allocated {
		cloned.allocated[pageID] = struct{}{}
	}
	for pageID := range planner.reused {
		cloned.reused[pageID] = struct{}{}
	}
	for pageID := range planner.recycled {
		cloned.recycled[pageID] = struct{}{}
	}
	for pageID := range planner.retired {
		cloned.retired[pageID] = struct{}{}
	}
	return cloned
}

func clonePageChange(change PageChange) PageChange {
	return PageChange{Page: clonePlanPage(change.Page), ExpectedLSN: change.ExpectedLSN}
}

func clonePlanPage(value page.Page) page.Page {
	return page.Page{Header: value.Header, Payload: bytes.Clone(value.Payload)}
}

func sortedIDs(values map[uint64]struct{}) []uint64 {
	result := make([]uint64, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Slice(result, func(left, right int) bool { return result[left] < result[right] })
	return result
}
