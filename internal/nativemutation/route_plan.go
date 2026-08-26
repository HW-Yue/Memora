package nativemutation

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/HW-Yue/Memora/internal/change"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/routemutationplan"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/security"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

func (service *Service) ApplyRouteMutationPlan(
	ctx context.Context, databaseName, tableName string, plan routemutationplan.Plan,
) (routemutationplan.Receipt, error) {
	if service == nil || service.Service == nil || service.catalog == nil || service.routes == nil || service.commit == nil {
		return routemutationplan.Receipt{}, routePlanError(result.CodeInternal, "native Route plan service is incomplete")
	}
	if err := routemutationplan.Validate(plan); err != nil {
		return routemutationplan.Receipt{}, err
	}
	if err := security.RequireApproval(ctx, security.ActionApplyRouteMutation, strings.TrimPrefix(plan.Hash, "sha256:")); err != nil {
		return routemutationplan.Receipt{}, err
	}
	table, err := service.catalog.DescribeTable(ctx, databaseName, tableName)
	if err != nil {
		return routemutationplan.Receipt{}, err
	}
	if err := security.RequireAnyDatabaseLevel(ctx, security.LevelStructural, databaseName, table.DatabaseID); err != nil {
		return routemutationplan.Receipt{}, err
	}
	if plan.Scope.DatabaseID != table.DatabaseID || plan.Scope.TableID != table.ID ||
		(plan.Scope.Database != "" && !strings.EqualFold(plan.Scope.Database, databaseName)) ||
		(plan.Scope.Table != "" && !strings.EqualFold(plan.Scope.Table, tableName)) {
		return routemutationplan.Receipt{}, routePlanError(result.CodePermissionDenied, "plan is outside the bound Table")
	}
	release, err := service.Service.BeginAuthorityWrite(ctx)
	if err != nil {
		return routemutationplan.Receipt{}, err
	}
	defer release()
	table, err = service.catalog.DescribeTable(ctx, databaseName, tableName)
	if err != nil || table.DatabaseID != plan.Scope.DatabaseID || table.ID != plan.Scope.TableID {
		return routemutationplan.Receipt{}, routePlanError(result.CodeRevisionConflict, "bound Table changed before Route plan execution")
	}
	current, err := service.revalidateRoutePlan(plan)
	if err != nil {
		return routemutationplan.Receipt{}, err
	}
	routes, created, memberships, updated, deleted, err := service.materializeRoutePlan(plan, current)
	if err != nil {
		return routemutationplan.Receipt{}, err
	}
	sequence, err := service.commit.CommitRoutePlan(RoutePlanCommit{
		Routes: routes, Created: created, Memberships: memberships, CommittedAt: time.Now().UTC(),
		Metadata: change.Metadata{Actor: plan.Actor, Source: plan.SourceEventID, Reason: plan.Reason},
	})
	if err != nil {
		return routemutationplan.Receipt{}, err
	}
	return routemutationplan.Receipt{
		Version: routemutationplan.ReceiptVersion, PlanID: plan.PlanID, PlanHash: plan.Hash,
		Operation: plan.Operation, Status: "committed", ChangeSequence: sequence,
		CreatedNodes: len(created), UpdatedNodes: updated, DeletedNodes: deleted,
		MembershipRevisions: len(memberships), Verified: true,
	}, nil
}

func (service *Service) revalidateRoutePlan(plan routemutationplan.Plan) (map[string]router.Node, error) {
	nodes, err := service.routes.Nodes()
	if err != nil {
		return nil, err
	}
	current := make(map[string]router.Node, len(nodes))
	for _, node := range nodes {
		current[node.ID] = node
	}
	for _, guard := range plan.NodeGuards {
		node, ok := current[guard.RouteID]
		if !ok || node.Revision != guard.Revision || node.DatabaseID != plan.Scope.DatabaseID || node.TableID != plan.Scope.TableID {
			return nil, routePlanError(result.CodeRevisionConflict, "Route %q no longer matches its guard", guard.RouteID)
		}
	}
	for _, guard := range plan.ChildSetGuards {
		children := service.routes.Children(guard.ParentRouteID)
		ids := make([]string, 0, len(children))
		for _, child := range children {
			ids = append(ids, child.ID)
		}
		sort.Strings(ids)
		if !sameStrings(ids, guard.ChildRouteIDs) {
			return nil, routePlanError(result.CodeRevisionConflict, "Route %q child set changed after planning", guard.ParentRouteID)
		}
	}
	for _, guard := range plan.LocatorSetGuards {
		locators, err := service.routeLocators(guard.LeafRouteID)
		if err != nil {
			return nil, err
		}
		if len(locators) != len(guard.Locators) {
			return nil, routePlanError(result.CodeRevisionConflict, "Route %q locator set changed after planning", guard.LeafRouteID)
		}
		for index := range locators {
			if locators[index].RowID != guard.Locators[index].RowID || locators[index].Revision != guard.Locators[index].Revision {
				return nil, routePlanError(result.CodeRevisionConflict, "Route %q locator set changed after planning", guard.LeafRouteID)
			}
		}
	}
	return current, nil
}

