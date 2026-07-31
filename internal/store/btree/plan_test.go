package btree

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"sort"
	"testing"

	"github.com/HW-Yue/Memora/internal/store/page"
)

func TestMutationPlannerUpsertSingleLeaf(t *testing.T) {
	root := planNodePage(t, 7, 3, 1, 41, Node{Kind: KindLeaf})
	reader := &mapReader{pages: map[uint64]page.Page{1: root}}
	planner := mustMutationPlanner(t, 7, 3, 1, 2, reader)
	key, value := []byte("k"), []byte("value")
	if err := planner.Upsert(key, value); err != nil {
		t.Fatal(err)
	}
	key[0], value[0] = 'x', 'x'
	plan := planner.Plan()
	if plan.RootPageID != 1 || plan.NextPageID != 2 || len(plan.Changes) != 1 ||
		len(plan.Allocated) != 0 || len(plan.Retired) != 0 || len(reader.reads) != 1 {
		t.Fatalf("plan = %+v reads=%v", plan, reader.reads)
	}
	if plan.Changes[0].Page.Header.PageID != 1 || plan.Changes[0].ExpectedLSN != 41 ||
		plan.Changes[0].Page.Header.LSN != 0 {
		t.Fatalf("change = %+v", plan.Changes[0])
	}
	node, err := Decode(plan.Changes[0].Page)
	if err != nil || len(node.LeafEntries) != 1 || string(node.LeafEntries[0].Key) != "k" ||
		string(node.LeafEntries[0].Value) != "value" {
		t.Fatalf("planned node = %+v, %v", node, err)
	}
	plan.Changes[0].Page.Payload[0] ^= 1
	again := planner.Plan()
	if _, err := Decode(again.Changes[0].Page); err != nil {
		t.Fatalf("Plan output aliases planner state: %v", err)
	}
}

func TestMutationPlannerUpsertPropagatesSplitToNewRoot(t *testing.T) {
	large := bytes.Repeat([]byte("v"), 6_000)
	root := planNodePage(t, 7, 3, 1, 9, Node{Kind: KindLeaf, LeafEntries: []LeafEntry{
		{Key: []byte("a"), Value: large}, {Key: []byte("c"), Value: large},
	}})
	planner := mustMutationPlanner(t, 7, 3, 1, 2, &mapReader{pages: map[uint64]page.Page{1: root}})
	if err := planner.Upsert([]byte("b"), large); err != nil {
		t.Fatal(err)
	}
	plan := planner.Plan()
	if plan.RootPageID != 3 || plan.NextPageID != 4 ||
		!reflect.DeepEqual(plan.Allocated, []uint64{2, 3}) || len(plan.Changes) != 3 {
		t.Fatalf("split plan = root=%d next=%d allocated=%v changes=%d", plan.RootPageID, plan.NextPageID, plan.Allocated, len(plan.Changes))
	}
	assertPlanValues(t, map[uint64]page.Page{1: root}, plan, map[string][]byte{
		"a": large, "b": large, "c": large,
	})
}

