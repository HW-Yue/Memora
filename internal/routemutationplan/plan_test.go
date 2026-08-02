package routemutationplan_test

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/routemutationplan"
	"github.com/HW-Yue/Memora/internal/router"
)

func TestBuildBranchSplitIsDeterministicCompleteAndReviewOnly(t *testing.T) {
	t.Parallel()
	source := branchSplitFixture()
	proposal := routemutationplan.Proposal{
		Version: routemutationplan.ProposalVersion, ID: "proposal_split", Operation: routemutationplan.OperationSplit,
		Actor: "agent:host", SourceEventID: "event_1", Reason: "capacity review",
		Sources: []routemutationplan.SourceRef{{RouteID: "route_source", ExpectedRevision: 3}},
		Targets: []routemutationplan.TargetProposal{
			{Key: "recent", Name: "recent", Purpose: "Recent notes", ChildRouteIDs: []string{"route_b"}},
			{Key: "archive", Name: "archive", Purpose: "Archived notes", ChildRouteIDs: []string{"route_a"}},
		},
	}
	first, err := routemutationplan.Build(context.Background(), source,
		routemutationplan.Scope{DatabaseID: "db_work", TableID: "tbl_notes"}, proposal)
	if err != nil {
		t.Fatal(err)
	}
	if first.Version != routemutationplan.PlanVersion || first.Status != routemutationplan.StatusReviewRequired ||
		first.PlanID == "" || first.Hash == "" || first.BaseSnapshotHash == "" ||
		len(first.Creates) != 2 || len(first.Moves) != 2 || len(first.MembershipMoves) != 0 || len(first.Deletes) != 1 {
		t.Fatalf("split plan = %#v", first)
	}
	if first.Creates[0].TargetKey != "archive" || first.Moves[0].RouteID != "route_a" {
		t.Fatalf("plan was not canonicalized = %#v", first)
	}
	encoded, _ := json.Marshal(first)
	if strings.Contains(string(encoded), "row values") {
		t.Fatalf("plan leaked Row values: %s", encoded)
	}
	rand.New(rand.NewSource(9)).Shuffle(len(source.nodes), func(left, right int) {
		source.nodes[left], source.nodes[right] = source.nodes[right], source.nodes[left]
	})
	second, err := routemutationplan.Build(context.Background(), source,
		routemutationplan.Scope{DatabaseID: "db_work", TableID: "tbl_notes"}, proposal)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("shuffled plan drift = %v\n%#v\n%#v", err, first, second)
	}
}

func TestBuildRejectsUnsafeOrIncompleteRouteProposals(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		source   *fakeSource
		proposal routemutationplan.Proposal
	}{
		{name: "single-Row leaf split", source: splitFixture(), proposal: splitProposal(
			[]routemutationplan.TargetProposal{
				{Key: "a", Name: "a", Purpose: "A", RowIDs: []string{"row_a"}},
				{Key: "b", Name: "b", Purpose: "B"},
			},
		)},
		{name: "split misses child", source: branchSplitFixture(), proposal: splitProposal(
			[]routemutationplan.TargetProposal{
				{Key: "a", Name: "a", Purpose: "A", ChildRouteIDs: []string{"route_a"}},
				{Key: "b", Name: "b", Purpose: "B"},
			},
		)},
		{name: "split duplicates child", source: branchSplitFixture(), proposal: splitProposal(
			[]routemutationplan.TargetProposal{
				{Key: "a", Name: "a", Purpose: "A", ChildRouteIDs: []string{"route_a", "route_b"}},
				{Key: "b", Name: "b", Purpose: "B", ChildRouteIDs: []string{"route_b"}},
			},
		)},
		{name: "merge non siblings", source: mergeFixture("route_other_parent"), proposal: mergeProposal()},
		{name: "merge distinct Rows", source: distinctRowMergeFixture(), proposal: mergeProposal()},
		{name: "split target conflicts with sibling alias", source: func() *fakeSource {
			value := branchSplitFixture()
			value.nodes = append(value.nodes, router.Node{
				Version: router.Version, ID: "route_sibling", DatabaseID: "db_work", TableID: "tbl_notes",
				ParentID: "route_root", Kind: router.KindLeaf, Name: "sibling", Aliases: []string{"a"},
				Path: "/sibling", Purpose: "Sibling", Revision: 1,
			})
			return value
		}(), proposal: splitProposal([]routemutationplan.TargetProposal{
			{Key: "a", Name: "a", Purpose: "A", ChildRouteIDs: []string{"route_a"}},
			{Key: "b", Name: "b", Purpose: "B", ChildRouteIDs: []string{"route_b"}},
		})},
		{name: "source is outside table", source: func() *fakeSource {
			value := branchSplitFixture()
			value.nodes[1].TableID = "tbl_private"
			return value
		}(), proposal: splitProposal([]routemutationplan.TargetProposal{
			{Key: "a", Name: "a", Purpose: "A", ChildRouteIDs: []string{"route_a"}},
			{Key: "b", Name: "b", Purpose: "B", ChildRouteIDs: []string{"route_b"}},
		})},
		{name: "move cycle", source: moveFixture(), proposal: routemutationplan.Proposal{
			Version: routemutationplan.ProposalVersion, ID: "proposal_move", Operation: routemutationplan.OperationMove,
			Actor: "agent:host", SourceEventID: "event_1", Reason: "reorganize",
			Sources:        []routemutationplan.SourceRef{{RouteID: "route_branch", ExpectedRevision: 2}},
			TargetParentID: "route_leaf",
		}},
		{name: "stale revision", source: branchSplitFixture(), proposal: func() routemutationplan.Proposal {
			value := splitProposal([]routemutationplan.TargetProposal{
				{Key: "a", Name: "a", Purpose: "A", ChildRouteIDs: []string{"route_a"}},
				{Key: "b", Name: "b", Purpose: "B", ChildRouteIDs: []string{"route_b"}},
			})
			value.Sources[0].ExpectedRevision = 2
			return value
		}()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := routemutationplan.Build(context.Background(), test.source,
				routemutationplan.Scope{DatabaseID: "db_work", TableID: "tbl_notes"}, test.proposal)
			var stable interface{ StableCode() string }
			if err == nil || !strings.Contains(err.Error(), "Route mutation plan") ||
				!errors.As(err, &stable) || stable.StableCode() == string(result.CodeInternal) {
				t.Fatalf("unsafe proposal error = %v", err)
			}
		})
	}
}

