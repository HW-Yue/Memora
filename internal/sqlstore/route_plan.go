package sqlstore

import (
	"context"
	"sort"
	"strings"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/change"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/routemutationplan"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/security"
)

// CurrentBranchFanout reports the Database's structural fan-out limit.
func (db *DB) CurrentBranchFanout(ctx context.Context) (int, error) {
	policy, err := db.CurrentRoutePolicy(ctx)
	if err != nil {
		return 0, err
	}
	return policy.Policy.BranchFanout, nil
}

// ApplyRouteMutationPlan executes a validated SPLIT / MERGE / MOVE plan in one
// transaction. Retired nodes are deprecated, not removed, and carry the new
// nodes as successors so references held outside the tree can follow them.
func (db *DB) ApplyRouteMutationPlan(ctx context.Context, databaseName, tableName string, plan routemutationplan.Plan) (routemutationplan.Receipt, error) {
	if err := routemutationplan.Validate(plan); err != nil {
		return routemutationplan.Receipt{}, err
	}
	if err := security.RequireApproval(ctx, security.ActionApplyRouteMutation, strings.TrimPrefix(plan.Hash, "sha256:")); err != nil {
		return routemutationplan.Receipt{}, err
	}
	receipt := routemutationplan.Receipt{
		Version: routemutationplan.ReceiptVersion, PlanID: plan.PlanID, PlanHash: plan.Hash,
		Operation: plan.Operation, Status: "committed", Verified: true,
	}
	err := db.update(ctx, func(t *tx) error {
		table, err := t.liveTable(ctx, databaseName, tableName)
		if err != nil {
			return err
		}
		if err := security.RequireAnyDatabaseLevel(ctx, security.LevelStructural, databaseName, table.DatabaseID); err != nil {
			return err
		}
		if plan.Scope.DatabaseID != table.DatabaseID || plan.Scope.TableID != table.ID {
			return fail(result.CodePermissionDenied, "plan is outside the bound Table")
		}
		if err := t.checkPlanGuards(ctx, table, plan); err != nil {
			return err
		}
		metadata := change.Metadata{Actor: plan.Actor, Source: fallback(plan.SourceEventID, "route-plan"), Reason: plan.Reason}
		t.claimAttribution(metadata)

		createdIDs := []string{}
		for _, value := range plan.Creates {
			parent, err := t.readRoute(ctx, table.ID, value.ParentID)
			if err != nil {
				return fail(result.CodeRevisionConflict, "planned parent %q is missing", value.ParentID)
			}
			if _, err := t.routeTableOf(ctx, value.RouteID); err == nil {
				return fail(result.CodeRevisionConflict, "planned Route %q already exists", value.RouteID)
			}
			node := routeNode{Node: router.Node{
				Version: router.Version, ID: value.RouteID, DatabaseID: table.DatabaseID, TableID: table.ID,
				ParentID: parent.ID, Name: value.Name, Aliases: []string{}, Kind: value.Kind,
				Purpose: value.Purpose, Synopsis: value.Synopsis,
			}}
			if err := t.saveRoute(ctx, table, node, change.OperationInsert); err != nil {
				return err
			}
			parent.ChildIDs = append(parent.ChildIDs, node.ID)
			if err := t.saveRoute(ctx, table, parent, change.OperationUpdate); err != nil {
				return err
			}
			createdIDs = append(createdIDs, node.ID)
		}
		receipt.CreatedNodes = len(createdIDs)

		for _, move := range plan.Moves {
			node, err := t.readRoute(ctx, table.ID, move.RouteID)
			if err != nil || node.Deprecated || node.ParentID != move.FromParentID {
				return fail(result.CodeRevisionConflict, "Route %q parent changed after planning", move.RouteID)
			}
			target, err := t.readRoute(ctx, table.ID, move.ToParentID)
			if err != nil || target.Deprecated || target.Kind == router.KindLeaf {
				return fail(result.CodeConstraint, "Route %q target parent is invalid", move.RouteID)
			}
			from, err := t.readRoute(ctx, table.ID, move.FromParentID)
			if err != nil {
				return err
			}
			from.ChildIDs = without(from.ChildIDs, node.ID)
			if err := t.saveRoute(ctx, table, from, change.OperationUpdate); err != nil {
				return err
			}
			target, _ = t.readRoute(ctx, table.ID, move.ToParentID)
			target.ChildIDs = append(target.ChildIDs, node.ID)
			if err := t.saveRoute(ctx, table, target, change.OperationUpdate); err != nil {
				return err
			}
			node.ParentID = target.ID
			if err := t.saveRoute(ctx, table, node, change.OperationUpdate); err != nil {
				return err
			}
			receipt.UpdatedNodes++
		}

		for _, move := range plan.MembershipMoves {
			if err := t.moveMembership(ctx, table, move); err != nil {
				return err
			}
			receipt.MembershipRevisions++
		}

		for _, retire := range plan.Deletes {
			node, err := t.readRoute(ctx, table.ID, retire.RouteID)
			if err != nil || node.Deprecated {
				return fail(result.CodeRevisionConflict, "Route %q delete guard changed", retire.RouteID)
			}
			children, err := t.liveChildren(ctx, node)
			if err != nil {
				return err
			}
			if len(children) > 0 {
				return fail(result.CodeConstraint, "Route %q still has live children after the plan", node.ID)
			}
			if node.ParentID != "" {
				parent, err := t.readRoute(ctx, table.ID, node.ParentID)
				if err != nil {
					return err
				}
				parent.ChildIDs = without(parent.ChildIDs, node.ID)
				if err := t.saveRoute(ctx, table, parent, change.OperationUpdate); err != nil {
					return err
				}
			}
			node, _ = t.readRoute(ctx, table.ID, retire.RouteID)
			node.Deprecated = true
			node.SuccessorIDs = append([]string{}, createdIDs...)
			if err := t.saveRoute(ctx, table, node, change.OperationDelete); err != nil {
				return err
			}
			receipt.DeletedNodes++
		}
		// Moving or retiring the last child of a branch leaves a shell behind;
		// the engine clears it here rather than handing the Agent a second way
		// to remove nodes.
		if err := t.pruneEmptyBranches(ctx, table); err != nil {
			return err
		}
		if err := t.checkTreeShape(ctx, table); err != nil {
			return err
		}
		sequence, err := t.commitSequence(ctx)
		receipt.ChangeSequence = sequence
		return err
	})
	if err != nil {
		return routemutationplan.Receipt{}, err
	}
	return receipt, nil
}

