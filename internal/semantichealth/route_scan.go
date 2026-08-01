package semantichealth

import (
	"context"
	"sort"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/row"
)

const (
	maximumRouteChildren = 12
	maximumLeafLocators  = 100
	maximumLocatorScan   = 10_000
	locatorPageSize      = 1000
)

type healthTableState struct {
	database       catalog.Database
	table          catalog.Table
	rows           map[string]row.Row
	rowsComplete   bool
	routesComplete bool
	routedRows     map[string]bool
}

func scanRoutes(
	ctx context.Context,
	source Source,
	tables map[string]*healthTableState,
) ([]Issue, bool, error) {
	nodes, err := source.ListRouterNodes(ctx)
	if err != nil {
		return nil, false, healthError(result.CodeInternal, "list Router nodes: %v", err)
	}
	nodes = cloneAndSortNodes(nodes)
	byID, children, tableNodes := map[string]router.Node{}, map[string][]router.Node{}, map[string][]router.Node{}
	for _, node := range nodes {
		byID[node.ID] = node
		children[node.ParentID] = append(children[node.ParentID], node)
		tableNodes[node.TableID] = append(tableNodes[node.TableID], node)
	}
	issues := []Issue{}
	for tableID, state := range tables {
		state.routedRows = map[string]bool{}
		state.routesComplete = true
		roots := []string{}
		for _, node := range tableNodes[tableID] {
			if node.Kind == router.KindRoot && node.ParentID == "" {
				roots = append(roots, node.ID)
			}
		}
		if len(roots) != 1 {
			objects := append([]string(nil), roots...)
			if len(objects) == 0 {
				objects = []string{tableID}
			}
			issues = append(issues, tableIssue(state, KindInvalidRouteStructure,
				"review_route_structure", objects, "", len(roots)))
		}
	}
	for _, node := range nodes {
		state, knownTable := tables[node.TableID]
		valid := knownTable && node.DatabaseID == state.database.ID
		if node.Kind == router.KindRoot {
			valid = valid && node.ParentID == ""
		} else {
			parent, found := byID[node.ParentID]
			valid = valid && found && parent.DatabaseID == node.DatabaseID &&
				parent.TableID == node.TableID && parent.Kind != router.KindLeaf
		}
		if !valid {
			issues = append(issues, nodeIssue(state, node, KindInvalidRouteStructure,
				"review_route_structure", []string{node.ID}, "", 1))
		}
		if (node.Kind == router.KindRoot || node.Kind == router.KindBranch) &&
			len(children[node.ID]) >= maximumRouteChildren {
			ids := make([]string, 0, len(children[node.ID]))
			for _, child := range children[node.ID] {
				ids = append(ids, child.ID)
			}
			issues = append(issues, nodeIssue(state, node, KindRouteCapacity,
				"review_route_split", ids, "", len(ids)))
		}
	}
	issues = append(issues, siblingAmbiguityIssues(nodes, children, tables)...)

	remaining, truncated := maximumLocatorScan, false
	for _, node := range nodes {
		if node.Kind != router.KindLeaf {
			continue
		}
		state := tables[node.TableID]
		if state == nil {
			continue
		}
		locators, complete, err := scanLeaf(ctx, source, node.ID, &remaining)
		if err != nil {
			return nil, false, healthError(result.CodeInternal, "scan Route leaf %s: %v", node.ID, err)
		}
		if !complete {
			state.routesComplete, truncated = false, true
		}
		if len(locators) >= maximumLeafLocators {
			issues = append(issues, nodeIssue(state, node, KindRouteCapacity,
				"review_route_split", []string{node.ID}, "", len(locators)))
		}
		for _, locator := range locators {
			if locator.DatabaseID != node.DatabaseID || locator.TableID != node.TableID {
				issues = append(issues, nodeIssue(state, node, KindInvalidMembershipScope,
					"review_membership", []string{node.ID, locator.RowID}, locator.RowID, 1))
				continue
			}
			current, found := state.rows[locator.RowID]
			if !found {
				if state.rowsComplete {
					issues = append(issues, nodeIssue(state, node, KindOrphanMembership,
						"review_membership", []string{node.ID, locator.RowID}, locator.RowID, 1))
				}
				continue
			}
			state.routedRows[locator.RowID] = true
			if locator.Revision != current.Revision {
				issues = append(issues, nodeIssue(state, node, KindStaleMembership,
					"review_membership", []string{node.ID, locator.RowID}, locator.RowID, 1))
			}
		}
	}
	for _, state := range tables {
		if !state.rowsComplete || !state.routesComplete {
			continue
		}
		rowIDs := make([]string, 0, len(state.rows))
		for rowID := range state.rows {
			rowIDs = append(rowIDs, rowID)
		}
		sort.Strings(rowIDs)
		for _, rowID := range rowIDs {
			if !state.routedRows[rowID] {
				issues = append(issues, tableIssue(state, KindUnroutedRow,
					"review_membership", []string{rowID}, rowID, 1))
			}
		}
	}
	return issues, truncated, nil
}

