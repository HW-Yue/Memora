package nativemutation

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/change"
	"github.com/HW-Yue/Memora/internal/nativecatalog"
	"github.com/HW-Yue/Memora/internal/nativechange"
	"github.com/HW-Yue/Memora/internal/nativerouter"
	"github.com/HW-Yue/Memora/internal/nativerow"
	"github.com/HW-Yue/Memora/internal/routemutationplan"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/security"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

func TestApprovedRoutePlanCommitsRoutesMountsAndChangeAtomically(t *testing.T) {
	path, file, rows, routes, _, _, _ := mutationFixture(t)
	dictionary := nativecatalog.NewService(nativecatalog.New(file), nativecatalog.ServiceOptions{})
	base := nativerow.NewService(rows, dictionary, nativerow.ServiceOptions{})
	service := NewService(base, dictionary, rows, routes, New(file, rows, routes))
	alias, err := routes.CreateChild("route_alias", "route_root", "alias", router.KindLeaf, "Alias for the same Row")
	if err != nil {
		t.Fatal(err)
	}
	mountRowInLeaf(t, file, rows, routes, alias.ID, "row_source")
	proposal := routemutationplan.Proposal{
		Version: routemutationplan.ProposalVersion, ID: "proposal_merge", Operation: routemutationplan.OperationMerge,
		Actor: "agent:test", SourceEventID: "event:route-review", Reason: "merge equivalent Row aliases",
		// Read the revisions rather than assuming 1: mounting a Row advances the
		// leaf it lands on, because the leaf records which Row it holds.
		Sources: []routemutationplan.SourceRef{
			{RouteID: "route_leaf", ExpectedRevision: currentRouteRevision(t, routes, "route_leaf")},
			{RouteID: alias.ID, ExpectedRevision: currentRouteRevision(t, routes, alias.ID)},
		},
		Targets: []routemutationplan.TargetProposal{{Key: "canonical", Name: "canonical", Purpose: "Canonical Row locator"}},
	}
	plan, err := routemutationplan.Build(context.Background(), service,
		routemutationplan.Scope{DatabaseID: "db_work", Database: "work", TableID: "tbl_notes", Table: "notes"}, proposal)
	if err != nil {
		t.Fatal(err)
	}
	ctx := approvedRoutePlanContext(plan)
	receipt, err := service.ApplyRouteMutationPlan(ctx, "work", "notes", plan)
	if err != nil || !receipt.Verified || receipt.Status != "committed" || receipt.ChangeSequence == 0 ||
		receipt.CreatedNodes != 1 || receipt.DeletedNodes != 2 {
		t.Fatalf("ApplyRouteMutationPlan() = %#v, %v", receipt, err)
	}
	if _, err := routes.Get("route_leaf"); err != nil {
		t.Fatal(err)
	}
	for _, create := range plan.Creates {
		locators := openLeaf(t, routes, rows, create.RouteID)
		if len(locators) != 1 {
			t.Fatalf("target %s locators = %#v", create.RouteID, locators)
		}
	}
	// The Row must come out of the plan naming the leaf it now hangs under.
	// The leaf and the Row are two ends of one mount; a plan that moves the Row
	// and leaves the Row's own list pointing at leaves it left has torn them
	// apart, and every route_paths read after it is wrong.
	moved, err := rows.Read("row_source")
	if err != nil {
		t.Fatal(err)
	}
	held, err := routes.LeavesHoldingRow("row_source")
	if err != nil {
		t.Fatal(err)
	}
	if len(held) != 1 || len(moved.RouteLeafIDs) != 1 || moved.RouteLeafIDs[0] != held[0] {
		t.Fatalf("Row leaf list = %#v, leaves holding it = %#v", moved.RouteLeafIDs, held)
	}
	changes, more, err := nativechange.New(file).ListAfter(0, 10)
	if err != nil || more || len(changes) != 1 ||
		countEntries(changes[0], change.ObjectRouteNode) != 3 {
		t.Fatalf("Route plan Change Log = %#v, %v, %v", changes, more, err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := nativestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reopenedRoutes, reopenedRows := nativerouter.New(reopened), nativerow.New(reopened)
	for _, create := range plan.Creates {
		locators := openLeaf(t, reopenedRoutes, reopenedRows, create.RouteID)
		if len(locators) != 1 {
			t.Fatalf("reopened target %s locators = %#v", create.RouteID, locators)
		}
	}
}

func TestRoutePlanRequiresExactApprovalAndFreshSnapshotWithoutPartialWrites(t *testing.T) {
	_, file, rows, routes, _, _, _ := mutationFixture(t)
	t.Cleanup(func() { _ = file.Close() })
	dictionary := nativecatalog.NewService(nativecatalog.New(file), nativecatalog.ServiceOptions{})
	base := nativerow.NewService(rows, dictionary, nativerow.ServiceOptions{})
	service := NewService(base, dictionary, rows, routes, New(file, rows, routes))
	_, err := routemutationplan.Build(context.Background(), service,
		routemutationplan.Scope{DatabaseID: "db_work", Database: "work", TableID: "tbl_notes", Table: "notes"},
		routemutationplan.Proposal{
			Version: routemutationplan.ProposalVersion, ID: "proposal_move", Operation: routemutationplan.OperationMove,
			Actor: "agent:test", SourceEventID: "event:move", Reason: "move",
			Sources: []routemutationplan.SourceRef{{
				RouteID: "route_leaf", ExpectedRevision: currentRouteRevision(t, routes, "route_leaf"),
			}}, TargetParentID: "route_root",
		})
	if err == nil {
		t.Fatal("fixture MOVE should reject same parent")
	}
	target, err := routes.CreateChild("route_target", "route_root", "target", router.KindBranch, "Move target")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := routemutationplan.Build(context.Background(), service,
		routemutationplan.Scope{DatabaseID: "db_work", Database: "work", TableID: "tbl_notes", Table: "notes"},
		routemutationplan.Proposal{
			Version: routemutationplan.ProposalVersion, ID: "proposal_move_valid", Operation: routemutationplan.OperationMove,
			Actor: "agent:test", SourceEventID: "event:move-valid", Reason: "move",
			Sources: []routemutationplan.SourceRef{{
				RouteID: "route_leaf", ExpectedRevision: currentRouteRevision(t, routes, "route_leaf"),
			}},
			TargetParentID: target.ID,
		})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApplyRouteMutationPlan(context.Background(), "work", "notes", plan); err == nil {
		t.Fatal("ApplyRouteMutationPlan without approval unexpectedly succeeded")
	}
	current, err := routes.Get("route_leaf")
	if err != nil {
		t.Fatal(err)
	}
	current.Revision++
	current.Synopsis = "changed after planning"
	tx, err := file.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := routes.StageNode(tx, current); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApplyRouteMutationPlan(approvedRoutePlanContext(plan), "work", "notes", plan); err == nil {
		t.Fatal("ApplyRouteMutationPlan(stale) unexpectedly succeeded")
	}
	for _, create := range plan.Creates {
		if _, err := routes.Get(create.RouteID); !errors.Is(err, nativestore.ErrNotFound) {
			t.Fatalf("partial target %s error = %v", create.RouteID, err)
		}
	}
	changes, _, err := nativechange.New(file).ListAfter(0, 10)
	if err != nil || len(changes) != 0 {
		t.Fatalf("failed apply published changes = %#v, %v", changes, err)
	}
}

func approvedRoutePlanContext(plan routemutationplan.Plan) context.Context {
	return security.WithAuthorization(context.Background(), security.Authorization{
		Version: security.AuthorizationVersion, Actor: "user:test", AuthorizedDatabases: []string{"work"}, DefaultLevel: security.LevelStructural,
		Approval: &security.Approval{Version: security.ApprovalVersion, Action: security.ActionApplyRouteMutation,
			SubjectSHA256: strings.TrimPrefix(plan.Hash, "sha256:"), Confirmed: true},
	})
}

// currentRouteRevision reads a Route's revision now, instead of assuming the
// one it had when it was created.
func currentRouteRevision(t *testing.T, routes *nativerouter.Repository, routeID string) uint64 {
	t.Helper()
	node, err := routes.Get(routeID)
	if err != nil {
		t.Fatal(err)
	}
	return node.Revision
}
