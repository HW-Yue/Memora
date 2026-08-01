package treecommit

import (
	"errors"
	"fmt"
	"math"

	"github.com/HW-Yue/Memora/internal/store/btree"
	"github.com/HW-Yue/Memora/internal/store/page"
	"github.com/HW-Yue/Memora/internal/store/treecontrol"
	"github.com/HW-Yue/Memora/internal/store/wal"
)

var ErrInvalidPlan = errors.New("Tree commit plan is invalid")

type Prepared struct {
	Records []wal.Record
}

func Prepare(
	base treecontrol.State,
	plan btree.MutationPlan,
) (Prepared, error) {
	if _, err := treecontrol.Encode(base); err != nil {
		return Prepared{}, fmt.Errorf("%w: control: %v", ErrInvalidPlan, err)
	}
	if base.Revision == math.MaxUint64 ||
		plan.NextPageID < base.NextPageID ||
		plan.RootPageID < treecontrol.FirstDataPageID ||
		plan.RootPageID >= plan.NextPageID {
		return Prepared{}, fmt.Errorf("%w: root, allocator, or revision", ErrInvalidPlan)
	}
	if len(plan.Changes) == 0 &&
		len(plan.Retired) == 0 &&
		plan.RootPageID == base.RootPageID &&
		plan.NextPageID == base.NextPageID {
		return Prepared{}, fmt.Errorf("%w: empty mutation", ErrInvalidPlan)
	}

	allocated, err := validateAllocated(base.NextPageID, plan.NextPageID, plan.Allocated)
	if err != nil {
		return Prepared{}, err
	}
	reused, err := validateReused(base, plan)
	if err != nil {
		return Prepared{}, err
	}
	recycled, err := validateRecycled(base, plan, reused)
	if err != nil {
		return Prepared{}, err
	}
	records := make(
		[]wal.Record,
		0,
		len(plan.Changes)+len(plan.Retired)+2,
	)
	changed := make(map[uint64]struct{}, len(plan.Changes))
	var previousChange uint64
	for index, change := range plan.Changes {
		pageID := change.Page.Header.PageID
		if pageID < treecontrol.FirstDataPageID ||
			(index > 0 && pageID <= previousChange) {
			return Prepared{}, fmt.Errorf("%w: change order or identity", ErrInvalidPlan)
		}
		previousChange = pageID
		if change.Page.Header.SpaceID != base.SpaceID ||
			change.Page.Header.Generation != base.Generation ||
			change.Page.Header.Flags != 0 ||
			change.Page.Header.LSN != 0 ||
			!isTreePage(change.Page.Header.Type) {
			return Prepared{}, fmt.Errorf(
				"%w: Page %d identity, generation, type, or LSN",
				ErrInvalidPlan,
				pageID,
			)
		}
		if _, err := btree.Decode(change.Page); err != nil {
			return Prepared{}, fmt.Errorf(
				"%w: decode Page %d: %v",
				ErrInvalidPlan,
				pageID,
				err,
			)
		}
		recordType := wal.TypeFullPageImage
		if _, exists := allocated[pageID]; exists {
			if change.ExpectedLSN != 0 {
				return Prepared{}, fmt.Errorf(
					"%w: allocated Page %d expected LSN",
					ErrInvalidPlan,
					pageID,
				)
			}
			recordType = wal.TypePageInit
		} else if pageID >= base.NextPageID || change.ExpectedLSN == 0 {
			return Prepared{}, fmt.Errorf(
				"%w: existing Page %d expected state",
				ErrInvalidPlan,
				pageID,
			)
		}
		encoded, err := page.Encode(change.Page)
		if err != nil {
			return Prepared{}, fmt.Errorf(
				"%w: encode Page %d: %v",
				ErrInvalidPlan,
				pageID,
				err,
			)
		}
		changed[pageID] = struct{}{}
		records = append(records, wal.Record{
			Type: recordType, SpaceID: base.SpaceID, PageID: pageID,
			Payload: encoded,
		})
	}
	for pageID := range allocated {
		if _, exists := changed[pageID]; !exists {
			return Prepared{}, fmt.Errorf(
				"%w: allocated Page %d has no change",
				ErrInvalidPlan,
				pageID,
			)
		}
	}
	for pageID := range reused {
		if _, exists := changed[pageID]; !exists {
			return Prepared{}, fmt.Errorf(
				"%w: reused Page %d has no change", ErrInvalidPlan, pageID,
			)
		}
	}
	for pageID := range recycled {
		if _, exists := changed[pageID]; !exists {
			return Prepared{}, fmt.Errorf(
				"%w: recycled Page %d has no change", ErrInvalidPlan, pageID,
			)
		}
	}

	retired, retiredRecords, err := prepareRetired(
		base,
		plan,
		allocated,
		changed,
	)
	if err != nil {
		return Prepared{}, err
	}
	records = append(records, retiredRecords...)
	if plan.RootPageID != base.RootPageID {
		_, newRoot := allocated[plan.RootPageID]
		_, reusedRoot := reused[plan.RootPageID]
		_, recycledRoot := recycled[plan.RootPageID]
		_, oldRootRetired := retired[base.RootPageID]
		if !newRoot && !reusedRoot && !recycledRoot && !oldRootRetired {
			return Prepared{}, fmt.Errorf(
				"%w: root change has no grow or shrink evidence",
				ErrInvalidPlan,
			)
		}
	}

	if plan.NextPageID != base.NextPageID || len(plan.Retired) != 0 {
		payload, err := wal.EncodeAllocatorRedo(wal.AllocatorRedo{
			ExpectedRevision:   base.Revision,
			ExpectedNextPageID: base.NextPageID,
			NextPageID:         plan.NextPageID,
			RetiredPageIDs:     append([]uint64(nil), plan.Retired...),
		})
		if err != nil {
			return Prepared{}, fmt.Errorf("%w: allocator redo: %v", ErrInvalidPlan, err)
		}
		records = append(records, wal.Record{
			Type: wal.TypeAllocator, SpaceID: base.SpaceID,
			PageID: treecontrol.PageID, Payload: payload,
		})
	}
	rootPayload, err := wal.EncodeRootRedo(wal.RootRedo{
		ExpectedRevision:   base.Revision,
		Revision:           base.Revision + 1,
		ExpectedRootPageID: base.RootPageID,
		RootPageID:         plan.RootPageID,
		ExpectedNextPageID: base.NextPageID,
	})
	if err != nil {
		return Prepared{}, fmt.Errorf("%w: root redo: %v", ErrInvalidPlan, err)
	}
	records = append(records, wal.Record{
		Type: wal.TypeRoot, SpaceID: base.SpaceID,
		PageID: treecontrol.PageID, Payload: rootPayload,
	})
	return Prepared{Records: records}, nil
}