func TestMutationPlannerUpsertRecursivelySplitsInternalAndRoot(t *testing.T) {
	longKey := func(prefix byte) []byte {
		return append([]byte{prefix}, bytes.Repeat([]byte{prefix}, 4_099)...)
	}
	value := bytes.Repeat([]byte("v"), 4_000)
	pages := map[uint64]page.Page{
		1: planNodePage(t, 7, 3, 1, 11, Node{Kind: KindInternal, Level: 1, LeftmostChild: 2, InternalEntries: []InternalEntry{
			{Key: longKey('b'), RightChild: 3}, {Key: longKey('d'), RightChild: 4},
			{Key: longKey('f'), RightChild: 5},
		}}),
		2: planNodePage(t, 7, 3, 2, 12, Node{Kind: KindLeaf, NextLeafPageID: 3, LeafEntries: []LeafEntry{{Key: longKey('a'), Value: []byte("a")}}}),
		3: planNodePage(t, 7, 3, 3, 13, Node{Kind: KindLeaf, NextLeafPageID: 4, LeafEntries: []LeafEntry{{Key: longKey('c'), Value: []byte("c")}}}),
		4: planNodePage(t, 7, 3, 4, 14, Node{Kind: KindLeaf, NextLeafPageID: 5, LeafEntries: []LeafEntry{{Key: longKey('e'), Value: []byte("e")}}}),
		5: planNodePage(t, 7, 3, 5, 15, Node{Kind: KindLeaf, LeafEntries: []LeafEntry{
			{Key: longKey('g'), Value: value}, {Key: longKey('h'), Value: value},
		}}),
	}
	planner := mustMutationPlanner(t, 7, 3, 1, 6, &mapReader{pages: pages})
	if err := planner.Upsert(longKey('i'), value); err != nil {
		t.Fatal(err)
	}
	plan := planner.Plan()
	if plan.RootPageID != 8 || plan.NextPageID != 9 ||
		!reflect.DeepEqual(plan.Allocated, []uint64{6, 7, 8}) {
		t.Fatalf("recursive split = root=%d next=%d allocated=%v", plan.RootPageID, plan.NextPageID, plan.Allocated)
	}
	root := mustPlanNode(t, plan, 8)
	if root.Level != 2 || root.LeftmostChild != 1 || len(root.InternalEntries) != 1 ||
		root.InternalEntries[0].RightChild != 7 {
		t.Fatalf("new root = %+v", root)
	}
	assertPlanValues(t, pages, plan, map[string][]byte{
		string(longKey('a')): []byte("a"), string(longKey('c')): []byte("c"),
		string(longKey('e')): []byte("e"), string(longKey('g')): value,
		string(longKey('h')): value, string(longKey('i')): value,
	})
}

func TestMutationPlannerDeleteRepairsAndShrinksRoot(t *testing.T) {
	pages := twoLeafPlanTree(t)
	planner := mustMutationPlanner(t, 7, 3, 1, 4, &mapReader{pages: pages})
	if err := planner.Delete([]byte("a")); err != nil {
		t.Fatal(err)
	}
	plan := planner.Plan()
	if plan.RootPageID != 2 || plan.NextPageID != 4 ||
		!reflect.DeepEqual(plan.Retired, []uint64{1, 3}) || len(plan.Changes) != 1 {
		t.Fatalf("shrink plan = root=%d retired=%v changes=%d", plan.RootPageID, plan.Retired, len(plan.Changes))
	}
	assertPlanValues(t, pages, plan, map[string][]byte{
		"b": []byte("2"), "m": []byte("3"), "n": []byte("4"),
	})
}

func TestMutationPlannerDeleteUsesLeftFallbackAndKeepsRoot(t *testing.T) {
	pages := map[uint64]page.Page{
		1: planNodePage(t, 7, 3, 1, 1, Node{Kind: KindInternal, Level: 1, LeftmostChild: 2, InternalEntries: []InternalEntry{
			{Key: []byte("m"), RightChild: 3}, {Key: []byte("t"), RightChild: 4},
		}}),
		2: planNodePage(t, 7, 3, 2, 2, Node{Kind: KindLeaf, NextLeafPageID: 3, LeafEntries: []LeafEntry{{Key: []byte("a"), Value: []byte("1")}}}),
		3: planNodePage(t, 7, 3, 3, 3, Node{Kind: KindLeaf, NextLeafPageID: 4, LeafEntries: []LeafEntry{{Key: []byte("m"), Value: []byte("2")}}}),
		4: planNodePage(t, 7, 3, 4, 4, Node{Kind: KindLeaf, LeafEntries: []LeafEntry{{Key: []byte("t"), Value: []byte("3")}}}),
	}
	planner := mustMutationPlanner(t, 7, 3, 1, 5, &mapReader{pages: pages})
	if err := planner.Delete([]byte("t")); err != nil {
		t.Fatal(err)
	}
	plan := planner.Plan()
	if plan.RootPageID != 1 || !reflect.DeepEqual(plan.Retired, []uint64{4}) {
		t.Fatalf("left fallback plan = root=%d retired=%v", plan.RootPageID, plan.Retired)
	}
	assertPlanValues(t, pages, plan, map[string][]byte{"a": []byte("1"), "m": []byte("2")})
}