// routeLocators resolves what a leaf currently locates.
//
// It reads the leaf's own RowID field and then that Row, which is the same
// source the planner captured its guard from. Reading one source here and
// another there is how a guard starts reporting conflicts that are only a
// disagreement between two views of the same fact.
func (service *Service) routeLocators(leafID string) ([]router.Locator, error) {
	leaf, err := service.routes.Get(leafID)
	if err != nil {
		return nil, err
	}
	if leaf.RowID == "" {
		return []router.Locator{}, nil
	}
	value, err := service.rows.Read(leaf.RowID)
	if errors.Is(err, nativestore.ErrNotFound) {
		return []router.Locator{}, nil
	}
	if err != nil {
		return nil, err
	}
	return []router.Locator{{
		DatabaseID: value.DatabaseID, TableID: value.TableID,
		RowID: value.ID, Revision: value.Revision,
	}}, nil
}

func (service *Service) materializeRoutePlan(
	plan routemutationplan.Plan, current map[string]router.Node,
) ([]router.Node, map[string]bool, []router.Membership, int, int, error) {
	desired := make(map[string]router.Node, len(current)+len(plan.Creates))
	for id, node := range current {
		desired[id] = node
	}
	created := map[string]bool{}
	for _, value := range plan.Creates {
		if _, exists := desired[value.RouteID]; exists {
			return nil, nil, nil, 0, 0, routePlanError(result.CodeRevisionConflict, "planned Route %q already exists", value.RouteID)
		}
		if _, err := service.routes.Get(value.RouteID); err == nil || !errors.Is(err, nativestore.ErrNotFound) {
			return nil, nil, nil, 0, 0, routePlanError(result.CodeRevisionConflict, "planned Route %q identity is unavailable", value.RouteID)
		}
		desired[value.RouteID] = router.Node{
			Version: router.Version, ID: value.RouteID, DatabaseID: plan.Scope.DatabaseID, TableID: plan.Scope.TableID,
			ParentID: value.ParentID, Name: value.Name, Aliases: []string{}, Kind: value.Kind,
			Purpose: value.Purpose, Synopsis: value.Synopsis, Revision: 1,
		}
		created[value.RouteID] = true
	}
	guarded := map[string]bool{}
	for _, guard := range plan.NodeGuards {
		guarded[guard.RouteID] = true
	}
	for _, value := range plan.Moves {
		node, ok := desired[value.RouteID]
		if !ok || node.ParentID != value.FromParentID {
			return nil, nil, nil, 0, 0, routePlanError(result.CodeRevisionConflict, "Route %q parent changed after planning", value.RouteID)
		}
		if parent, ok := desired[value.ToParentID]; !ok || parent.Deleted || parent.Kind == router.KindLeaf {
			return nil, nil, nil, 0, 0, routePlanError(result.CodeConstraint, "Route %q target parent is invalid", value.RouteID)
		}
		node.ParentID = value.ToParentID
		desired[value.RouteID] = node
	}
	for _, value := range plan.Deletes {
		node, ok := desired[value.RouteID]
		if !ok || node.Revision != value.Revision {
			return nil, nil, nil, 0, 0, routePlanError(result.CodeRevisionConflict, "Route %q delete guard changed", value.RouteID)
		}
		node.Deleted = true
		desired[value.RouteID] = node
	}
	if err := validateDesiredTree(plan.Scope, desired); err != nil {
		return nil, nil, nil, 0, 0, err
	}
	if err := assignDesiredPaths(plan.Scope, desired); err != nil {
		return nil, nil, nil, 0, 0, err
	}
	memberships, err := service.materializeMembershipMoves(plan, desired)
	if err != nil {
		return nil, nil, nil, 0, 0, err
	}
	changes, updated, deleted := make([]router.Node, 0), 0, 0
	for id, node := range desired {
		if node.DatabaseID != plan.Scope.DatabaseID || node.TableID != plan.Scope.TableID {
			continue
		}
		before, exists := current[id]
		if created[id] {
			changes = append(changes, node)
			continue
		}
		if !exists || (node.ParentID == before.ParentID && node.Path == before.Path &&
			node.Deleted == before.Deleted && node.RowID == before.RowID) {
			continue
		}
		if !guarded[id] {
			return nil, nil, nil, 0, 0, routePlanError(result.CodeValidation, "Route %q changed without a node guard", id)
		}
		node.Revision = before.Revision + 1
		changes = append(changes, node)
		if node.Deleted {
			deleted++
		} else {
			updated++
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].ID < changes[j].ID })
	sort.Slice(memberships, func(i, j int) bool {
		if memberships[i].LeafID == memberships[j].LeafID {
			return memberships[i].RowID < memberships[j].RowID
		}
		return memberships[i].LeafID < memberships[j].LeafID
	})
	if len(created) != plan.Impact.CreatedNodes || deleted != plan.Impact.DeletedNodes {
		return nil, nil, nil, 0, 0, routePlanError(result.CodeInternal, "Route plan action coverage is incomplete")
	}
	return changes, created, memberships, updated, deleted, nil
}

