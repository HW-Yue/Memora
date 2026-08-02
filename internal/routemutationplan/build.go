package routemutationplan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/router"
)

const (
	maximumNodes        = 10_000
	maximumSources      = 12
	maximumTargets      = 12
	maximumChildren     = 12
	maximumLeafLocators = 1
	maximumLocatorScan  = 2_000
	maximumLocatorPages = 100
	locatorPageSize     = 100
)

type builder struct {
	ctx       context.Context
	source    Source
	scope     Scope
	proposal  Proposal
	nodes     []router.Node
	byID      map[string]router.Node
	children  map[string][]router.Node
	remaining int
	plan      Plan
}

func Build(ctx context.Context, source Source, scope Scope, proposal Proposal) (Plan, error) {
	if source == nil {
		return Plan{}, planError(result.CodeInternal, "source is unavailable")
	}
	if err := validateProposalIdentity(scope, proposal); err != nil {
		return Plan{}, err
	}
	nodes, err := source.ListRouterNodes(ctx)
	if err != nil {
		return Plan{}, sourceError("list Router nodes", err)
	}
	if len(nodes) > maximumNodes {
		return Plan{}, planError(result.CodeConstraint, "Router node scan exceeds %d", maximumNodes)
	}
	b := &builder{
		ctx: ctx, source: source, scope: scope, proposal: normalizeProposal(proposal),
		byID: map[string]router.Node{}, children: map[string][]router.Node{}, remaining: maximumLocatorScan,
		plan: Plan{
			Version: PlanVersion, ProposalID: strings.TrimSpace(proposal.ID), Operation: proposal.Operation,
			Status: StatusReviewRequired, Scope: scope, Actor: strings.TrimSpace(proposal.Actor),
			SourceEventID: strings.TrimSpace(proposal.SourceEventID), Reason: strings.TrimSpace(proposal.Reason),
			NodeGuards: []NodeGuard{}, ChildSetGuards: []ChildSetGuard{}, LocatorSetGuards: []LocatorSetGuard{},
			Creates: []NodeCreate{}, Moves: []NodeMove{}, MembershipMoves: []MembershipMove{}, Deletes: []NodeDelete{},
		},
	}
	for _, node := range nodes {
		if node.ID == "" || b.byID[node.ID].ID != "" {
			return Plan{}, planError(result.CodeInternal, "Router node identities are invalid or duplicated")
		}
		node.Aliases = append([]string(nil), node.Aliases...)
		sort.Strings(node.Aliases)
		b.byID[node.ID] = node
		if node.DatabaseID == scope.DatabaseID && node.TableID == scope.TableID && !node.Deleted {
			if node.Version != router.Version || node.Revision == 0 ||
				(node.Kind != router.KindRoot && node.Kind != router.KindBranch && node.Kind != router.KindLeaf) {
				return Plan{}, planError(result.CodeInternal, "Route %q structure is invalid", node.ID)
			}
			b.nodes = append(b.nodes, node)
			b.children[node.ParentID] = append(b.children[node.ParentID], node)
		}
	}
	sort.Slice(b.nodes, func(left, right int) bool { return b.nodes[left].ID < b.nodes[right].ID })
	for parentID := range b.children {
		sort.Slice(b.children[parentID], func(left, right int) bool {
			return b.children[parentID][left].ID < b.children[parentID][right].ID
		})
	}
	switch b.proposal.Operation {
	case OperationSplit:
		err = b.buildSplit()
	case OperationMerge:
		err = b.buildMerge()
	case OperationMove:
		err = b.buildMove()
	default:
		err = planError(result.CodeValidation, "operation %q is not supported", b.proposal.Operation)
	}
	if err != nil {
		return Plan{}, err
	}
	b.canonicalize()
	b.plan.BaseSnapshotHash, err = SnapshotHash(
		b.scope, b.plan.NodeGuards, b.plan.ChildSetGuards, b.plan.LocatorSetGuards,
	)
	if err != nil {
		return Plan{}, planError(result.CodeInternal, "snapshot could not be hashed")
	}
	seed, err := digest(b.plan)
	if err != nil {
		return Plan{}, planError(result.CodeInternal, "plan identity could not be hashed")
	}
	b.plan.PlanID = "route_plan_" + strings.TrimPrefix(seed, "sha256:")[:24]
	b.plan.Hash, err = digest(b.plan)
	if err != nil {
		return Plan{}, planError(result.CodeInternal, "plan could not be hashed")
	}
	return b.plan, nil
}

