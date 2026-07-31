package wal

import (
	"errors"
	"fmt"

	"github.com/HW-Yue/Memora/internal/store/page"
	"github.com/HW-Yue/Memora/internal/store/treecontrol"
)

type treeMetadataPlan struct {
	rootRecord      Record
	root            RootRedo
	allocatorRecord Record
	allocator       *AllocatorRedo
}

func parseTreeMetadata(records []Record) (*treeMetadataPlan, error) {
	var plan *treeMetadataPlan
	allocatorSeen := false
	for index, record := range records {
		switch record.Type {
		case TypeAllocator:
			if allocatorSeen || plan != nil || record.PageID != treecontrol.PageID {
				return nil, fmt.Errorf("%w: allocator redo order or identity", ErrCorrupt)
			}
			value, err := decodeAllocatorRedo(record.Payload)
			if err != nil {
				return nil, err
			}
			allocatorSeen = true
			plan = &treeMetadataPlan{
				allocatorRecord: record,
				allocator:       &value,
			}
		case TypeRoot:
			if record.PageID != treecontrol.PageID || index != len(records)-1 {
				return nil, fmt.Errorf("%w: root redo must be last", ErrCorrupt)
			}
			value, err := decodeRootRedo(record.Payload)
			if err != nil {
				return nil, err
			}
			if plan == nil {
				plan = &treeMetadataPlan{}
			} else if plan.rootRecord.Type != 0 {
				return nil, fmt.Errorf("%w: duplicate root redo", ErrCorrupt)
			}
			plan.rootRecord = record
			plan.root = value
		default:
			if allocatorSeen {
				return nil, fmt.Errorf("%w: Page redo after allocator", ErrCorrupt)
			}
		}
	}
	if plan == nil {
		return nil, nil
	}
	if plan.rootRecord.Type != TypeRoot {
		return nil, fmt.Errorf("%w: allocator redo without root", ErrCorrupt)
	}
	if plan.allocator != nil &&
		plan.allocatorRecord.SpaceID != plan.rootRecord.SpaceID {
		return nil, fmt.Errorf("%w: Tree metadata space mismatch", ErrCorrupt)
	}
	return plan, nil
}

func stageTreeMetadata(
	plan *treeMetadataPlan,
	spaces map[uint64]PageStore,
	staged map[recoveryPageKey]stagedPage,
	touched map[uint64]PageStore,
	initialized map[recoveryPageKey]page.Type,
	freed map[recoveryPageKey]struct{},
) (uint64, error) {
	if plan == nil {
		return 0, nil
	}
	spaceID := plan.rootRecord.SpaceID
	store, exists := spaces[spaceID]
	if !exists || store == nil {
		return 0, fmt.Errorf("%w: space %d", ErrMissingSpace, spaceID)
	}
	controlKey := recoveryPageKey{spaceID: spaceID, pageID: treecontrol.PageID}
	if _, exists := staged[controlKey]; exists {
		return 0, fmt.Errorf("%w: Tree control Page has ordinary redo", ErrCorrupt)
	}

	base, bootstrap, err := readTreeControl(store, spaceID)
	if err != nil {
		return 0, err
	}
	root := plan.root
	finalNextPageID := root.ExpectedNextPageID
	if plan.allocator != nil {
		allocator := *plan.allocator
		if allocator.ExpectedRevision != root.ExpectedRevision ||
			allocator.ExpectedNextPageID != root.ExpectedNextPageID {
			return 0, fmt.Errorf("%w: allocator/root precondition mismatch", ErrCorrupt)
		}
		finalNextPageID = allocator.NextPageID
	}
	if root.RootPageID >= finalNextPageID {
		return 0, fmt.Errorf("%w: root exceeds allocator high-water", ErrCorrupt)
	}
	if err := validateTreePageGenerations(
		staged,
		spaceID,
		base.Generation,
	); err != nil {
		return 0, err
	}
	if err := validateTreePageEvidence(
		plan,
		spaceID,
		finalNextPageID,
		staged,
		initialized,
		freed,
	); err != nil {
		return 0, err
	}

	metadataRecords := uint64(1)
	if plan.allocator != nil {
		metadataRecords++
	}
	if base.Revision > root.Revision {
		return metadataRecords, nil
	}
	if base.Revision == root.Revision {
		if base.RootPageID != root.RootPageID ||
			base.NextPageID != finalNextPageID ||
			base.LSN != plan.rootRecord.LSN {
			return 0, fmt.Errorf("%w: conflicting recovered Tree revision", ErrCorrupt)
		}
		if err := validateRecoveredRoot(
			store,
			staged,
			recoveryPageKey{spaceID: spaceID, pageID: root.RootPageID},
			base.Generation,
		); err != nil {
			return 0, err
		}
		touched[spaceID] = store
		return metadataRecords, nil
	}
	if base.Revision != root.ExpectedRevision ||
		base.RootPageID != root.ExpectedRootPageID ||
		base.NextPageID != root.ExpectedNextPageID {
		return 0, fmt.Errorf("%w: Tree metadata precondition", ErrCorrupt)
	}
	if err := validateRecoveredRoot(
		store,
		staged,
		recoveryPageKey{spaceID: spaceID, pageID: root.RootPageID},
		base.Generation,
	); err != nil {
		return 0, err
	}

	control, err := treecontrol.Encode(treecontrol.State{
		SpaceID:    spaceID,
		Generation: base.Generation,
		Revision:   root.Revision,
		RootPageID: root.RootPageID,
		NextPageID: finalNextPageID,
		LSN:        plan.rootRecord.LSN,
	})
	if err != nil {
		return 0, fmt.Errorf("%w: encode Tree control", ErrCorrupt)
	}
	staged[controlKey] = stagedPage{
		store:     store,
		value:     control,
		dirty:     true,
		control:   true,
		bootstrap: bootstrap,
	}
	touched[spaceID] = store
	return 0, nil
}

