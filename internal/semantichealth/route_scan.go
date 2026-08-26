package semantichealth

import (
	"context"
	"sort"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/row"
)

type healthTableState struct {
	database     catalog.Database
	table        catalog.Table
	rows         map[string]row.Row
	rowsComplete bool
	routedRows   map[string]bool
}

func scanRoutes(
	ctx context.Context,
	source Source,
	tables map[string]*healthTableState,
) ([]Issue, error) {
	nodes, err := source.ListRouterNodes(ctx)
	if err != nil {
		return nil, healthError(result.CodeInternal, "list Router nodes: %v", err)
	}
	fanout, err := branchFanout(ctx, source)
	if err != nil {
		return nil, err
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
			len(children[node.ID]) >= fanout {
			ids := make([]string, 0, len(children[node.ID]))
			for _, child := range children[node.ID] {
				ids = append(ids, child.ID)
			}
			issues = append(issues, nodeIssue(state, node, KindRouteCapacity,
				"review_route_split", ids, "", len(ids)))
		}
	}
	issues = append(issues, siblingAmbiguityIssues(nodes, children, tables)...)

	// What a leaf holds is a field on the leaf. There is no locator set to page
	// through and no scan budget to run out of, so the three problems that
	// budget used to surface — two Rows in one leaf, a locator whose scope
	// disagrees with its leaf, a locator revision that fell behind the Row —
	// have no state left to be in. See docs/storage/leaf-rowid-v1.md §5.
	for _, node := range nodes {
		if node.Kind != router.KindLeaf || node.RowID == "" {
			continue
		}
		state := tables[node.TableID]
		if state == nil {
			continue
		}
		if _, found := state.rows[node.RowID]; !found {
			if state.rowsComplete {
				issues = append(issues, nodeIssue(state, node, KindOrphanMembership,
					"review_membership", []string{node.ID, node.RowID}, node.RowID, 1))
			}
			continue
		}
		state.routedRows[node.RowID] = true
	}
	for _, state := range tables {
		if !state.rowsComplete {
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
	return issues, nil
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