func validateProposalIdentity(scope Scope, proposal Proposal) error {
	if strings.TrimSpace(scope.DatabaseID) == "" || strings.TrimSpace(scope.TableID) == "" {
		return planError(result.CodeValidation, "Database and Table scope are required")
	}
	if proposal.Version != ProposalVersion {
		return planError(result.CodeValidation, "proposal version = %q, want %q", proposal.Version, ProposalVersion)
	}
	if blank(proposal.ID) || blank(proposal.Actor) || blank(proposal.SourceEventID) || blank(proposal.Reason) {
		return planError(result.CodeValidation, "proposal identity and provenance are required")
	}
	if len(proposal.Sources) == 0 || len(proposal.Sources) > maximumSources || len(proposal.Targets) > maximumTargets {
		return planError(result.CodeConstraint, "proposal source or target count exceeds the local plan budget")
	}
	seenSources := map[string]bool{}
	for _, source := range proposal.Sources {
		id := strings.TrimSpace(source.RouteID)
		if id == "" || seenSources[id] {
			return planError(result.CodeValidation, "proposal source IDs must be non-empty and unique")
		}
		seenSources[id] = true
	}
	return nil
}

func normalizeProposal(value Proposal) Proposal {
	value.ID, value.Actor = strings.TrimSpace(value.ID), strings.TrimSpace(value.Actor)
	value.SourceEventID, value.Reason = strings.TrimSpace(value.SourceEventID), strings.TrimSpace(value.Reason)
	value.TargetParentID = strings.TrimSpace(value.TargetParentID)
	value.Sources = append([]SourceRef(nil), value.Sources...)
	sort.Slice(value.Sources, func(left, right int) bool { return value.Sources[left].RouteID < value.Sources[right].RouteID })
	value.Targets = append([]TargetProposal(nil), value.Targets...)
	for index := range value.Targets {
		target := &value.Targets[index]
		target.Key, target.Name = strings.TrimSpace(target.Key), strings.TrimSpace(target.Name)
		target.Purpose, target.Synopsis = strings.TrimSpace(target.Purpose), strings.TrimSpace(target.Synopsis)
		target.ChildRouteIDs = sortedStrings(target.ChildRouteIDs)
		target.RowIDs = sortedStrings(target.RowIDs)
	}
	sort.Slice(value.Targets, func(left, right int) bool { return value.Targets[left].Key < value.Targets[right].Key })
	return value
}

func (b *builder) buildSplit() error {
	if len(b.proposal.Sources) != 1 || len(b.proposal.Targets) < 2 || b.proposal.TargetParentID != "" {
		return planError(result.CodeValidation, "SPLIT requires one source, at least two targets, and no target parent")
	}
	source, err := b.sourceNode(b.proposal.Sources[0])
	if err != nil {
		return err
	}
	if source.Kind == router.KindRoot {
		return planError(result.CodeConstraint, "Router root cannot be split")
	}
	parent, err := b.node(source.ParentID)
	if err != nil {
		return err
	}
	if parent.Kind == router.KindLeaf {
		return planError(result.CodeConstraint, "SPLIT source parent cannot be a leaf")
	}
	if len(b.children[parent.ID])-1+len(b.proposal.Targets) > maximumChildren {
		return planError(result.CodeConstraint, "SPLIT would exceed parent child capacity")
	}
	if err := b.validateTargets(b.proposal.Targets, b.children[parent.ID], map[string]bool{source.ID: true}); err != nil {
		return err
	}
	b.guardParent(parent)
	creates, err := b.creates(parent.ID, source.Kind, b.proposal.Targets)
	if err != nil {
		return err
	}
	b.plan.Creates = append(b.plan.Creates, creates...)
	b.addNodeGuard(source)
	if source.Kind == router.KindBranch {
		b.addChildSet(source.ID)
		contents := b.children[source.ID]
		if err := completeChildAssignment(b.proposal.Targets, contents); err != nil {
			return err
		}
		for index, target := range b.proposal.Targets {
			if len(target.ChildRouteIDs) > maximumChildren {
				return planError(result.CodeConstraint, "split target %q exceeds child capacity", target.Key)
			}
			for _, childID := range target.ChildRouteIDs {
				child, _ := b.node(childID)
				if err := b.guardSubtree(child, map[string]bool{}); err != nil {
					return err
				}
				b.plan.Moves = append(b.plan.Moves, NodeMove{RouteID: child.ID, FromParentID: source.ID, ToParentID: creates[index].RouteID})
			}
		}
	} else if source.Kind == router.KindLeaf {
		return planError(result.CodeConstraint, "single-Row Route leaf cannot be split; split the Row or restructure a branch")
	} else {
		return planError(result.CodeConstraint, "SPLIT source must be a branch or leaf")
	}
	b.plan.Deletes = append(b.plan.Deletes, NodeDelete{RouteID: source.ID, Revision: source.Revision})
	return nil
}