func (t *tx) checkPlanGuards(ctx context.Context, table catalog.Table, plan routemutationplan.Plan) error {
	for _, guard := range plan.NodeGuards {
		node, err := t.readRoute(ctx, table.ID, guard.RouteID)
		if err != nil || node.Deprecated || node.Revision != guard.Revision {
			return fail(result.CodeRevisionConflict, "Route %q no longer matches its guard", guard.RouteID)
		}
	}
	for _, guard := range plan.ChildSetGuards {
		parent, err := t.readRoute(ctx, table.ID, guard.ParentRouteID)
		if err != nil {
			return fail(result.CodeRevisionConflict, "Route %q is missing", guard.ParentRouteID)
		}
		children, err := t.liveChildren(ctx, parent)
		if err != nil {
			return err
		}
		ids := make([]string, 0, len(children))
		for _, child := range children {
			ids = append(ids, child.ID)
		}
		want := append([]string{}, guard.ChildRouteIDs...)
		sort.Strings(ids)
		sort.Strings(want)
		if strings.Join(ids, ",") != strings.Join(want, ",") {
			return fail(result.CodeRevisionConflict, "Route %q child set changed after planning", guard.ParentRouteID)
		}
	}
	for _, guard := range plan.LocatorSetGuards {
		locators, _, err := t.leafPage(ctx, guard.LeafRouteID, "", 1000)
		if err != nil {
			return err
		}
		if len(locators) != len(guard.Locators) {
			return fail(result.CodeRevisionConflict, "Route %q locator set changed after planning", guard.LeafRouteID)
		}
		for index := range locators {
			if locators[index].RowID != guard.Locators[index].RowID || locators[index].Revision != guard.Locators[index].Revision {
				return fail(result.CodeRevisionConflict, "Route %q locator set changed after planning", guard.LeafRouteID)
			}
		}
	}
	return nil
}