func TestBuildMergeBranchSplitAndMoveShapes(t *testing.T) {
	t.Parallel()
	scope := routemutationplan.Scope{DatabaseID: "db_work", TableID: "tbl_notes"}

	merged, err := routemutationplan.Build(context.Background(), aliasMergeFixture(), scope, mergeProposal())
	if err != nil || len(merged.Creates) != 1 || len(merged.Deletes) != 2 || len(merged.MembershipMoves) != 1 {
		t.Fatalf("MERGE plan = %#v, %v", merged, err)
	}

	branchSource := &fakeSource{nodes: []router.Node{
		{Version: router.Version, ID: "route_root", DatabaseID: "db_work", TableID: "tbl_notes", Kind: router.KindRoot, Name: "root", Path: "/", Purpose: "Root", Revision: 1},
		{Version: router.Version, ID: "route_source", DatabaseID: "db_work", TableID: "tbl_notes", ParentID: "route_root", Kind: router.KindBranch, Name: "source", Path: "/source", Purpose: "Source", Revision: 3},
		{Version: router.Version, ID: "route_a", DatabaseID: "db_work", TableID: "tbl_notes", ParentID: "route_source", Kind: router.KindLeaf, Name: "a", Path: "/source/a", Purpose: "A", Revision: 1},
		{Version: router.Version, ID: "route_b", DatabaseID: "db_work", TableID: "tbl_notes", ParentID: "route_source", Kind: router.KindLeaf, Name: "b", Path: "/source/b", Purpose: "B", Revision: 1},
	}, locators: map[string][]router.Locator{}}
	branchProposal := splitProposal([]routemutationplan.TargetProposal{
		{Key: "left", Name: "left", Purpose: "Left", ChildRouteIDs: []string{"route_a"}},
		{Key: "right", Name: "right", Purpose: "Right", ChildRouteIDs: []string{"route_b"}},
	})
	branchPlan, err := routemutationplan.Build(context.Background(), branchSource, scope, branchProposal)
	if err != nil || len(branchPlan.Moves) != 2 || len(branchPlan.MembershipMoves) != 0 {
		t.Fatalf("branch SPLIT plan = %#v, %v", branchPlan, err)
	}

	moveSource := moveFixture()
	moveSource.nodes = append(moveSource.nodes, router.Node{
		Version: router.Version, ID: "route_target", DatabaseID: "db_work", TableID: "tbl_notes",
		ParentID: "route_root", Kind: router.KindBranch, Name: "target", Path: "/target", Purpose: "Target", Revision: 4,
	})
	moveProposal := routemutationplan.Proposal{
		Version: routemutationplan.ProposalVersion, ID: "proposal_move", Operation: routemutationplan.OperationMove,
		Actor: "agent:host", SourceEventID: "event_1", Reason: "move",
		Sources:        []routemutationplan.SourceRef{{RouteID: "route_branch", ExpectedRevision: 2}},
		TargetParentID: "route_target",
	}
	moved, err := routemutationplan.Build(context.Background(), moveSource, scope, moveProposal)
	if err != nil || len(moved.Moves) != 1 || moved.Moves[0].ToParentID != "route_target" ||
		len(moved.Creates) != 0 || len(moved.Deletes) != 0 {
		t.Fatalf("MOVE plan = %#v, %v", moved, err)
	}
}