func (b *builder) buildMerge() error {
	if len(b.proposal.Sources) < 2 || len(b.proposal.Targets) != 1 || b.proposal.TargetParentID != "" {
		return planError(result.CodeValidation, "MERGE requires at least two sources, one target, and no target parent")
	}
	if len(b.proposal.Targets[0].ChildRouteIDs) != 0 || len(b.proposal.Targets[0].RowIDs) != 0 {
		return planError(result.CodeValidation, "MERGE target contents are derived from all sources")
	}
	sources := make([]router.Node, 0, len(b.proposal.Sources))
	for _, reference := range b.proposal.Sources {
		node, err := b.sourceNode(reference)
		if err != nil {
			return err
		}
		if node.Kind == router.KindRoot {
			return planError(result.CodeConstraint, "Router root cannot be merged")
		}
		sources = append(sources, node)
	}
	parentID, kind := sources[0].ParentID, sources[0].Kind
	excluded := map[string]bool{}
	for _, source := range sources {
		if source.ParentID != parentID || source.Kind != kind {
			return planError(result.CodeConstraint, "MERGE sources must be same-kind siblings")
		}
		excluded[source.ID] = true
	}
	parent, err := b.node(parentID)
	if err != nil {
		return err
	}
	if parent.Kind == router.KindLeaf {
		return planError(result.CodeConstraint, "MERGE source parent cannot be a leaf")
	}
	if len(b.children[parent.ID])-len(sources)+1 > maximumChildren {
		return planError(result.CodeConstraint, "MERGE would exceed parent child capacity")
	}
	if err := b.validateTargets(b.proposal.Targets, b.children[parent.ID], excluded); err != nil {
		return err
	}
	b.guardParent(parent)
	creates, err := b.creates(parent.ID, kind, b.proposal.Targets)
	if err != nil {
		return err
	}
	b.plan.Creates = append(b.plan.Creates, creates...)
	targetID := creates[0].RouteID
	if kind == router.KindBranch {
		count := 0
		for _, source := range sources {
			b.addNodeGuard(source)
			b.addChildSet(source.ID)
			count += len(b.children[source.ID])
			for _, child := range b.children[source.ID] {
				if err := b.guardSubtree(child, map[string]bool{}); err != nil {
					return err
				}
				b.plan.Moves = append(b.plan.Moves, NodeMove{RouteID: child.ID, FromParentID: source.ID, ToParentID: targetID})
			}
		}
		if count > maximumChildren {
			return planError(result.CodeConstraint, "MERGE target exceeds child capacity")
		}
	} else if kind == router.KindLeaf {
		merged := map[string]MembershipMove{}
		for _, source := range sources {
			b.addNodeGuard(source)
			locators, err := b.leafLocators(source)
			if err != nil {
				return err
			}
			for _, locator := range locators {
				move := merged[locator.RowID]
				if move.RowID != "" && move.Revision != locator.Revision {
					return planError(result.CodeRevisionConflict, "Row %q has inconsistent locator revisions", locator.RowID)
				}
				move.RowID, move.Revision, move.ToLeafID = locator.RowID, locator.Revision, targetID
				move.FromLeafIDs = append(move.FromLeafIDs, source.ID)
				merged[locator.RowID] = move
			}
		}
		if len(merged) > maximumLeafLocators {
			return planError(result.CodeConstraint, "MERGE would place distinct Rows in one Route leaf")
		}
		for _, move := range merged {
			move.FromLeafIDs = uniqueSorted(move.FromLeafIDs)
			b.plan.MembershipMoves = append(b.plan.MembershipMoves, move)
		}
	} else {
		return planError(result.CodeConstraint, "MERGE sources must be branches or leaves")
	}
	for _, source := range sources {
		b.plan.Deletes = append(b.plan.Deletes, NodeDelete{RouteID: source.ID, Revision: source.Revision})
	}
	return nil
}