func TestMutationPlannerDeleteCascadesInternalMergeAndRootShrink(t *testing.T) {
	pages := map[uint64]page.Page{
		1: planNodePage(t, 7, 3, 1, 1, Node{Kind: KindInternal, Level: 2, LeftmostChild: 2, InternalEntries: []InternalEntry{{Key: []byte("m"), RightChild: 3}}}),
		2: planNodePage(t, 7, 3, 2, 2, Node{Kind: KindInternal, Level: 1, LeftmostChild: 4, InternalEntries: []InternalEntry{{Key: []byte("g"), RightChild: 5}}}),
		3: planNodePage(t, 7, 3, 3, 3, Node{Kind: KindInternal, Level: 1, LeftmostChild: 6, InternalEntries: []InternalEntry{{Key: []byte("t"), RightChild: 7}}}),
		4: planNodePage(t, 7, 3, 4, 4, Node{Kind: KindLeaf, NextLeafPageID: 5, LeafEntries: []LeafEntry{{Key: []byte("a"), Value: []byte("1")}}}),
		5: planNodePage(t, 7, 3, 5, 5, Node{Kind: KindLeaf, NextLeafPageID: 6, LeafEntries: []LeafEntry{{Key: []byte("g"), Value: []byte("2")}}}),
		6: planNodePage(t, 7, 3, 6, 6, Node{Kind: KindLeaf, NextLeafPageID: 7, LeafEntries: []LeafEntry{{Key: []byte("m"), Value: []byte("3")}}}),
		7: planNodePage(t, 7, 3, 7, 7, Node{Kind: KindLeaf, LeafEntries: []LeafEntry{{Key: []byte("t"), Value: []byte("4")}}}),
	}
	planner := mustMutationPlanner(t, 7, 3, 1, 8, &mapReader{pages: pages})
	if err := planner.Delete([]byte("a")); err != nil {
		t.Fatal(err)
	}
	plan := planner.Plan()
	if plan.RootPageID != 2 || !reflect.DeepEqual(plan.Retired, []uint64{1, 3, 5}) {
		t.Fatalf("cascade plan = root=%d retired=%v", plan.RootPageID, plan.Retired)
	}
	assertPlanValues(t, pages, plan, map[string][]byte{
		"g": []byte("2"), "m": []byte("3"), "t": []byte("4"),
	})
}

func TestMutationPlannerKeepsTransientAllocationsContiguous(t *testing.T) {
	large := bytes.Repeat([]byte("v"), 6_000)
	base := map[uint64]page.Page{1: planNodePage(t, 7, 3, 1, 9, Node{Kind: KindLeaf, LeafEntries: []LeafEntry{
		{Key: []byte("a"), Value: large}, {Key: []byte("c"), Value: large},
	}})}
	planner := mustMutationPlanner(t, 7, 3, 1, 2, &mapReader{pages: base})
	if err := planner.Upsert([]byte("b"), large); err != nil {
		t.Fatal(err)
	}
	if err := planner.Delete([]byte("b")); err != nil {
		t.Fatal(err)
	}
	plan := planner.Plan()
	if plan.RootPageID != 1 || plan.NextPageID != 4 ||
		!reflect.DeepEqual(plan.Allocated, []uint64{2, 3}) || len(plan.Retired) != 0 {
		t.Fatalf("transient allocation plan = root=%d next=%d allocated=%v retired=%v", plan.RootPageID, plan.NextPageID, plan.Allocated, plan.Retired)
	}
	for _, pageID := range plan.Allocated {
		_ = mustPlanNode(t, plan, pageID)
	}
	assertPlanValues(t, base, plan, map[string][]byte{"a": large, "c": large})
}

