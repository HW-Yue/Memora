package treecommit

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/HW-Yue/Memora/internal/store/btree"
	"github.com/HW-Yue/Memora/internal/store/page"
	"github.com/HW-Yue/Memora/internal/store/treecontrol"
	"github.com/HW-Yue/Memora/internal/store/wal"
)

func TestPrepareOrdersTreeRedoAndDeepCopies(t *testing.T) {
	base := treecontrol.State{
		SpaceID: 7, Generation: 3, Revision: 7,
		RootPageID: 2, NextPageID: 4, LSN: 99,
	}
	existing := commitNodePage(t, 7, 3, 2, btree.KindLeaf)
	allocated := commitNodePage(t, 7, 3, 4, btree.KindInternal)
	plan := btree.MutationPlan{
		RootPageID: 4,
		NextPageID: 5,
		Changes: []btree.PageChange{
			{Page: existing, ExpectedLSN: 11},
			{Page: allocated},
		},
		Allocated: []uint64{4},
		Retired:   []uint64{3},
	}

	prepared, err := Prepare(base, plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Records) != 5 {
		t.Fatalf("record count = %d", len(prepared.Records))
	}
	gotTypes := make([]wal.Type, len(prepared.Records))
	gotPages := make([]uint64, len(prepared.Records))
	for index, record := range prepared.Records {
		gotTypes[index], gotPages[index] = record.Type, record.PageID
		if record.SpaceID != 7 || record.LSN != 0 || record.TransactionID != 0 {
			t.Fatalf("record %d identity = %#v", index, record)
		}
	}
	if !reflect.DeepEqual(gotTypes, []wal.Type{
		wal.TypeFullPageImage,
		wal.TypePageInit,
		wal.TypeFullPageImage,
		wal.TypeAllocator,
		wal.TypeRoot,
	}) || !reflect.DeepEqual(gotPages, []uint64{2, 4, 3, 1, 1}) {
		t.Fatalf("record order types=%v pages=%v", gotTypes, gotPages)
	}
	retired, err := page.Decode(prepared.Records[2].Payload)
	if err != nil || retired.Header.Type != page.TypeFree ||
		retired.Header.Generation != 3 || retired.Header.PageID != 3 {
		t.Fatalf("retired Page = %#v, %v", retired, err)
	}
	wantAllocator, err := wal.EncodeAllocatorRedo(wal.AllocatorRedo{
		ExpectedRevision:   7,
		ExpectedNextPageID: 4,
		NextPageID:         5,
		RetiredPageIDs:     []uint64{3},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantRoot, err := wal.EncodeRootRedo(wal.RootRedo{
		ExpectedRevision:   7,
		Revision:           8,
		ExpectedRootPageID: 2,
		RootPageID:         4,
		ExpectedNextPageID: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(prepared.Records[3].Payload, wantAllocator) ||
		!reflect.DeepEqual(prepared.Records[4].Payload, wantRoot) {
		t.Fatal("metadata payload mismatch")
	}

	firstPayload := append([]byte(nil), prepared.Records[0].Payload...)
	plan.Changes[0].Page.Payload[0] ^= 1
	if !reflect.DeepEqual(prepared.Records[0].Payload, firstPayload) {
		t.Fatal("prepared output aliases input")
	}
	prepared.Records[0].Payload[0] ^= 1
	again, err := Prepare(base, btree.MutationPlan{
		RootPageID: 4,
		NextPageID: 5,
		Changes: []btree.PageChange{
			{Page: commitNodePage(t, 7, 3, 2, btree.KindLeaf), ExpectedLSN: 11},
			{Page: commitNodePage(t, 7, 3, 4, btree.KindLeaf)},
		},
		Allocated: []uint64{4},
		Retired:   []uint64{3},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(firstPayload, again.Records[0].Payload) {
		t.Fatal("identical fresh input produced different payload")
	}
}

func TestPrepareBootstrapRecordsRecoverAndReopen(t *testing.T) {
	base := treecontrol.Bootstrap(7)
	prepared, err := Prepare(base, btree.MutationPlan{
		RootPageID: 2,
		NextPageID: 3,
		Changes: []btree.PageChange{{
			Page: commitNodePage(t, 7, 1, 2, btree.KindLeaf),
		}},
		Allocated: []uint64{2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := []wal.Type{
		prepared.Records[0].Type,
		prepared.Records[1].Type,
		prepared.Records[2].Type,
	}; !reflect.DeepEqual(got, []wal.Type{
		wal.TypePageInit, wal.TypeAllocator, wal.TypeRoot,
	}) {
		t.Fatalf("bootstrap record order = %v", got)
	}

	segmentPath := filepath.Join(t.TempDir(), "redo.wal")
	segment, err := wal.Create(segmentPath, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := wal.NewTransactionWriter(segment)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Commit(1, prepared.Records); err != nil {
		t.Fatal(err)
	}
	pagePath := filepath.Join(t.TempDir(), "tree.pages")
	manager, err := page.Create(pagePath, 7)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wal.Recover(
		segment,
		map[uint64]wal.PageStore{7: manager},
	); err != nil {
		t.Fatal(err)
	}
	if err := segment.Close(); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := page.Open(pagePath, 7)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	controlPage, err := reopened.Read(treecontrol.PageID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := treecontrol.Decode(controlPage, 7)
	if err != nil || state.Generation != 1 || state.Revision != 1 ||
		state.RootPageID != 2 || state.NextPageID != 3 {
		t.Fatalf("reopened control = %#v, %v", state, err)
	}
	root, err := reopened.Read(2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := btree.Decode(root); err != nil {
		t.Fatalf("reopened root: %v", err)
	}
}

func TestPrepareRootShrinkRetiresOldRoot(t *testing.T) {
	prepared, err := Prepare(treecontrol.State{
		SpaceID: 7, Generation: 3, Revision: 7,
		RootPageID: 3, NextPageID: 4, LSN: 99,
	}, btree.MutationPlan{
		RootPageID: 2,
		NextPageID: 4,
		Retired:    []uint64{3},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Records) != 3 ||
		prepared.Records[0].Type != wal.TypeFullPageImage ||
		prepared.Records[0].PageID != 3 ||
		prepared.Records[1].Type != wal.TypeAllocator ||
		prepared.Records[2].Type != wal.TypeRoot {
		t.Fatalf("shrink records = %#v", prepared.Records)
	}
	free, err := page.Decode(prepared.Records[0].Payload)
	if err != nil || free.Header.Type != page.TypeFree ||
		free.Header.Generation != 3 {
		t.Fatalf("retired root = %#v, %v", free, err)
	}
}

func TestPrepareRejectsInvalidPlansAtomically(t *testing.T) {
	type fixture struct {
		base treecontrol.State
		plan btree.MutationPlan
	}
	valid := func() fixture {
		return fixture{
			base: treecontrol.State{
				SpaceID: 7, Generation: 3, Revision: 7,
				RootPageID: 2, NextPageID: 4, LSN: 99,
			},
			plan: btree.MutationPlan{
				RootPageID: 2,
				NextPageID: 4,
				Changes: []btree.PageChange{{
					Page:        commitNodePage(t, 7, 3, 2, btree.KindLeaf),
					ExpectedLSN: 11,
				}},
			},
		}
	}
	tests := []struct {
		name   string
		mutate func(*fixture)
	}{
		{"bad control", func(value *fixture) { value.base.Revision = 0 }},
		{"revision overflow", func(value *fixture) { value.base.Revision = ^uint64(0) }},
		{"root below data", func(value *fixture) { value.plan.RootPageID = 1 }},
		{"root at high-water", func(value *fixture) { value.plan.RootPageID = 4 }},
		{"allocator regresses", func(value *fixture) { value.plan.NextPageID = 3 }},
		{"empty", func(value *fixture) { value.plan.Changes = nil }},
		{"change order", func(value *fixture) {
			value.plan.Changes = append(value.plan.Changes, btree.PageChange{
				Page:        commitNodePage(t, 7, 3, 2, btree.KindLeaf),
				ExpectedLSN: 11,
			})
		}},
		{"wrong space", func(value *fixture) {
			value.plan.Changes[0].Page.Header.SpaceID++
		}},
		{"wrong generation", func(value *fixture) {
			value.plan.Changes[0].Page.Header.Generation++
		}},
		{"wrong type", func(value *fixture) {
			value.plan.Changes[0].Page.Header.Type = page.TypeData
		}},
		{"Page LSN", func(value *fixture) {
			value.plan.Changes[0].Page.Header.LSN = 1
		}},
		{"existing expected LSN", func(value *fixture) {
			value.plan.Changes[0].ExpectedLSN = 0
		}},
		{"bad Node payload", func(value *fixture) {
			value.plan.Changes[0].Page.Payload = nil
		}},
		{"allocated gap", func(value *fixture) {
			value.plan.NextPageID = 6
			value.plan.Allocated = []uint64{4, 6}
			value.plan.Changes = append(value.plan.Changes, btree.PageChange{
				Page: commitNodePage(t, 7, 3, 5, btree.KindLeaf),
			})
		}},
		{"allocated missing change", func(value *fixture) {
			value.plan.NextPageID = 5
			value.plan.Allocated = []uint64{4}
		}},
		{"allocated expected LSN", func(value *fixture) {
			value.plan.NextPageID = 5
			value.plan.Allocated = []uint64{4}
			value.plan.Changes = append(value.plan.Changes, btree.PageChange{
				Page:        commitNodePage(t, 7, 3, 4, btree.KindLeaf),
				ExpectedLSN: 1,
			})
		}},
		{"retired order", func(value *fixture) {
			value.plan.Retired = []uint64{3, 2}
		}},
		{"retired duplicate", func(value *fixture) {
			value.plan.Retired = []uint64{3, 3}
		}},
		{"retired root", func(value *fixture) {
			value.plan.Retired = []uint64{2}
		}},
		{"retired changed", func(value *fixture) {
			value.plan.RootPageID = 3
			value.plan.Retired = []uint64{2}
		}},
		{"retired above old high-water", func(value *fixture) {
			value.plan.Retired = []uint64{4}
		}},
		{"root change without evidence", func(value *fixture) {
			value.plan.RootPageID = 3
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := valid()
			test.mutate(&value)
			prepared, err := Prepare(value.base, value.plan)
			if !errors.Is(err, ErrInvalidPlan) {
				t.Fatalf("Prepare() error = %v", err)
			}
			if len(prepared.Records) != 0 {
				t.Fatalf("Prepare() returned partial records: %#v", prepared)
			}
		})
	}
}

func commitNodePage(
	t *testing.T,
	spaceID, generation, pageID uint64,
	kind btree.Kind,
) page.Page {
	t.Helper()
	pageType := page.TypeBTreeLeaf
	node := btree.Node{Kind: kind}
	if kind == btree.KindInternal {
		pageType = page.TypeBTreeInternal
		node.Level = 1
		node.LeftmostChild = 2
	}
	value, err := btree.Encode(page.Header{
		Type: pageType, SpaceID: spaceID, PageID: pageID,
		Generation: generation,
	}, node)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
