package discovery_test

import (
	"context"
	"testing"

	"github.com/HW-Yue/Memora/internal/discovery"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/search"
)

func TestPlannerFusesColdRouteSearchAndRelationsWithoutRowBodies(t *testing.T) {
	t.Parallel()

	source := &fakeSource{
		root: router.Node{ID: "route_root", DatabaseID: "db_work", Path: "/", Kind: router.KindRoot},
		children: map[string][]router.Node{
			"route_root": {
				{ID: "route_wrong", DatabaseID: "db_work", ParentID: "route_root", Name: "wrong", Path: "/wrong", Kind: router.KindLeaf},
			},
		},
		leaves: map[string][]router.Locator{
			"route_wrong": {{DatabaseID: "db_work", TableID: "tbl_notes", RowID: "row_wrong", Revision: 1}},
		},
		matches: search.Result{Matches: []search.Match{{
			Locator: search.Locator{DatabaseID: "db_work", TableID: "tbl_notes", RowID: "row_correct", Revision: 2},
			Score:   1,
		}}},
		relations: map[string][]search.Locator{
			"row_correct": {{DatabaseID: "db_work", TableID: "tbl_notes", RowID: "row_related", Revision: 3}},
		},
	}
	planner, err := discovery.New(source, discovery.Options{})
	if err != nil {
		t.Fatal(err)
	}
	resultValue, err := planner.Discover(context.Background(), discovery.Request{
		DatabaseID: "db_work", RoutePaths: []string{"/wrong"},
		Query: "architecture", QueryTerms: []string{"architecture"}, Limit: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if source.rootCalls != 1 || len(resultValue.Candidates) != 3 ||
		resultValue.Candidates[0].RowID != "row_correct" ||
		resultValue.Candidates[0].SearchScore != 1 ||
		resultValue.Candidates[1].RowID != "row_wrong" ||
		resultValue.Candidates[1].RouteScore != 1 ||
		resultValue.Candidates[2].RowID != "row_related" ||
		resultValue.Candidates[2].RelationScore != 1 {
		t.Fatalf("Discover() = %#v, root calls %d", resultValue, source.rootCalls)
	}
}

func TestPlannerMarksRouteBudgetExhaustionButStillRunsSearchFallback(t *testing.T) {
	t.Parallel()

	source := &fakeSource{
		root: router.Node{ID: "route_root", DatabaseID: "db_work", Path: "/", Kind: router.KindRoot},
		children: map[string][]router.Node{
			"route_root": {{ID: "route_branch", DatabaseID: "db_work", Name: "branch", Kind: router.KindBranch}},
		},
		matches: search.Result{Matches: []search.Match{{
			Locator: search.Locator{DatabaseID: "db_work", TableID: "tbl_notes", RowID: "row_rescued", Revision: 1},
			Score:   1,
		}}},
	}
	planner, err := discovery.New(source, discovery.Options{MaxRouteNodes: 1})
	if err != nil {
		t.Fatal(err)
	}
	resultValue, err := planner.Discover(context.Background(), discovery.Request{
		DatabaseID: "db_work", RoutePaths: []string{"/branch/deeper"},
		QueryTerms: []string{"rescue"}, Limit: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resultValue.BudgetExhausted || !resultValue.Truncated ||
		len(resultValue.Candidates) != 1 ||
		resultValue.Candidates[0].RowID != "row_rescued" {
		t.Fatalf("budgeted Discover() = %#v", resultValue)
	}
}

type fakeSource struct {
	root      router.Node
	children  map[string][]router.Node
	leaves    map[string][]router.Locator
	matches   search.Result
	relations map[string][]search.Locator
	rootCalls int
}

func (source *fakeSource) Root(context.Context, string) (router.Node, error) {
	source.rootCalls++
	return source.root, nil
}

func (source *fakeSource) Children(_ context.Context, parentID string) ([]router.Node, error) {
	return source.children[parentID], nil
}

func (source *fakeSource) Leaf(_ context.Context, leafID string, _ int) ([]router.Locator, bool, error) {
	return source.leaves[leafID], false, nil
}

func (source *fakeSource) Match(context.Context, string, string, []string, int) (search.Result, error) {
	return source.matches, nil
}

func (source *fakeSource) Relations(_ context.Context, locator search.Locator, _ int) ([]search.Locator, bool, error) {
	return source.relations[locator.RowID], false, nil
}