func TestMutationPlannerMultipleOperationsUseOverlayAtomically(t *testing.T) {
	root := planNodePage(t, 7, 3, 1, 8, Node{Kind: KindLeaf})
	reader := &mapReader{pages: map[uint64]page.Page{1: root}}
	planner := mustMutationPlanner(t, 7, 3, 1, 2, reader)
	if err := planner.Upsert([]byte("a"), []byte("one")); err != nil {
		t.Fatal(err)
	}
	before := planner.Plan()
	if err := planner.Upsert([]byte("b"), bytes.Repeat([]byte("x"), page.Size)); !errors.Is(err, ErrNoSpace) {
		t.Fatalf("oversize Upsert error = %v, want ErrNoSpace", err)
	}
	if got := planner.Plan(); !reflect.DeepEqual(got, before) {
		t.Fatalf("failed mutation changed plan\nbefore=%+v\nafter=%+v", before, got)
	}
	if err := planner.Delete([]byte("missing")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("absent Delete error = %v, want ErrNotFound", err)
	}
	if got := planner.Plan(); !reflect.DeepEqual(got, before) {
		t.Fatal("failed delete changed plan")
	}
	if err := planner.Upsert([]byte("b"), []byte("two")); err != nil {
		t.Fatal(err)
	}
	if len(reader.reads) != 1 {
		t.Fatalf("Reader reads = %v, want one cached load", reader.reads)
	}
	assertPlanValues(t, map[uint64]page.Page{1: root}, planner.Plan(), map[string][]byte{
		"a": []byte("one"), "b": []byte("two"),
	})
}

func TestMutationPlannerRejectsCorruptionAndFaultsAtomically(t *testing.T) {
	readFault := errors.New("read fault")
	validLeaf := planNodePage(t, 7, 3, 2, 2, Node{Kind: KindLeaf, LeafEntries: []LeafEntry{{Key: []byte("a"), Value: []byte("1")}}})
	wrongGeneration := clonePage(validLeaf)
	wrongGeneration.Header.Generation++
	duplicateChild := planNodePage(t, 7, 3, 1, 1, Node{Kind: KindInternal, Level: 1, LeftmostChild: 2, InternalEntries: []InternalEntry{{Key: []byte("m"), RightChild: 2}}})
	boundaryRoot := planNodePage(t, 7, 3, 1, 1, Node{Kind: KindInternal, Level: 1, LeftmostChild: 2, InternalEntries: []InternalEntry{{Key: []byte("m"), RightChild: 3}}})
	boundaryLeaf := planNodePage(t, 7, 3, 2, 2, Node{Kind: KindLeaf, LeafEntries: []LeafEntry{{Key: []byte("z"), Value: []byte("1")}}})
	cycle := planNodePage(t, 7, 3, 1, 1, Node{Kind: KindInternal, Level: 1, LeftmostChild: 1})

	tests := []struct {
		name       string
		root, next uint64
		reader     Reader
		want       error
	}{
		{"generation", 2, 3, &mapReader{pages: map[uint64]page.Page{2: wrongGeneration}}, ErrCorrupt},
		{"duplicate child", 1, 3, &mapReader{pages: map[uint64]page.Page{1: duplicateChild, 2: validLeaf}}, ErrCorrupt},
		{"boundary", 1, 4, &mapReader{pages: map[uint64]page.Page{1: boundaryRoot, 2: boundaryLeaf}}, ErrCorrupt},
		{"cycle", 1, 2, &mapReader{pages: map[uint64]page.Page{1: cycle}}, ErrCorrupt},
		{"reader", 1, 2, ReaderFunc(func(uint64) (page.Page, error) { return page.Page{}, readFault }), readFault},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			planner := mustMutationPlanner(t, 7, 3, test.root, test.next, test.reader)
			before := planner.Plan()
			if err := planner.Upsert([]byte("a"), []byte("v")); !errors.Is(err, test.want) {
				t.Fatalf("Upsert error = %v, want %v", err, test.want)
			}
			if got := planner.Plan(); !reflect.DeepEqual(got, before) {
				t.Fatalf("corruption/fault changed plan: %+v", got)
			}
		})
	}

	overflowRoot := planNodePage(t, 7, 3, 1, 0, Node{Kind: KindLeaf, LeafEntries: []LeafEntry{
		{Key: []byte("a"), Value: bytes.Repeat([]byte("a"), 8_000)},
		{Key: []byte("c"), Value: bytes.Repeat([]byte("c"), 8_000)},
	}})
	planner := mustMutationPlanner(t, 7, 3, 1, math.MaxUint64, &mapReader{pages: map[uint64]page.Page{1: overflowRoot}})
	before := planner.Plan()
	if err := planner.Upsert([]byte("b"), bytes.Repeat([]byte("v"), 1_000)); !errors.Is(err, ErrPageIDOverflow) {
		t.Fatalf("overflow error = %v", err)
	}
	if got := planner.Plan(); !reflect.DeepEqual(got, before) {
		t.Fatal("overflow changed plan")
	}
}