func (b *builder) buildMove() error {
	if len(b.proposal.Sources) != 1 || len(b.proposal.Targets) != 0 || b.proposal.TargetParentID == "" {
		return planError(result.CodeValidation, "MOVE requires one source, one target parent, and no new targets")
	}
	source, err := b.sourceNode(b.proposal.Sources[0])
	if err != nil {
		return err
	}
	if source.Kind == router.KindRoot {
		return planError(result.CodeConstraint, "Router root cannot be moved")
	}
	target, err := b.node(b.proposal.TargetParentID)
	if err != nil {
		return err
	}
	if target.Kind == router.KindLeaf || target.ID == source.ParentID || target.ID == source.ID {
		return planError(result.CodeConstraint, "MOVE target must be a different root or branch")
	}
	if b.descendant(source.ID, target.ID) {
		return planError(result.CodeConstraint, "MOVE cannot place a Route inside its own subtree")
	}
	if len(b.children[target.ID])+1 > maximumChildren {
		return planError(result.CodeConstraint, "MOVE target exceeds child capacity")
	}
	if siblingConflict(source.Name, b.children[target.ID], nil) {
		return planError(result.CodeConstraint, "MOVE target has a sibling name or alias conflict")
	}
	oldParent, err := b.node(source.ParentID)
	if err != nil {
		return err
	}
	if oldParent.Kind == router.KindLeaf || (source.Kind == router.KindLeaf && len(b.children[source.ID]) != 0) {
		return planError(result.CodeConstraint, "MOVE source structure is invalid")
	}
	b.guardParent(oldParent)
	b.guardParent(target)
	if err := b.guardSubtree(source, map[string]bool{}); err != nil {
		return err
	}
	b.plan.Moves = append(b.plan.Moves, NodeMove{RouteID: source.ID, FromParentID: oldParent.ID, ToParentID: target.ID})
	return nil
}

func (b *builder) sourceNode(reference SourceRef) (router.Node, error) {
	if blank(reference.RouteID) || reference.ExpectedRevision == 0 {
		return router.Node{}, planError(result.CodeValidation, "source Route ID and expected revision are required")
	}
	node, err := b.node(reference.RouteID)
	if err != nil {
		return router.Node{}, err
	}
	if node.Revision != reference.ExpectedRevision {
		return router.Node{}, planError(result.CodeRevisionConflict, "Route %q revision conflicts with proposal", node.ID)
	}
	return node, nil
}

func (b *builder) node(id string) (router.Node, error) {
	node, found := b.byID[id]
	if !found || node.Deleted {
		return router.Node{}, planError(result.CodeNotFound, "Route %q was not found", id)
	}
	if node.Version != router.Version || node.Revision == 0 ||
		(node.Kind != router.KindRoot && node.Kind != router.KindBranch && node.Kind != router.KindLeaf) {
		return router.Node{}, planError(result.CodeInternal, "Route %q structure is invalid", id)
	}
	if node.DatabaseID != b.scope.DatabaseID || node.TableID != b.scope.TableID {
		return router.Node{}, planError(result.CodePermissionDenied, "Route %q is outside the bound Table", id)
	}
	return node, nil
}

func (b *builder) validateTargets(targets []TargetProposal, siblings []router.Node, excluded map[string]bool) error {
	keys, names := map[string]bool{}, map[string]bool{}
	for _, target := range targets {
		if !validRouteName(target.Key) || keys[target.Key] || !validRouteName(target.Name) || blank(target.Purpose) ||
			!utf8.ValidString(target.Purpose) || !utf8.ValidString(target.Synopsis) ||
			utf8.RuneCountInString(target.Purpose) > 800 || utf8.RuneCountInString(target.Synopsis) > 800 {
			return planError(result.CodeValidation, "target key, name, purpose, or synopsis is invalid")
		}
		keys[target.Key] = true
		name := canonical(target.Name)
		if names[name] || siblingConflict(target.Name, siblings, excluded) {
			return planError(result.CodeConstraint, "target %q has a sibling name or alias conflict", target.Key)
		}
		names[name] = true
	}
	return nil
}

func (b *builder) creates(parentID string, kind router.Kind, targets []TargetProposal) ([]NodeCreate, error) {
	values := make([]NodeCreate, 0, len(targets))
	for _, target := range targets {
		digest := sha256.Sum256([]byte(b.scope.TableID + "\x00" + b.proposal.ID + "\x00" + target.Key))
		id := "route_" + hex.EncodeToString(digest[:16])
		if existing := b.byID[id]; existing.ID != "" {
			return nil, planError(result.CodeAlreadyExists, "derived target Route ID already exists")
		}
		values = append(values, NodeCreate{TargetKey: target.Key, RouteID: id, ParentID: parentID,
			Name: target.Name, Kind: kind, Purpose: target.Purpose, Synopsis: target.Synopsis})
	}
	return values, nil
}

