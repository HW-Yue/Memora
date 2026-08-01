package routemutationplan

import (
	"sort"
	"strings"

	"github.com/HW-Yue/Memora/internal/result"
)

// Validate proves that a plan still has the canonical identity emitted by
// Build and that every action stays within the bounded, guarded vocabulary.
func Validate(plan Plan) error {
	if plan.Version != PlanVersion || plan.Status != StatusReviewRequired ||
		blank(plan.PlanID) || blank(plan.ProposalID) || blank(plan.Actor) ||
		blank(plan.SourceEventID) || blank(plan.Reason) || blank(plan.Scope.DatabaseID) ||
		blank(plan.Scope.TableID) || blank(plan.BaseSnapshotHash) || blank(plan.Hash) {
		return planError(result.CodeValidation, "plan identity, scope, provenance, and status are required")
	}
	if len(plan.NodeGuards) == 0 || len(plan.NodeGuards) > maximumNodes ||
		len(plan.ChildSetGuards) > maximumNodes || len(plan.LocatorSetGuards) > maximumSources ||
		len(plan.Creates) > maximumTargets || len(plan.Moves) > maximumNodes ||
		len(plan.MembershipMoves) > maximumLocatorScan || len(plan.Deletes) > maximumSources {
		return planError(result.CodeConstraint, "plan exceeds the local execution budget")
	}
	if plan.Operation != OperationSplit && plan.Operation != OperationMerge && plan.Operation != OperationMove {
		return planError(result.CodeValidation, "plan operation is invalid")
	}
	if !canonicalPlanOrder(plan) {
		return planError(result.CodeValidation, "plan collections are not canonical")
	}
	guards, childSets, locatorSets := map[string]uint64{}, map[string]bool{}, map[string]bool{}
	for _, guard := range plan.NodeGuards {
		if blank(guard.RouteID) || guard.Revision == 0 || guards[guard.RouteID] != 0 {
			return planError(result.CodeValidation, "node guards are invalid or duplicated")
		}
		guards[guard.RouteID] = guard.Revision
	}
	for _, guard := range plan.ChildSetGuards {
		if blank(guard.ParentRouteID) || childSets[guard.ParentRouteID] || guards[guard.ParentRouteID] == 0 ||
			!uniqueCanonicalStrings(guard.ChildRouteIDs) {
			return planError(result.CodeValidation, "child-set guards are invalid or duplicated")
		}
		childSets[guard.ParentRouteID] = true
	}
	for _, guard := range plan.LocatorSetGuards {
		if blank(guard.LeafRouteID) || locatorSets[guard.LeafRouteID] || guards[guard.LeafRouteID] == 0 {
			return planError(result.CodeValidation, "locator-set guards are invalid or duplicated")
		}
		last := ""
		for _, locator := range guard.Locators {
			if blank(locator.RowID) || locator.Revision == 0 || (last != "" && locator.RowID <= last) {
				return planError(result.CodeValidation, "locator guards are invalid or duplicated")
			}
			last = locator.RowID
		}
		locatorSets[guard.LeafRouteID] = true
	}
	created := map[string]bool{}
	for _, value := range plan.Creates {
		if blank(value.TargetKey) || blank(value.RouteID) || created[value.RouteID] ||
			blank(value.ParentID) || guards[value.ParentID] == 0 || !validRouteName(value.Name) ||
			blank(value.Purpose) || (value.Kind != "branch" && value.Kind != "leaf") {
			return planError(result.CodeValidation, "Route creates are invalid or duplicated")
		}
		created[value.RouteID] = true
	}
	moved := map[string]bool{}
	for _, value := range plan.Moves {
		if blank(value.RouteID) || moved[value.RouteID] || guards[value.RouteID] == 0 ||
			blank(value.FromParentID) || !childSets[value.FromParentID] ||
			blank(value.ToParentID) || (!created[value.ToParentID] && guards[value.ToParentID] == 0) {
			return planError(result.CodeValidation, "Route moves are invalid or unguarded")
		}
		moved[value.RouteID] = true
	}
	membershipCount, movedRows := 0, map[string]bool{}
	movedFromLeaf := map[string]map[string]bool{}
	for _, value := range plan.MembershipMoves {
		if blank(value.RowID) || value.Revision == 0 || movedRows[value.RowID] ||
			len(value.FromLeafIDs) == 0 || !uniqueCanonicalStrings(value.FromLeafIDs) ||
			blank(value.ToLeafID) || (!created[value.ToLeafID] && guards[value.ToLeafID] == 0) {
			return planError(result.CodeValidation, "membership moves are invalid or duplicated")
		}
		for _, leafID := range value.FromLeafIDs {
			if !locatorSets[leafID] {
				return planError(result.CodeValidation, "membership source is not guarded")
			}
			if movedFromLeaf[leafID] == nil {
				movedFromLeaf[leafID] = map[string]bool{}
			}
			movedFromLeaf[leafID][value.RowID] = true
		}
		movedRows[value.RowID], membershipCount = true, membershipCount+len(value.FromLeafIDs)
	}
	deleted := map[string]bool{}
	for _, value := range plan.Deletes {
		if blank(value.RouteID) || deleted[value.RouteID] || guards[value.RouteID] != value.Revision {
			return planError(result.CodeValidation, "Route deletes are invalid or unguarded")
		}
		deleted[value.RouteID] = true
	}
	for _, value := range plan.Deletes {
		if childSets[value.RouteID] {
			for _, guard := range plan.ChildSetGuards {
				if guard.ParentRouteID != value.RouteID {
					continue
				}
				for _, childID := range guard.ChildRouteIDs {
					found := false
					for _, move := range plan.Moves {
						found = found || (move.RouteID == childID && move.FromParentID == value.RouteID)
					}
					if !found {
						return planError(result.CodeValidation, "deleted branch contents are not completely moved")
					}
				}
			}
		}
		if locatorSets[value.RouteID] {
			for _, guard := range plan.LocatorSetGuards {
				if guard.LeafRouteID != value.RouteID {
					continue
				}
				for _, locator := range guard.Locators {
					if !movedFromLeaf[value.RouteID][locator.RowID] {
						return planError(result.CodeValidation, "deleted leaf locators are not completely moved")
					}
				}
			}
		}
		if !childSets[value.RouteID] && !locatorSets[value.RouteID] {
			return planError(result.CodeValidation, "deleted Route contents are not guarded")
		}
	}
	switch plan.Operation {
	case OperationSplit:
		if len(plan.Creates) < 2 || len(plan.Deletes) != 1 || len(plan.Moves)+len(plan.MembershipMoves) == 0 {
			return planError(result.CodeValidation, "SPLIT plan shape is invalid")
		}
	case OperationMerge:
		if len(plan.Creates) != 1 || len(plan.Deletes) < 2 {
			return planError(result.CodeValidation, "MERGE plan shape is invalid")
		}
	case OperationMove:
		if len(plan.Creates) != 0 || len(plan.Deletes) != 0 || len(plan.MembershipMoves) != 0 || len(plan.Moves) != 1 {
			return planError(result.CodeValidation, "MOVE plan shape is invalid")
		}
	}
	wantImpact := Impact{CreatedNodes: len(plan.Creates), ReparentedNodes: len(plan.Moves),
		MovedMemberships: membershipCount, DeletedNodes: len(plan.Deletes)}
	if plan.Impact != wantImpact {
		return planError(result.CodeValidation, "plan impact does not match its actions")
	}
	snapshot, err := SnapshotHash(plan.Scope, plan.NodeGuards, plan.ChildSetGuards, plan.LocatorSetGuards)
	if err != nil || snapshot != plan.BaseSnapshotHash {
		return planError(result.CodeValidation, "base snapshot hash is invalid")
	}
	seed := plan
	seed.PlanID, seed.Hash = "", ""
	identity, err := digest(seed)
	if err != nil || plan.PlanID != "route_plan_"+strings.TrimPrefix(identity, "sha256:")[:24] {
		return planError(result.CodeValidation, "plan identity is invalid")
	}
	hashed := plan
	hashed.Hash = ""
	wantHash, err := digest(hashed)
	if err != nil || wantHash != plan.Hash {
		return planError(result.CodeValidation, "plan hash is invalid")
	}
	return nil
}