func readTreeControl(
	store PageStore,
	spaceID uint64,
) (treecontrol.State, bool, error) {
	value, err := store.Read(treecontrol.PageID)
	if errors.Is(err, page.ErrNotFound) {
		return treecontrol.Bootstrap(spaceID), true, nil
	}
	if err != nil {
		if !errors.Is(err, page.ErrCorrupt) {
			return treecontrol.State{}, false, fmt.Errorf("read Tree control: %w", err)
		}
		return treecontrol.State{}, false, fmt.Errorf(
			"%w: read Tree control: %v",
			ErrCorrupt,
			err,
		)
	}
	state, err := treecontrol.Decode(value, spaceID)
	if err != nil {
		return treecontrol.State{}, false, fmt.Errorf(
			"%w: decode Tree control: %v",
			ErrCorrupt,
			err,
		)
	}
	return state, state.Revision == 0, nil
}

func validateTreePageGenerations(
	staged map[recoveryPageKey]stagedPage,
	spaceID, generation uint64,
) error {
	for key, state := range staged {
		if key.spaceID != spaceID {
			continue
		}
		switch state.value.Header.Type {
		case page.TypeBTreeInternal, page.TypeBTreeLeaf, page.TypeFree:
			if state.value.Header.Generation != generation {
				return fmt.Errorf(
					"%w: Page %d physical generation",
					ErrCorrupt,
					key.pageID,
				)
			}
		default:
			return fmt.Errorf(
				"%w: non-Tree Page %d in Tree transaction",
				ErrCorrupt,
				key.pageID,
			)
		}
	}
	return nil
}

func validateTreePageEvidence(
	plan *treeMetadataPlan,
	spaceID, finalNextPageID uint64,
	staged map[recoveryPageKey]stagedPage,
	initialized map[recoveryPageKey]page.Type,
	freed map[recoveryPageKey]struct{},
) error {
	root := plan.root
	allocatedCount := finalNextPageID - root.ExpectedNextPageID
	if allocatedCount > uint64(len(initialized)) {
		return fmt.Errorf("%w: allocator is missing Page init", ErrCorrupt)
	}
	for pageID := root.ExpectedNextPageID; pageID < finalNextPageID; pageID++ {
		pageType, exists := initialized[recoveryPageKey{spaceID: spaceID, pageID: pageID}]
		if !exists || !isBTreePageType(pageType) {
			return fmt.Errorf("%w: allocator is missing B+ Tree Page init", ErrCorrupt)
		}
		if state, exists := staged[recoveryPageKey{
			spaceID: spaceID,
			pageID:  pageID,
		}]; !exists || !isBTreePageType(state.value.Header.Type) {
			return fmt.Errorf("%w: allocated Page is not a B+ Tree Page", ErrCorrupt)
		}
	}
	if plan.allocator == nil {
		return nil
	}
	for _, pageID := range plan.allocator.RetiredPageIDs {
		key := recoveryPageKey{spaceID: spaceID, pageID: pageID}
		if _, exists := freed[key]; !exists {
			return fmt.Errorf("%w: retired Page is not free", ErrCorrupt)
		}
		if state, exists := staged[key]; !exists ||
			state.value.Header.Type != page.TypeFree {
			return fmt.Errorf("%w: retired Page final image is not free", ErrCorrupt)
		}
		if pageID == root.RootPageID {
			return fmt.Errorf("%w: committed root is retired", ErrCorrupt)
		}
	}
	return nil
}

func validateRecoveredRoot(
	store PageStore,
	staged map[recoveryPageKey]stagedPage,
	key recoveryPageKey,
	generation uint64,
) error {
	if state, exists := staged[key]; exists {
		if !isBTreePageType(state.value.Header.Type) ||
			state.value.Header.Generation != generation {
			return fmt.Errorf(
				"%w: committed root type or physical generation",
				ErrCorrupt,
			)
		}
		return nil
	}
	value, err := store.Read(key.pageID)
	if err != nil {
		return fmt.Errorf("%w: read committed root: %v", ErrCorrupt, err)
	}
	if value.Header.SpaceID != key.spaceID ||
		value.Header.PageID != key.pageID ||
		value.Header.Generation != generation ||
		!isBTreePageType(value.Header.Type) {
		return fmt.Errorf(
			"%w: committed root identity, type, or physical generation",
			ErrCorrupt,
		)
	}
	return nil
}

func isBTreePageType(value page.Type) bool {
	return value == page.TypeBTreeInternal || value == page.TypeBTreeLeaf
}