func (b *builder) leafLocators(leaf router.Node) ([]router.Locator, error) {
	values, cursor, seen, snapshot, pages := []router.Locator{}, "", map[string]bool{}, "", 0
	for {
		pages++
		if pages > maximumLocatorPages {
			return nil, planError(result.CodeConstraint, "locator scan exceeds %d pages", maximumLocatorPages)
		}
		if b.remaining == 0 {
			return nil, planError(result.CodeConstraint, "locator scan exceeds %d", maximumLocatorScan)
		}
		limit := min(locatorPageSize, b.remaining)
		page, receipt, err := b.source.ListRouterLeafPage(b.ctx, leaf.ID, cursor, limit)
		if err != nil {
			return nil, sourceError("scan Route leaf", err)
		}
		if len(page) > limit {
			return nil, planError(result.CodeInternal, "leaf page exceeded requested limit")
		}
		if snapshot != "" && receipt.Snapshot != "" && receipt.Snapshot != snapshot {
			return nil, planError(result.CodeRevisionConflict, "leaf snapshot changed during plan")
		}
		if snapshot == "" {
			snapshot = receipt.Snapshot
		}
		values = append(values, page...)
		b.remaining -= len(page)
		if receipt.NextCursor == "" {
			break
		}
		if receipt.NextCursor == cursor || seen[receipt.NextCursor] {
			return nil, planError(result.CodeValidation, "leaf cursor did not advance")
		}
		seen[receipt.NextCursor], cursor = true, receipt.NextCursor
	}
	sort.Slice(values, func(left, right int) bool {
		if values[left].RowID == values[right].RowID {
			return values[left].Revision < values[right].Revision
		}
		return values[left].RowID < values[right].RowID
	})
	guards, seenRows := make([]LocatorGuard, 0, len(values)), map[string]bool{}
	for _, locator := range values {
		if locator.DatabaseID != b.scope.DatabaseID || locator.TableID != b.scope.TableID ||
			blank(locator.RowID) || locator.Revision == 0 || seenRows[locator.RowID] {
			return nil, planError(result.CodeInternal, "leaf membership scope or identity is invalid")
		}
		seenRows[locator.RowID] = true
		guards = append(guards, LocatorGuard{RowID: locator.RowID, Revision: locator.Revision})
	}
	b.plan.LocatorSetGuards = append(b.plan.LocatorSetGuards, LocatorSetGuard{LeafRouteID: leaf.ID, Locators: guards})
	return values, nil
}

func completeChildAssignment(targets []TargetProposal, children []router.Node) error {
	want, got := map[string]bool{}, map[string]bool{}
	for _, child := range children {
		want[child.ID] = true
	}
	for _, target := range targets {
		if len(target.ChildRouteIDs) == 0 || len(target.RowIDs) != 0 {
			return planError(result.CodeValidation, "branch SPLIT targets require only non-empty child_route_ids")
		}
		for _, id := range target.ChildRouteIDs {
			if !want[id] || got[id] {
				return planError(result.CodeConstraint, "branch SPLIT assignment is incomplete or duplicated")
			}
			got[id] = true
		}
	}
	if len(got) != len(want) {
		return planError(result.CodeConstraint, "branch SPLIT assignment is incomplete or duplicated")
	}
	return nil
}

func (b *builder) guardParent(parent router.Node) {
	b.addNodeGuard(parent)
	b.addChildSet(parent.ID)
	for _, child := range b.children[parent.ID] {
		b.addNodeGuard(child)
	}
}

func (b *builder) guardSubtree(node router.Node, seen map[string]bool) error {
	if seen[node.ID] {
		return planError(result.CodeConstraint, "Router subtree contains a cycle")
	}
	seen[node.ID] = true
	b.addNodeGuard(node)
	if node.Kind == router.KindLeaf {
		return nil
	}
	b.addChildSet(node.ID)
	for _, child := range b.children[node.ID] {
		if err := b.guardSubtree(child, seen); err != nil {
			return err
		}
	}
	return nil
}

func (b *builder) addNodeGuard(node router.Node) {
	for _, guard := range b.plan.NodeGuards {
		if guard.RouteID == node.ID {
			return
		}
	}
	b.plan.NodeGuards = append(b.plan.NodeGuards, NodeGuard{RouteID: node.ID, Revision: node.Revision})
}