func scanLeaf(ctx context.Context, source Source, leafID string, remaining *int) ([]router.Locator, bool, error) {
	locators, cursor, seen := []router.Locator{}, "", map[string]bool{}
	for {
		if *remaining == 0 {
			return locators, false, nil
		}
		limit := min(locatorPageSize, *remaining)
		page, receipt, err := source.ListRouterLeafPage(ctx, leafID, cursor, limit)
		if err != nil {
			return nil, false, err
		}
		if len(page) > limit {
			return nil, false, healthError(result.CodeInternal, "leaf page exceeded requested limit")
		}
		locators = append(locators, page...)
		*remaining -= len(page)
		if receipt.NextCursor == "" {
			return locators, true, nil
		}
		if seen[receipt.NextCursor] || receipt.NextCursor == cursor {
			return nil, false, healthError(result.CodeInternal, "leaf cursor did not advance")
		}
		seen[receipt.NextCursor], cursor = true, receipt.NextCursor
	}
}

func siblingAmbiguityIssues(
	nodes []router.Node,
	children map[string][]router.Node,
	tables map[string]*healthTableState,
) []Issue {
	issues := []Issue{}
	parents := make([]string, 0, len(children))
	for parent := range children {
		if parent != "" {
			parents = append(parents, parent)
		}
	}
	sort.Strings(parents)
	for _, parent := range parents {
		terms := map[string][]string{}
		for _, child := range children[parent] {
			for _, surface := range append([]string{child.Name}, child.Aliases...) {
				term := normalizeWords(surface)
				if term != "" && !containsID(terms[term], child.ID) {
					terms[term] = append(terms[term], child.ID)
				}
			}
		}
		keys := make([]string, 0, len(terms))
		for term := range terms {
			keys = append(keys, term)
		}
		sort.Strings(keys)
		for _, term := range keys {
			ids := terms[term]
			if len(ids) < 2 {
				continue
			}
			sort.Strings(ids)
			first := findNode(nodes, ids[0])
			issues = append(issues, nodeIssue(tables[first.TableID], first, KindAmbiguousSiblings,
				"review_route_ambiguity", ids, "", len(ids)))
		}
	}
	return issues
}

func tableIssue(state *healthTableState, kind Kind, action string, objectIDs []string, rowID string, count int) Issue {
	issue := Issue{Kind: kind, Severity: SeverityReviewRequired, AutoFix: false, Action: action,
		RowID: rowID, ObjectIDs: objectIDs, Count: count}
	if state != nil {
		issue.DatabaseID, issue.Database = state.database.ID, state.database.Name
		issue.TableID, issue.Table = state.table.ID, state.table.Name
	}
	return newIssue(issue)
}

func nodeIssue(state *healthTableState, node router.Node, kind Kind, action string, objectIDs []string, rowID string, count int) Issue {
	issue := tableIssue(state, kind, action, objectIDs, rowID, count)
	if issue.DatabaseID == "" {
		issue.DatabaseID, issue.TableID = node.DatabaseID, node.TableID
		issue = newIssue(issue)
	}
	return issue
}

func cloneAndSortNodes(values []router.Node) []router.Node {
	result := make([]router.Node, len(values))
	for index, value := range values {
		value.Aliases = append([]string(nil), value.Aliases...)
		result[index] = value
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result
}

func findNode(nodes []router.Node, id string) router.Node {
	for _, node := range nodes {
		if node.ID == id {
			return node
		}
	}
	return router.Node{}
}

func containsID(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