func SnapshotHash(scope Scope, nodes []NodeGuard, children []ChildSetGuard, memberships []LocatorSetGuard) (string, error) {
	return digest(struct {
		Scope       Scope             `json:"scope"`
		Nodes       []NodeGuard       `json:"node_guards"`
		Children    []ChildSetGuard   `json:"child_set_guards"`
		Memberships []LocatorSetGuard `json:"locator_set_guards"`
	}{scope, nodes, children, memberships})
}

func canonicalPlanOrder(plan Plan) bool {
	return sortedBy(plan.NodeGuards, func(v NodeGuard) string { return v.RouteID }) &&
		sortedBy(plan.ChildSetGuards, func(v ChildSetGuard) string { return v.ParentRouteID }) &&
		sortedBy(plan.LocatorSetGuards, func(v LocatorSetGuard) string { return v.LeafRouteID }) &&
		sortedBy(plan.Creates, func(v NodeCreate) string { return v.TargetKey }) &&
		sortedBy(plan.Moves, func(v NodeMove) string { return v.RouteID }) &&
		sortedBy(plan.MembershipMoves, func(v MembershipMove) string { return v.RowID }) &&
		sortedBy(plan.Deletes, func(v NodeDelete) string { return v.RouteID })
}

func sortedBy[T any](values []T, key func(T) string) bool {
	for index := 1; index < len(values); index++ {
		if key(values[index-1]) >= key(values[index]) {
			return false
		}
	}
	return true
}

func uniqueCanonicalStrings(values []string) bool {
	if !sort.StringsAreSorted(values) {
		return false
	}
	for index, value := range values {
		if strings.TrimSpace(value) == "" || (index > 0 && values[index-1] == value) {
			return false
		}
	}
	return true
}