func TestMutationPlannerMatchesSeededReferenceModel(t *testing.T) {
	const seed = int64(9701)
	random := rand.New(rand.NewSource(seed))
	base := map[uint64]page.Page{1: planNodePage(t, 7, 3, 1, 17, Node{Kind: KindLeaf})}
	planner := mustMutationPlanner(t, 7, 3, 1, 2, &mapReader{pages: base})
	model := make(map[string][]byte)
	for step := 0; step < 240; step++ {
		key := fmt.Sprintf("k%03d", random.Intn(80))
		if random.Intn(4) == 0 {
			err := planner.Delete([]byte(key))
			if _, exists := model[key]; exists {
				if err != nil {
					t.Fatalf("seed=%d step=%d delete %s: %v", seed, step, key, err)
				}
				delete(model, key)
			} else if !errors.Is(err, ErrNotFound) {
				t.Fatalf("seed=%d step=%d absent delete: %v", seed, step, err)
			}
		} else {
			value := bytes.Repeat([]byte{byte(step%251 + 1)}, 300+random.Intn(701))
			if err := planner.Upsert([]byte(key), value); err != nil {
				t.Fatalf("seed=%d step=%d upsert %s: %v", seed, step, key, err)
			}
			model[key] = bytes.Clone(value)
		}
		assertPlanValues(t, base, planner.Plan(), model)
	}
}

func TestMutationPlannerDeepCopiesAndIsDeterministic(t *testing.T) {
	pages := twoLeafPlanTree(t)
	left := mustMutationPlanner(t, 7, 3, 1, 4, &mapReader{pages: pages})
	right := mustMutationPlanner(t, 7, 3, 1, 4, &mapReader{pages: pages})
	key, value := []byte("c"), bytes.Repeat([]byte("v"), 9_000)
	if err := left.Upsert(key, value); err != nil {
		t.Fatal(err)
	}
	if err := right.Upsert(bytes.Clone(key), bytes.Clone(value)); err != nil {
		t.Fatal(err)
	}
	key[0], value[0] = 'x', 'x'
	if !reflect.DeepEqual(left.Plan(), right.Plan()) {
		t.Fatal("identical input produced different plans")
	}
	for _, original := range pages {
		if _, err := Decode(original); err != nil {
			t.Fatalf("Reader Page was mutated: %v", err)
		}
	}
}

func TestMutationPlannerValidatesConfiguration(t *testing.T) {
	reader := ReaderFunc(func(uint64) (page.Page, error) { return page.Page{}, nil })
	for name, args := range map[string][4]uint64{
		"zero space": {0, 3, 1, 2}, "zero root": {7, 3, 0, 2},
		"zero next": {7, 3, 1, 0}, "root at high-water": {7, 3, 2, 2},
	} {
		t.Run(name, func(t *testing.T) {
			values := args
			if _, err := NewMutationPlanner(values[0], values[1], values[2], values[3], reader); !errors.Is(err, ErrInvalid) {
				t.Fatalf("NewMutationPlanner error = %v", err)
			}
		})
	}
	if _, err := NewMutationPlanner(7, 3, 1, 2, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil Reader error = %v", err)
	}
}

func mustMutationPlanner(
	t *testing.T,
	spaceID, generation, rootPageID, nextPageID uint64,
	reader Reader,
) *MutationPlanner {
	t.Helper()
	planner, err := NewMutationPlanner(spaceID, generation, rootPageID, nextPageID, reader)
	if err != nil {
		t.Fatal(err)
	}
	return planner
}