func (b *builder) addChildSet(parentID string) {
	for _, guard := range b.plan.ChildSetGuards {
		if guard.ParentRouteID == parentID {
			return
		}
	}
	ids := make([]string, 0, len(b.children[parentID]))
	for _, child := range b.children[parentID] {
		ids = append(ids, child.ID)
	}
	b.plan.ChildSetGuards = append(b.plan.ChildSetGuards, ChildSetGuard{ParentRouteID: parentID, ChildRouteIDs: ids})
}

func (b *builder) descendant(parentID, candidateID string) bool {
	seen, pending := map[string]bool{}, []string{parentID}
	for len(pending) > 0 {
		last := len(pending) - 1
		current := pending[last]
		pending = pending[:last]
		if seen[current] {
			continue
		}
		seen[current] = true
		for _, child := range b.children[current] {
			if child.ID == candidateID {
				return true
			}
			pending = append(pending, child.ID)
		}
	}
	return false
}

func (b *builder) canonicalize() {
	sort.Slice(b.plan.NodeGuards, func(left, right int) bool { return b.plan.NodeGuards[left].RouteID < b.plan.NodeGuards[right].RouteID })
	sort.Slice(b.plan.ChildSetGuards, func(left, right int) bool {
		return b.plan.ChildSetGuards[left].ParentRouteID < b.plan.ChildSetGuards[right].ParentRouteID
	})
	sort.Slice(b.plan.LocatorSetGuards, func(left, right int) bool {
		return b.plan.LocatorSetGuards[left].LeafRouteID < b.plan.LocatorSetGuards[right].LeafRouteID
	})
	sort.Slice(b.plan.Creates, func(left, right int) bool { return b.plan.Creates[left].TargetKey < b.plan.Creates[right].TargetKey })
	sort.Slice(b.plan.Moves, func(left, right int) bool { return b.plan.Moves[left].RouteID < b.plan.Moves[right].RouteID })
	sort.Slice(b.plan.MembershipMoves, func(left, right int) bool {
		return b.plan.MembershipMoves[left].RowID < b.plan.MembershipMoves[right].RowID
	})
	sort.Slice(b.plan.Deletes, func(left, right int) bool { return b.plan.Deletes[left].RouteID < b.plan.Deletes[right].RouteID })
	movedMemberships := 0
	for _, move := range b.plan.MembershipMoves {
		movedMemberships += len(move.FromLeafIDs)
	}
	b.plan.Impact = Impact{CreatedNodes: len(b.plan.Creates), ReparentedNodes: len(b.plan.Moves),
		MovedMemberships: movedMemberships, DeletedNodes: len(b.plan.Deletes)}
}

func siblingConflict(name string, siblings []router.Node, excluded map[string]bool) bool {
	want := canonical(name)
	for _, sibling := range siblings {
		if excluded != nil && excluded[sibling.ID] {
			continue
		}
		for _, surface := range append([]string{sibling.Name}, sibling.Aliases...) {
			if canonical(surface) == want {
				return true
			}
		}
	}
	return false
}

func validRouteName(name string) bool {
	if name == "" || name != strings.TrimSpace(name) || name != strings.ToLower(name) || utf8.RuneCountInString(name) > 64 {
		return false
	}
	for _, value := range name {
		if !unicode.IsLetter(value) && !unicode.IsNumber(value) && value != '-' && value != '_' {
			return false
		}
	}
	return true
}

func uniqueSorted(values []string) []string {
	result, seen := make([]string, 0, len(values)), map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func sortedStrings(values []string) []string {
	result := append([]string(nil), values...)
	for index := range result {
		result[index] = strings.TrimSpace(result[index])
	}
	sort.Strings(result)
	return result
}

func digest(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(hash[:]), nil
}

func sourceError(action string, err error) error {
	if stable, ok := err.(interface{ StableCode() string }); ok {
		return &Error{Code: result.Code(stable.StableCode()), Message: fmt.Sprintf("%s: %v", action, err)}
	}
	return planError(result.CodeInternal, "%s: %v", action, err)
}

func planError(code result.Code, format string, arguments ...any) error {
	return &Error{Code: code, Message: fmt.Sprintf(format, arguments...)}
}

func blank(value string) bool       { return strings.TrimSpace(value) == "" }
func canonical(value string) string { return strings.ToLower(strings.TrimSpace(value)) }
