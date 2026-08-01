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

func TestApprovedRoutePlanCommitsRoutesMembershipsAndChangeAtomically(t *testing.T) {
	path, file, rows, routes, _, _, _ := mutationFixture(t)
	dictionary := nativecatalog.NewService(nativecatalog.New(file), nativecatalog.ServiceOptions{})
	base := nativerow.NewService(rows, dictionary, nativerow.ServiceOptions{})
	service := NewService(base, dictionary, rows, routes, New(file, rows, routes))
	if err := routes.Attach("route_leaf", router.Locator{
		DatabaseID: "db_work", TableID: "tbl_notes", RowID: "row_target", Revision: 1,
	}, 1); err != nil {
		t.Fatal(err)
	}
	proposal := routemutationplan.Proposal{
		Version: routemutationplan.ProposalVersion, ID: "proposal_split", Operation: routemutationplan.OperationSplit,
		Actor: "agent:test", SourceEventID: "event:route-review", Reason: "split reviewed leaf",
		Sources: []routemutationplan.SourceRef{{RouteID: "route_leaf", ExpectedRevision: 1}},
		Targets: []routemutationplan.TargetProposal{
			{Key: "current", Name: "current", Purpose: "Current", RowIDs: []string{"row_source"}},
			{Key: "archive", Name: "archive", Purpose: "Archive", RowIDs: []string{"row_target"}},
		},
	}
	plan, err := routemutationplan.Build(context.Background(), service,
		routemutationplan.Scope{DatabaseID: "db_work", Database: "work", TableID: "tbl_notes", Table: "notes"}, proposal)
	if err != nil {
		t.Fatal(err)
	}
	ctx := approvedRoutePlanContext(plan)
	receipt, err := service.ApplyRouteMutationPlan(ctx, "work", "notes", plan)
	if err != nil || !receipt.Verified || receipt.Status != "committed" || receipt.ChangeSequence == 0 ||
		receipt.CreatedNodes != 2 || receipt.DeletedNodes != 1 || receipt.MembershipRevisions != 4 {
		t.Fatalf("ApplyRouteMutationPlan() = %#v, %v", receipt, err)
	}
	if _, err := routes.Get("route_leaf"); err != nil {
		t.Fatal(err)
	}
	for _, create := range plan.Creates {
		locators, _, err := routes.Open(create.RouteID, 10)
		if err != nil || len(locators) != 1 {
			t.Fatalf("target %s locators = %#v, %v", create.RouteID, locators, err)
		}
	}
	changes, more, err := nativechange.New(file).ListAfter(0, 10)
	if err != nil || more || len(changes) != 1 ||
		countEntries(changes[0], change.ObjectRouteNode) != 3 ||
		countEntries(changes[0], change.ObjectRouteMembership) != 4 {
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
	reopenedRoutes := nativerouter.New(reopened)
	for _, create := range plan.Creates {
		locators, _, err := reopenedRoutes.Open(create.RouteID, 10)
		if err != nil || len(locators) != 1 {
			t.Fatalf("reopened target %s locators = %#v, %v", create.RouteID, locators, err)
		}
	}
}

func TestRoutePlanRequiresExactApprovalAndFreshSnapshotWithoutPartialWrites(t *testing.T) {
	_, file, rows, routes, _, _, _ := mutationFixture(t)
	t.Cleanup(func() { _ = file.Close() })
	dictionary := nativecatalog.NewService(nativecatalog.New(file), nativecatalog.ServiceOptions{})
	base := nativerow.NewService(rows, dictionary, nativerow.ServiceOptions{})
	service := NewService(base, dictionary, rows, routes, New(file, rows, routes))
	plan, err := routemutationplan.Build(context.Background(), service,
		routemutationplan.Scope{DatabaseID: "db_work", Database: "work", TableID: "tbl_notes", Table: "notes"},
		routemutationplan.Proposal{
			Version: routemutationplan.ProposalVersion, ID: "proposal_move", Operation: routemutationplan.OperationMove,
			Actor: "agent:test", SourceEventID: "event:move", Reason: "move",
			Sources: []routemutationplan.SourceRef{{RouteID: "route_leaf", ExpectedRevision: 1}}, TargetParentID: "route_root",
		})
	if err == nil {
		t.Fatal("fixture MOVE should reject same parent")
	}
	// Build a valid leaf split after adding a second membership.
	if err := routes.Attach("route_leaf", router.Locator{DatabaseID: "db_work", TableID: "tbl_notes", RowID: "row_target", Revision: 1}, 1); err != nil {
		t.Fatal(err)
	}
	plan, err = routemutationplan.Build(context.Background(), service,
		routemutationplan.Scope{DatabaseID: "db_work", Database: "work", TableID: "tbl_notes", Table: "notes"},
		routemutationplan.Proposal{
			Version: routemutationplan.ProposalVersion, ID: "proposal_split", Operation: routemutationplan.OperationSplit,
			Actor: "agent:test", SourceEventID: "event:split", Reason: "split",
			Sources: []routemutationplan.SourceRef{{RouteID: "route_leaf", ExpectedRevision: 1}},
			Targets: []routemutationplan.TargetProposal{
				{Key: "one", Name: "one", Purpose: "One", RowIDs: []string{"row_source"}},
				{Key: "two", Name: "two", Purpose: "Two", RowIDs: []string{"row_target"}},
			},
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