func (service *Service) materializeMembershipMoves(
	plan routemutationplan.Plan, desired map[string]router.Node,
) ([]router.Membership, error) {
	changes := make([]router.Membership, 0, plan.Impact.MovedMemberships+len(plan.MembershipMoves))
	for _, move := range plan.MembershipMoves {
		all, err := service.routes.MembershipsIncludingDeleted(move.RowID)
		if err != nil {
			return nil, err
		}
		byLeaf := map[string]router.Membership{}
		for _, value := range all {
			byLeaf[value.LeafID] = value
		}
		for _, sourceID := range move.FromLeafIDs {
			membership, ok := byLeaf[sourceID]
			if !ok || membership.Deleted || membership.Revision != move.Revision {
				return nil, routePlanError(result.CodeRevisionConflict, "Row %q membership changed after planning", move.RowID)
			}
			membership.MembershipRevision++
			membership.Deleted = true
			changes = append(changes, membership)
			// The leaf records the Row it holds, so emptying a source leaf is a
			// change to the leaf itself, not only to a Membership beside it.
			if source, ok := desired[sourceID]; ok && source.RowID == move.RowID {
				source.RowID = ""
				desired[sourceID] = source
			}
		}
		target, ok := desired[move.ToLeafID]
		if !ok || target.Deleted || target.Kind != router.KindLeaf || target.DatabaseID != plan.Scope.DatabaseID || target.TableID != plan.Scope.TableID {
			return nil, routePlanError(result.CodeConstraint, "Row %q target leaf is invalid", move.RowID)
		}
		membership, exists := byLeaf[move.ToLeafID]
		if exists && !membership.Deleted {
			return nil, routePlanError(result.CodeConstraint, "Row %q is already in target leaf", move.RowID)
		}
		membership = router.Membership{LeafID: move.ToLeafID, MembershipRevision: 1,
			Locator: router.Locator{DatabaseID: plan.Scope.DatabaseID, TableID: plan.Scope.TableID, RowID: move.RowID, Revision: move.Revision}}
		if exists {
			membership.MembershipRevision = byLeaf[move.ToLeafID].MembershipRevision + 1
		}
		changes = append(changes, membership)
		// And the target leaf records that it now holds the Row. Both sides are
		// written by the same commit as the rest of the plan.
		target.RowID = move.RowID
		desired[move.ToLeafID] = target
	}
	return changes, nil
}

func validateDesiredTree(scope routemutationplan.Scope, nodes map[string]router.Node) error {
	siblings := map[string]map[string]bool{}
	for id, node := range nodes {
		if node.DatabaseID != scope.DatabaseID || node.TableID != scope.TableID || node.Deleted {
			continue
		}
		if node.Kind == router.KindRoot {
			if node.ParentID != "" {
				return routePlanError(result.CodeConstraint, "Router root has a parent")
			}
			continue
		}
		parent, ok := nodes[node.ParentID]
		if !ok || parent.Deleted || parent.Kind == router.KindLeaf || parent.DatabaseID != scope.DatabaseID || parent.TableID != scope.TableID {
			return routePlanError(result.CodeConstraint, "Route %q has an invalid parent", id)
		}
		name := strings.ToLower(strings.TrimSpace(node.Name))
		if siblings[node.ParentID] == nil {
			siblings[node.ParentID] = map[string]bool{}
		}
		if siblings[node.ParentID][name] {
			return routePlanError(result.CodeConstraint, "Route %q has a duplicate sibling name", id)
		}
		siblings[node.ParentID][name] = true
	}
	for _, node := range nodes {
		if node.Deleted {
			for _, child := range nodes {
				if !child.Deleted && child.ParentID == node.ID {
					return routePlanError(result.CodeConstraint, "deleted Route %q still has a child", node.ID)
				}
			}
		}
	}
	return nil
}

func assignDesiredPaths(scope routemutationplan.Scope, nodes map[string]router.Node) error {
	visiting, done := map[string]bool{}, map[string]bool{}
	var visit func(string) (string, error)
	visit = func(id string) (string, error) {
		if done[id] {
			return nodes[id].Path, nil
		}
		if visiting[id] {
			return "", routePlanError(result.CodeConstraint, "Router tree contains a cycle")
		}
		visiting[id] = true
		node := nodes[id]
		if node.Kind == router.KindRoot {
			node.Path = "/"
		} else {
			parentPath, err := visit(node.ParentID)
			if err != nil {
				return "", err
			}
			node.Path = strings.TrimSuffix(parentPath, "/") + "/" + node.Name
		}
		nodes[id], visiting[id], done[id] = node, false, true
		return node.Path, nil
	}
	for id, node := range nodes {
		if node.DatabaseID == scope.DatabaseID && node.TableID == scope.TableID && !node.Deleted {
			if _, err := visit(id); err != nil {
				return err
			}
		}
	}
	return nil
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func routePlanError(code result.Code, format string, args ...any) error {
	return &ServiceError{Code: code, Message: fmt.Sprintf("Route mutation execution: "+format, args...)}
}