func validateReused(
	base treecontrol.State,
	plan btree.MutationPlan,
) (map[uint64]struct{}, error) {
	result := make(map[uint64]struct{}, len(plan.Reused))
	var previous uint64
	for index, pageID := range plan.Reused {
		if pageID < treecontrol.FirstDataPageID || pageID >= base.NextPageID ||
			(index > 0 && pageID <= previous) {
			return nil, fmt.Errorf("%w: reused Page order or identity", ErrInvalidPlan)
		}
		result[pageID] = struct{}{}
		previous = pageID
	}
	return result, nil
}

func validateRecycled(
	base treecontrol.State,
	plan btree.MutationPlan,
	reused map[uint64]struct{},
) (map[uint64]struct{}, error) {
	result := make(map[uint64]struct{}, len(plan.Recycled))
	var previous uint64
	for index, pageID := range plan.Recycled {
		_, alsoReused := reused[pageID]
		if pageID < treecontrol.FirstDataPageID || pageID >= base.NextPageID || alsoReused ||
			(index > 0 && pageID <= previous) {
			return nil, fmt.Errorf("%w: recycled Page order or identity", ErrInvalidPlan)
		}
		result[pageID] = struct{}{}
		previous = pageID
	}
	return result, nil
}

func validateAllocated(
	first, next uint64,
	pageIDs []uint64,
) (map[uint64]struct{}, error) {
	count := next - first
	if count != uint64(len(pageIDs)) {
		return nil, fmt.Errorf("%w: allocated range", ErrInvalidPlan)
	}
	result := make(map[uint64]struct{}, len(pageIDs))
	for index, pageID := range pageIDs {
		if pageID != first+uint64(index) {
			return nil, fmt.Errorf("%w: allocated order or gap", ErrInvalidPlan)
		}
		result[pageID] = struct{}{}
	}
	return result, nil
}

func prepareRetired(
	base treecontrol.State,
	plan btree.MutationPlan,
	allocated, changed map[uint64]struct{},
) (map[uint64]struct{}, []wal.Record, error) {
	retired := make(map[uint64]struct{}, len(plan.Retired))
	records := make([]wal.Record, 0, len(plan.Retired))
	var previous uint64
	for index, pageID := range plan.Retired {
		if pageID < treecontrol.FirstDataPageID ||
			pageID >= base.NextPageID ||
			pageID == plan.RootPageID ||
			(index > 0 && pageID <= previous) {
			return nil, nil, fmt.Errorf(
				"%w: retired Page order or identity",
				ErrInvalidPlan,
			)
		}
		if _, exists := allocated[pageID]; exists {
			return nil, nil, fmt.Errorf("%w: retired allocated Page", ErrInvalidPlan)
		}
		if _, exists := changed[pageID]; exists {
			return nil, nil, fmt.Errorf("%w: retired changed Page", ErrInvalidPlan)
		}
		free := page.Page{Header: page.Header{
			Type: page.TypeFree, SpaceID: base.SpaceID, PageID: pageID,
			Generation: base.Generation,
		}}
		encoded, err := page.Encode(free)
		if err != nil {
			return nil, nil, fmt.Errorf(
				"%w: encode retired Page %d: %v",
				ErrInvalidPlan,
				pageID,
				err,
			)
		}
		retired[pageID] = struct{}{}
		records = append(records, wal.Record{
			Type: wal.TypeFullPageImage, SpaceID: base.SpaceID,
			PageID: pageID, Payload: encoded,
		})
		previous = pageID
	}
	return retired, records, nil
}

func isTreePage(value page.Type) bool {
	return value == page.TypeBTreeInternal || value == page.TypeBTreeLeaf
}