func (t *tx) moveMembership(ctx context.Context, table catalog.Table, move routemutationplan.MembershipMove) error {
	value, err := t.readRow(ctx, table, move.RowID)
	if err != nil || value.State != row.StateLive {
		return fail(result.CodeRevisionConflict, "Row %q is no longer live", move.RowID)
	}
	for _, sourceID := range move.FromLeafIDs {
		source, err := t.readRoute(ctx, table.ID, sourceID)
		if err != nil || source.RowID != move.RowID {
			return fail(result.CodeRevisionConflict, "Row %q is no longer in Route leaf %q", move.RowID, sourceID)
		}
		source.RowID = ""
		if err := t.saveRoute(ctx, table, source, change.OperationUpdate); err != nil {
			return err
		}
		value.RouteLeafIDs = without(value.RouteLeafIDs, sourceID)
	}
	target, err := t.readRoute(ctx, table.ID, move.ToLeafID)
	if err != nil || target.Deprecated || target.Kind != router.KindLeaf {
		return fail(result.CodeConstraint, "Row %q target leaf is invalid", move.RowID)
	}
	if target.RowID != "" && target.RowID != move.RowID {
		return fail(result.CodeConstraint, "Route leaf %q already locates Row %q", move.ToLeafID, target.RowID)
	}
	target.RowID = move.RowID
	if err := t.saveRoute(ctx, table, target, change.OperationUpdate); err != nil {
		return err
	}
	value.RouteLeafIDs = mergeLeaves(value.RouteLeafIDs, []string{target.ID})
	// A plan that would leave the Row under no leaf, or under more than one, is
	// refused like any other write: the Row is live and the mount is one-to-one.
	if err := requireSingleLeaf(value); err != nil {
		return err
	}
	if _, err := t.q().ExecContext(ctx, `UPDATE `+dataTable(table.ID)+` SET route_leaf_ids = ? WHERE row_id = ?`,
		encodeJSON(value.RouteLeafIDs), value.ID); err != nil {
		return err
	}
	return t.syncRecallUnit(ctx, table, value)
}

// checkTreeShape enforces the invariants a plan must leave behind: parent and
// children agree, sibling names are unique, and no parent exceeds the fan-out.
func (t *tx) checkTreeShape(ctx context.Context, table catalog.Table) error {
	nodes, err := t.tableRouteNodes(ctx, table.ID)
	if err != nil {
		return err
	}
	fanout, err := t.branchFanout(ctx)
	if err != nil {
		return err
	}
	byID := map[string]routeNode{}
	for _, node := range nodes {
		byID[node.ID] = node
	}
	for _, node := range nodes {
		names := map[string]bool{}
		live := 0
		for _, childID := range node.ChildIDs {
			child, ok := byID[childID]
			if !ok {
				continue
			}
			if child.ParentID != node.ID {
				return fail(result.CodeInternal, "route %q lists child %q whose parent is %q", node.ID, childID, child.ParentID)
			}
			key := strings.ToLower(strings.TrimSpace(child.Name))
			if names[key] {
				return fail(result.CodeConstraint, "route %q has two children named %q", node.ID, child.Name)
			}
			names[key] = true
			live++
		}
		if live > fanout {
			return router.CheckBranchFanout(node.ID, live, 0, fanout)
		}
		if node.ParentID != "" {
			parent, ok := byID[node.ParentID]
			if !ok {
				return fail(result.CodeConstraint, "route %q has an invalid parent", node.ID)
			}
			found := false
			for _, id := range parent.ChildIDs {
				found = found || id == node.ID
			}
			if !found {
				return fail(result.CodeInternal, "route %q is missing from its parent's child_ids", node.ID)
			}
		}
	}
	return nil
}