func TestValidateRejectsTamperingAndBranchPlansGuardMovedSubtrees(t *testing.T) {
	t.Parallel()
	scope := routemutationplan.Scope{DatabaseID: "db_work", TableID: "tbl_notes"}
	source := &fakeSource{nodes: []router.Node{
		{Version: router.Version, ID: "route_root", DatabaseID: "db_work", TableID: "tbl_notes", Kind: router.KindRoot, Name: "root", Path: "/", Purpose: "Root", Revision: 1},
		{Version: router.Version, ID: "route_source", DatabaseID: "db_work", TableID: "tbl_notes", ParentID: "route_root", Kind: router.KindBranch, Name: "source", Path: "/source", Purpose: "Source", Revision: 3},
		{Version: router.Version, ID: "route_child", DatabaseID: "db_work", TableID: "tbl_notes", ParentID: "route_source", Kind: router.KindBranch, Name: "child", Path: "/source/child", Purpose: "Child", Revision: 2},
		{Version: router.Version, ID: "route_grandchild", DatabaseID: "db_work", TableID: "tbl_notes", ParentID: "route_child", Kind: router.KindLeaf, Name: "grandchild", Path: "/source/child/grandchild", Purpose: "Grandchild", Revision: 4},
		{Version: router.Version, ID: "route_sibling", DatabaseID: "db_work", TableID: "tbl_notes", ParentID: "route_source", Kind: router.KindLeaf, Name: "sibling", Path: "/source/sibling", Purpose: "Sibling", Revision: 1},
	}, locators: map[string][]router.Locator{}}
	plan, err := routemutationplan.Build(context.Background(), source, scope, splitProposal([]routemutationplan.TargetProposal{
		{Key: "left", Name: "left", Purpose: "Left", ChildRouteIDs: []string{"route_child"}},
		{Key: "right", Name: "right", Purpose: "Right", ChildRouteIDs: []string{"route_sibling"}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	guarded := map[string]bool{}
	for _, guard := range plan.NodeGuards {
		guarded[guard.RouteID] = true
	}
	if !guarded["route_grandchild"] {
		t.Fatalf("moved subtree descendant was not guarded: %#v", plan.NodeGuards)
	}
	if err := routemutationplan.Validate(plan); err != nil {
		t.Fatalf("Validate(generated plan) error = %v", err)
	}
	tampered := plan
	tampered.Creates = append([]routemutationplan.NodeCreate(nil), plan.Creates...)
	tampered.Creates[0].Purpose = "silently changed"
	if err := routemutationplan.Validate(tampered); err == nil {
		t.Fatal("Validate(tampered plan) unexpectedly succeeded")
	}
}

func splitProposal(targets []routemutationplan.TargetProposal) routemutationplan.Proposal {
	return routemutationplan.Proposal{
		Version: routemutationplan.ProposalVersion, ID: "proposal_split", Operation: routemutationplan.OperationSplit,
		Actor: "agent:host", SourceEventID: "event_1", Reason: "split", Targets: targets,
		Sources: []routemutationplan.SourceRef{{RouteID: "route_source", ExpectedRevision: 3}},
	}
}

func mergeProposal() routemutationplan.Proposal {
	return routemutationplan.Proposal{
		Version: routemutationplan.ProposalVersion, ID: "proposal_merge", Operation: routemutationplan.OperationMerge,
		Actor: "agent:host", SourceEventID: "event_1", Reason: "merge",
		Sources: []routemutationplan.SourceRef{{RouteID: "route_a", ExpectedRevision: 1}, {RouteID: "route_b", ExpectedRevision: 1}},
		Targets: []routemutationplan.TargetProposal{{Key: "merged", Name: "merged", Purpose: "Merged"}},
	}
}

func splitFixture() *fakeSource {
	return &fakeSource{
		nodes: []router.Node{
			{Version: router.Version, ID: "route_root", DatabaseID: "db_work", TableID: "tbl_notes", Kind: router.KindRoot, Name: "root", Path: "/", Purpose: "Root", Revision: 1},
			{Version: router.Version, ID: "route_source", DatabaseID: "db_work", TableID: "tbl_notes", ParentID: "route_root", Kind: router.KindLeaf, Name: "source", Path: "/source", Purpose: "Source", Revision: 3},
		},
		locators: map[string][]router.Locator{"route_source": {{DatabaseID: "db_work", TableID: "tbl_notes", RowID: "row_a", Revision: 1}}},
	}
}

func branchSplitFixture() *fakeSource {
	return &fakeSource{nodes: []router.Node{
		{Version: router.Version, ID: "route_root", DatabaseID: "db_work", TableID: "tbl_notes", Kind: router.KindRoot, Name: "root", Path: "/", Purpose: "Root", Revision: 1},
		{Version: router.Version, ID: "route_source", DatabaseID: "db_work", TableID: "tbl_notes", ParentID: "route_root", Kind: router.KindBranch, Name: "source", Path: "/source", Purpose: "Source", Revision: 3},
		{Version: router.Version, ID: "route_a", DatabaseID: "db_work", TableID: "tbl_notes", ParentID: "route_source", Kind: router.KindLeaf, Name: "child-a", Path: "/source/child-a", Purpose: "A", Revision: 1},
		{Version: router.Version, ID: "route_b", DatabaseID: "db_work", TableID: "tbl_notes", ParentID: "route_source", Kind: router.KindLeaf, Name: "child-b", Path: "/source/child-b", Purpose: "B", Revision: 1},
	}, locators: map[string][]router.Locator{}}
}

func mergeFixture(secondParent string) *fakeSource {
	return &fakeSource{nodes: []router.Node{
		{Version: router.Version, ID: "route_root", DatabaseID: "db_work", TableID: "tbl_notes", Kind: router.KindRoot, Name: "root", Path: "/", Purpose: "Root", Revision: 1},
		{Version: router.Version, ID: "route_other_parent", DatabaseID: "db_work", TableID: "tbl_notes", ParentID: "route_root", Kind: router.KindBranch, Name: "other", Path: "/other", Purpose: "Other", Revision: 1},
		{Version: router.Version, ID: "route_a", DatabaseID: "db_work", TableID: "tbl_notes", ParentID: "route_root", Kind: router.KindLeaf, Name: "a", Path: "/a", Purpose: "A", Revision: 1},
		{Version: router.Version, ID: "route_b", DatabaseID: "db_work", TableID: "tbl_notes", ParentID: secondParent, Kind: router.KindLeaf, Name: "b", Path: "/b", Purpose: "B", Revision: 1},
	}, locators: map[string][]router.Locator{"route_a": {}, "route_b": {}}}
}

func aliasMergeFixture() *fakeSource {
	value := mergeFixture("route_root")
	locator := router.Locator{DatabaseID: "db_work", TableID: "tbl_notes", RowID: "row_same", Revision: 1}
	value.locators["route_a"] = []router.Locator{locator}
	value.locators["route_b"] = []router.Locator{locator}
	return value
}

func distinctRowMergeFixture() *fakeSource {
	value := mergeFixture("route_root")
	value.locators["route_a"] = []router.Locator{{DatabaseID: "db_work", TableID: "tbl_notes", RowID: "row_a", Revision: 1}}
	value.locators["route_b"] = []router.Locator{{DatabaseID: "db_work", TableID: "tbl_notes", RowID: "row_b", Revision: 1}}
	return value
}

func oversizedMergeFixture() *fakeSource {
	value := mergeFixture("route_root")
	for index := 0; index < 101; index++ {
		leafID := "route_a"
		if index >= 51 {
			leafID = "route_b"
		}
		value.locators[leafID] = append(value.locators[leafID], router.Locator{
			DatabaseID: "db_work", TableID: "tbl_notes",
			RowID: "row_" + string(rune(0x1000+index)), Revision: 1,
		})
	}
	return value
}

func moveFixture() *fakeSource {
	return &fakeSource{nodes: []router.Node{
		{Version: router.Version, ID: "route_root", DatabaseID: "db_work", TableID: "tbl_notes", Kind: router.KindRoot, Name: "root", Path: "/", Purpose: "Root", Revision: 1},
		{Version: router.Version, ID: "route_branch", DatabaseID: "db_work", TableID: "tbl_notes", ParentID: "route_root", Kind: router.KindBranch, Name: "branch", Path: "/branch", Purpose: "Branch", Revision: 2},
		{Version: router.Version, ID: "route_leaf", DatabaseID: "db_work", TableID: "tbl_notes", ParentID: "route_branch", Kind: router.KindBranch, Name: "leaf", Path: "/branch/leaf", Purpose: "Leaf", Revision: 1},
	}, locators: map[string][]router.Locator{"route_leaf": {}}}
}

type fakeSource struct {
	nodes        []router.Node
	locators     map[string][]router.Locator
	repeatCursor bool
}

func (source *fakeSource) ListRouterNodes(context.Context) ([]router.Node, error) {
	return append([]router.Node(nil), source.nodes...), nil
}

func (source *fakeSource) ListRouterLeafPage(_ context.Context, leafID, cursor string, limit int) ([]router.Locator, router.ReadPage, error) {
	values := append([]router.Locator(nil), source.locators[leafID]...)
	if source.repeatCursor {
		return values, router.ReadPage{NextCursor: "repeat"}, nil
	}
	return values, router.ReadPage{}, nil
}