func planNodePage(
	t *testing.T,
	spaceID, generation, pageID, lsn uint64,
	node Node,
) page.Page {
	t.Helper()
	pageType := page.TypeBTreeLeaf
	if node.Kind == KindInternal {
		pageType = page.TypeBTreeInternal
	}
	value, err := Encode(page.Header{
		Type: pageType, SpaceID: spaceID, PageID: pageID,
		Generation: generation, LSN: lsn,
	}, node)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func twoLeafPlanTree(t *testing.T) map[uint64]page.Page {
	t.Helper()
	return map[uint64]page.Page{
		1: planNodePage(t, 7, 3, 1, 1, Node{Kind: KindInternal, Level: 1, LeftmostChild: 2, InternalEntries: []InternalEntry{{Key: []byte("m"), RightChild: 3}}}),
		2: planNodePage(t, 7, 3, 2, 2, Node{Kind: KindLeaf, NextLeafPageID: 3, LeafEntries: []LeafEntry{
			{Key: []byte("a"), Value: []byte("1")}, {Key: []byte("b"), Value: []byte("2")},
		}}),
		3: planNodePage(t, 7, 3, 3, 3, Node{Kind: KindLeaf, LeafEntries: []LeafEntry{
			{Key: []byte("m"), Value: []byte("3")}, {Key: []byte("n"), Value: []byte("4")},
		}}),
	}
}

func mustPlanNode(t *testing.T, plan MutationPlan, pageID uint64) Node {
	t.Helper()
	for _, change := range plan.Changes {
		if change.Page.Header.PageID == pageID {
			node, err := Decode(change.Page)
			if err != nil {
				t.Fatal(err)
			}
			return node
		}
	}
	t.Fatalf("planned Page %d not found", pageID)
	return Node{}
}

func assertPlanValues(
	t *testing.T,
	base map[uint64]page.Page,
	plan MutationPlan,
	want map[string][]byte,
) {
	t.Helper()
	pages := make(map[uint64]page.Page, len(base)+len(plan.Changes))
	for pageID, value := range base {
		pages[pageID] = clonePage(value)
	}
	for _, change := range plan.Changes {
		pages[change.Page.Header.PageID] = clonePage(change.Page)
	}
	for _, pageID := range plan.Retired {
		delete(pages, pageID)
	}
	searcher := mustSearcher(t, 7, plan.RootPageID, &mapReader{pages: pages})
	keys := make([]string, 0, len(want))
	for key := range want {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		got, err := searcher.Get([]byte(key))
		if err != nil || !bytes.Equal(got, want[key]) {
			t.Fatalf("Get(%q) = %x, %v; want %x", key, got, err, want[key])
		}
	}
	assertReachablePlanTree(t, pages, plan.RootPageID, keys)
}

func assertReachablePlanTree(
	t *testing.T,
	pages map[uint64]page.Page,
	rootPageID uint64,
	wantKeys []string,
) {
	t.Helper()
	seen := make(map[uint64]struct{})
	leafIDs := make([]uint64, 0)
	var walk func(uint64, int)
	walk = func(pageID uint64, expectedLevel int) {
		if _, duplicate := seen[pageID]; duplicate {
			t.Fatalf("reachable Page cycle/duplicate at %d", pageID)
		}
		seen[pageID] = struct{}{}
		value, exists := pages[pageID]
		if !exists {
			t.Fatalf("reachable Page %d missing", pageID)
		}
		node, err := Decode(value)
		if err != nil {
			t.Fatalf("Decode Page %d: %v", pageID, err)
		}
		if expectedLevel >= 0 && int(node.Level) != expectedLevel {
			t.Fatalf("Page %d level=%d want=%d", pageID, node.Level, expectedLevel)
		}
		if node.Kind == KindLeaf {
			leafIDs = append(leafIDs, pageID)
			return
		}
		walk(node.LeftmostChild, int(node.Level)-1)
		for _, entry := range node.InternalEntries {
			walk(entry.RightChild, int(node.Level)-1)
		}
	}
	walk(rootPageID, -1)
	actualKeys := make([]string, 0, len(wantKeys))
	for index, pageID := range leafIDs {
		node, _ := Decode(pages[pageID])
		wantNext := uint64(0)
		if index+1 < len(leafIDs) {
			wantNext = leafIDs[index+1]
		}
		if node.NextLeafPageID != wantNext {
			t.Fatalf("leaf %d next=%d want=%d", pageID, node.NextLeafPageID, wantNext)
		}
		for _, entry := range node.LeafEntries {
			actualKeys = append(actualKeys, string(entry.Key))
		}
	}
	if !reflect.DeepEqual(actualKeys, wantKeys) {
		t.Fatalf("leaf keys = %v, want %v", actualKeys, wantKeys)
	}
}
