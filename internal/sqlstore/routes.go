package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/change"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/row"
)

// routeNode is one row of a table's route table
// (docs/product/route-companion-table.md). Up is parent_id, down is child_ids;
// neither direction needs an index. The path is never stored.
type routeNode struct {
	router.Node
	ChildIDs     []string `json:"child_ids"`
	Deprecated   bool     `json:"deprecated"`
	SuccessorIDs []string `json:"successor_ids"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
}

const maxSuccessorHops = 16

func (t *tx) routeTableOf(ctx context.Context, routeID string) (string, error) {
	var tableID string
	err := t.q().QueryRowContext(ctx, `SELECT table_id FROM mem_route_index WHERE route_id = ?`, routeID).Scan(&tableID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fail(result.CodeNotFound, "route %q was not found", routeID)
	}
	return tableID, err
}

func (t *tx) tableRouteNodes(ctx context.Context, tableID string) ([]routeNode, error) {
	rows, err := t.q().QueryContext(ctx, `SELECT body FROM `+routeTable(tableID)+` WHERE deprecated = 0`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	nodes := []routeNode{}
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		var node routeNode
		if err := decodeJSON(body, &node); err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	sort.Slice(nodes, func(left, right int) bool { return nodes[left].ID < nodes[right].ID })
	return nodes, rows.Err()
}

func (t *tx) readRoute(ctx context.Context, tableID, routeID string) (routeNode, error) {
	var body string
	err := t.q().QueryRowContext(ctx, `SELECT body FROM `+routeTable(tableID)+` WHERE route_id = ?`, routeID).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return routeNode{}, fail(result.CodeNotFound, "route %q was not found", routeID)
	}
	if err != nil {
		return routeNode{}, err
	}
	var node routeNode
	if err := decodeJSON(body, &node); err != nil {
		return routeNode{}, err
	}
	node.Deleted = node.Deprecated
	if node.Aliases == nil {
		node.Aliases = []string{}
	}
	return node, nil
}

// findRoute resolves any route ID, following successors when the node was
// deprecated by a restructure.
func (t *tx) findRoute(ctx context.Context, routeID string) (routeNode, error) {
	tableID, err := t.routeTableOf(ctx, routeID)
	if err != nil {
		return routeNode{}, err
	}
	return t.readRoute(ctx, tableID, routeID)
}

func (t *tx) saveRoute(ctx context.Context, table catalog.Table, node routeNode, operation change.Operation) error {
	node.Revision++
	node.UpdatedAt = formatTime(t.now)
	if node.CreatedAt == "" {
		node.CreatedAt = node.UpdatedAt
	}
	if node.ChildIDs == nil {
		node.ChildIDs = []string{}
	}
	if node.SuccessorIDs == nil {
		node.SuccessorIDs = []string{}
	}
	node.Path = ""
	node.Deleted = node.Deprecated
	var parent any
	if node.ParentID != "" {
		parent = node.ParentID
	}
	deprecated := 0
	if node.Deprecated {
		deprecated = 1
	}
	if _, err := t.q().ExecContext(ctx, `INSERT INTO `+routeTable(table.ID)+`(route_id, parent_id, kind, deprecated, body) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(route_id) DO UPDATE SET parent_id = excluded.parent_id, deprecated = excluded.deprecated, body = excluded.body`,
		node.ID, parent, string(node.Kind), deprecated, encodeJSON(node)); err != nil {
		return err
	}
	if node.Revision == 1 {
		if _, err := t.q().ExecContext(ctx, `INSERT INTO mem_route_index(route_id, table_id) VALUES (?, ?)`, node.ID, table.ID); err != nil {
			return err
		}
	}
	t.recordEntry(change.Entry{
		ObjectKind: change.ObjectRouteNode, DatabaseID: table.DatabaseID, TableID: table.ID, ObjectID: node.ID,
		Operation: operation, BeforeRevision: node.Revision - 1, AfterRevision: node.Revision,
	}, change.Metadata{Actor: "system:router", Source: "msql", Reason: "Route mutation"})
	return nil
}

// withPath fills the computed path by walking parent_id to the root.
func (t *tx) withPath(ctx context.Context, node routeNode) (router.Node, error) {
	names := []string{}
	current := node
	for hops := 0; current.Kind != router.KindRoot; hops++ {
		if hops > 64 || current.ParentID == "" {
			return router.Node{}, fail(result.CodeInternal, "route %q has a broken parent chain", node.ID)
		}
		names = append(names, current.Name)
		parent, err := t.readRoute(ctx, node.TableID, current.ParentID)
		if err != nil {
			return router.Node{}, err
		}
		current = parent
	}
	for left, right := 0, len(names)-1; left < right; left, right = left+1, right-1 {
		names[left], names[right] = names[right], names[left]
	}
	value := node.Node
	value.Path = "/" + strings.Join(names, "/")
	value.Aliases = append([]string{}, node.Aliases...)
	return value, nil
}

func (t *tx) liveChildren(ctx context.Context, parent routeNode) ([]routeNode, error) {
	children := make([]routeNode, 0, len(parent.ChildIDs))
	for _, childID := range parent.ChildIDs {
		child, err := t.readRoute(ctx, parent.TableID, childID)
		if err != nil {
			return nil, err
		}
		if !child.Deprecated {
			children = append(children, child)
		}
	}
	sort.Slice(children, func(left, right int) bool {
		if children[left].Name == children[right].Name {
			return children[left].ID < children[right].ID
		}
		return children[left].Name < children[right].Name
	})
	return children, nil
}

func (t *tx) branchFanout(ctx context.Context) (int, error) {
	policy, err := t.currentRoutePolicy(ctx)
	if err != nil {
		return 0, err
	}
	return policy.Policy.BranchFanout, nil
}

// ---- executor.Rows route surface ----

func (t *tx) createTableRoot(ctx context.Context, databaseName, tableName, purpose, synopsis string) (router.Node, error) {
	table, err := t.liveTable(ctx, databaseName, tableName)
	if err != nil {
		return router.Node{}, err
	}
	if strings.TrimSpace(purpose) == "" {
		return router.Node{}, fail(result.CodeValidation, "route root requires a purpose")
	}
	var existing sql.NullString
	if err := t.q().QueryRowContext(ctx, `SELECT router_root_id FROM mem_tables WHERE id = ?`, table.ID).Scan(&existing); err != nil {
		return router.Node{}, err
	}
	if existing.Valid && existing.String != "" {
		return router.Node{}, fail(result.CodeAlreadyExists, "table %q already has a route root", table.Name)
	}
	node := routeNode{Node: router.Node{
		Version: router.Version, ID: newID("route_"), DatabaseID: table.DatabaseID, TableID: table.ID,
		Name: "root", Aliases: []string{}, Kind: router.KindRoot, Purpose: purpose, Synopsis: synopsis,
	}}
	if err := t.saveRoute(ctx, table, node, change.OperationInsert); err != nil {
		return router.Node{}, err
	}
	if _, err := t.q().ExecContext(ctx, `UPDATE mem_tables SET router_root_id = ? WHERE id = ?`, node.ID, table.ID); err != nil {
		return router.Node{}, err
	}
	node.Revision = 1
	return t.withPath(ctx, node)
}

func (t *tx) tableRootChildren(ctx context.Context, databaseID, tableID, cursor string, limit int) ([]router.Node, router.ReadPage, error) {
	var root sql.NullString
	err := t.q().QueryRowContext(ctx, `SELECT router_root_id FROM mem_tables WHERE id = ? AND database_id = ?`, tableID, databaseID).Scan(&root)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, router.ReadPage{}, fail(result.CodeNotFound, "table %q was not found", tableID)
	}
	if err != nil {
		return nil, router.ReadPage{}, err
	}
	if !root.Valid || root.String == "" {
		return router.PaginateNodes("table-root:"+databaseID+":"+tableID, cursor, limit, []router.Node{})
	}
	return t.childrenPage(ctx, root.String, cursor, limit)
}

func (t *tx) childrenPage(ctx context.Context, parentID, cursor string, limit int) ([]router.Node, router.ReadPage, error) {
	parent, err := t.findRoute(ctx, parentID)
	if err != nil {
		return nil, router.ReadPage{}, err
	}
	if parent.Deprecated || parent.Kind == router.KindLeaf {
		return nil, router.ReadPage{}, fail(result.CodeValidation, "children require a live root or branch")
	}
	children, err := t.liveChildren(ctx, parent)
	if err != nil {
		return nil, router.ReadPage{}, err
	}
	nodes := make([]router.Node, 0, len(children))
	for _, child := range children {
		value, err := t.withPath(ctx, child)
		if err != nil {
			return nil, router.ReadPage{}, err
		}
		nodes = append(nodes, value)
	}
	return router.PaginateNodes("parent:"+parentID, cursor, limit, nodes)
}

func (t *tx) createNode(ctx context.Context, parentID string, definition router.NodeDefinition) (router.Node, error) {
	parent, err := t.findRoute(ctx, parentID)
	if err != nil {
		return router.Node{}, err
	}
	name := strings.TrimSpace(definition.Name)
	if name == "" || strings.Contains(name, "/") || strings.TrimSpace(definition.Purpose) == "" ||
		parent.Deprecated || parent.Kind == router.KindLeaf ||
		(definition.Kind != router.KindBranch && definition.Kind != router.KindLeaf) {
		return router.Node{}, fail(result.CodeValidation, "invalid route child definition")
	}
	table, err := t.tableByID(ctx, parent.TableID)
	if err != nil {
		return router.Node{}, err
	}
	children, err := t.liveChildren(ctx, parent)
	if err != nil {
		return router.Node{}, err
	}
	for _, sibling := range children {
		if strings.EqualFold(sibling.Name, name) {
			return router.Node{}, fail(result.CodeAlreadyExists, "route %q already has a child named %q", parent.ID, name)
		}
	}
	fanout, err := t.branchFanout(ctx)
	if err != nil {
		return router.Node{}, err
	}
	if err := router.CheckBranchFanout(parent.ID, len(children), 1, fanout); err != nil {
		return router.Node{}, err
	}
	node := routeNode{Node: router.Node{
		Version: router.Version, ID: newID("route_"), DatabaseID: parent.DatabaseID, TableID: parent.TableID,
		ParentID: parent.ID, Name: name, Aliases: []string{}, Kind: definition.Kind,
		Purpose: definition.Purpose, Synopsis: definition.Synopsis,
	}}
	if err := t.saveRoute(ctx, table, node, change.OperationInsert); err != nil {
		return router.Node{}, err
	}
	parent.ChildIDs = append(parent.ChildIDs, node.ID)
	if err := t.saveRoute(ctx, table, parent, change.OperationUpdate); err != nil {
		return router.Node{}, err
	}
	node.Revision = 1
	return t.withPath(ctx, node)
}

func (t *tx) liveRouteForWrite(ctx context.Context, routeID string, expected uint64) (catalog.Table, routeNode, error) {
	node, err := t.findRoute(ctx, routeID)
	if err != nil {
		return catalog.Table{}, routeNode{}, err
	}
	if node.Deprecated {
		return catalog.Table{}, routeNode{}, fail(result.CodeNotFound, "route %q was deprecated", routeID)
	}
	if expected != 0 && expected != node.Revision {
		return catalog.Table{}, routeNode{}, fail(result.CodeRevisionConflict, "route %q is at revision %d, not %d", routeID, node.Revision, expected)
	}
	table, err := t.tableByID(ctx, node.TableID)
	return table, node, err
}

func (t *tx) renameNode(ctx context.Context, routeID, name string, expected uint64) (router.Node, error) {
	table, node, err := t.liveRouteForWrite(ctx, routeID, expected)
	if err != nil {
		return router.Node{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" || strings.Contains(name, "/") || node.Kind == router.KindRoot {
		return router.Node{}, fail(result.CodeValidation, "invalid route name")
	}
	if node.ParentID != "" {
		parent, err := t.readRoute(ctx, table.ID, node.ParentID)
		if err != nil {
			return router.Node{}, err
		}
		siblings, err := t.liveChildren(ctx, parent)
		if err != nil {
			return router.Node{}, err
		}
		for _, sibling := range siblings {
			if sibling.ID != node.ID && strings.EqualFold(sibling.Name, name) {
				return router.Node{}, fail(result.CodeAlreadyExists, "a sibling is already named %q", name)
			}
		}
	}
	aliases, err := router.AliasesAfterRename(node.Aliases, node.Name, name)
	if err != nil {
		return router.Node{}, err
	}
	node.Name, node.Aliases = name, aliases
	if err := t.saveRoute(ctx, table, node, change.OperationUpdate); err != nil {
		return router.Node{}, err
	}
	node.Revision++
	return t.withPath(ctx, node)
}

// deprecateNode retires a node. Children must be moved or retired first, so the
// tree never has a live node under a deprecated parent.
func (t *tx) deprecateNode(ctx context.Context, routeID string, expected uint64) (uint64, error) {
	table, node, err := t.liveRouteForWrite(ctx, routeID, expected)
	if err != nil {
		return 0, err
	}
	children, err := t.liveChildren(ctx, node)
	if err != nil {
		return 0, err
	}
	if len(children) > 0 {
		return 0, fail(result.CodeConstraint, "route %q still has %d live children", routeID, len(children))
	}
	if node.ParentID != "" {
		parent, err := t.readRoute(ctx, table.ID, node.ParentID)
		if err != nil {
			return 0, err
		}
		parent.ChildIDs = without(parent.ChildIDs, node.ID)
		if err := t.saveRoute(ctx, table, parent, change.OperationUpdate); err != nil {
			return 0, err
		}
	} else {
		if _, err := t.q().ExecContext(ctx, `UPDATE mem_tables SET router_root_id = NULL WHERE id = ?`, table.ID); err != nil {
			return 0, err
		}
	}
	if node.RowID != "" {
		if err := t.unmountRow(ctx, table, node.RowID, node.ID); err != nil {
			return 0, err
		}
	}
	node.Deprecated = true
	if err := t.saveRoute(ctx, table, node, change.OperationDelete); err != nil {
		return 0, err
	}
	return node.Revision + 1, nil
}

func (t *tx) unmountRow(ctx context.Context, table catalog.Table, rowID, leafID string) error {
	value, err := t.readRow(ctx, table, rowID)
	if err != nil {
		return nil
	}
	remaining := without(value.RouteLeafIDs, leafID)
	if len(remaining) == len(value.RouteLeafIDs) {
		return nil
	}
	value.RouteLeafIDs = remaining
	_, err = t.q().ExecContext(ctx, `UPDATE `+dataTable(table.ID)+` SET route_leaf_ids = ? WHERE row_id = ?`,
		encodeJSON(remaining), rowID)
	return err
}

// unmountLeaf frees a leaf whose Row has moved on. The Row is the authority on
// which leaf it hangs under, so leaving the leaf's row_id behind would make the
// two directions disagree — and would keep the leaf from ever taking another
// Row. A leaf with no Row is a normal empty leaf, not a node to prune.
func (t *tx) unmountLeaf(ctx context.Context, table catalog.Table, rowID, leafID string) error {
	node, err := t.readRoute(ctx, table.ID, leafID)
	if err != nil {
		return err
	}
	if node.RowID != rowID {
		return nil
	}
	node.RowID = ""
	return t.saveRoute(ctx, table, node, change.OperationUpdate)
}

func without(values []string, drop string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != drop {
			out = append(out, value)
		}
	}
	return out
}

func (t *tx) getNode(ctx context.Context, routeID string) (router.Node, error) {
	node, err := t.findRoute(ctx, routeID)
	if err != nil {
		return router.Node{}, err
	}
	for hops := 0; node.Deprecated && len(node.SuccessorIDs) == 1; hops++ {
		if hops >= maxSuccessorHops {
			return router.Node{}, fail(result.CodeInternal, "route %q successor chain is too long", routeID)
		}
		next, err := t.readRoute(ctx, node.TableID, node.SuccessorIDs[0])
		if err != nil {
			return router.Node{}, err
		}
		node = next
	}
	if node.Deprecated {
		value := node.Node
		value.Deleted = true
		return value, nil
	}
	return t.withPath(ctx, node)
}

func (t *tx) leafPage(ctx context.Context, leafID, cursor string, limit int) ([]router.Locator, router.ReadPage, error) {
	node, err := t.findRoute(ctx, leafID)
	if err != nil {
		return nil, router.ReadPage{}, err
	}
	if node.Kind != router.KindLeaf || node.Deprecated {
		return nil, router.ReadPage{}, fail(result.CodeValidation, "OPEN requires a live leaf")
	}
	locators := []router.Locator{}
	if node.RowID != "" {
		table, err := t.tableByID(ctx, node.TableID)
		if err != nil {
			return nil, router.ReadPage{}, err
		}
		value, err := t.readRow(ctx, table, node.RowID)
		if err == nil && value.State == row.StateLive {
			locators = append(locators, router.Locator{
				DatabaseID: table.DatabaseID, TableID: table.ID, RowID: value.ID, Revision: value.Revision,
			})
		}
	}
	return router.PaginateLocators("leaf:"+node.ID, cursor, limit, locators)
}

func (t *tx) updateSynopsis(ctx context.Context, routeID, synopsis string, expected uint64) (router.Node, error) {
	table, node, err := t.liveRouteForWrite(ctx, routeID, expected)
	if err != nil {
		return router.Node{}, err
	}
	node.Synopsis = synopsis
	if err := t.saveRoute(ctx, table, node, change.OperationUpdate); err != nil {
		return router.Node{}, err
	}
	node.Revision++
	return t.withPath(ctx, node)
}

func (t *tx) updateAliases(ctx context.Context, routeID string, aliases []string, expected uint64) (router.Node, error) {
	table, node, err := t.liveRouteForWrite(ctx, routeID, expected)
	if err != nil {
		return router.Node{}, err
	}
	normalized, err := router.NormalizeAliases(node.Name, aliases)
	if err != nil {
		return router.Node{}, err
	}
	node.Aliases = normalized
	if err := t.saveRoute(ctx, table, node, change.OperationUpdate); err != nil {
		return router.Node{}, err
	}
	node.Revision++
	return t.withPath(ctx, node)
}

// allNodes lists every live node. Mutation Plan and scans need the whole tree;
// the tree is metadata-sized.
func (t *tx) allNodes(ctx context.Context) ([]router.Node, error) {
	rows, err := t.q().QueryContext(ctx, `SELECT id FROM mem_tables WHERE role = 'data'`)
	if err != nil {
		return nil, err
	}
	tableIDs := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		tableIDs = append(tableIDs, id)
	}
	_ = rows.Close()
	nodes := []router.Node{}
	for _, tableID := range tableIDs {
		routeRows, err := t.q().QueryContext(ctx, `SELECT body FROM `+routeTable(tableID)+` WHERE deprecated = 0`)
		if err != nil {
			return nil, err
		}
		stored := []routeNode{}
		for routeRows.Next() {
			var body string
			if err := routeRows.Scan(&body); err != nil {
				_ = routeRows.Close()
				return nil, err
			}
			var node routeNode
			if err := decodeJSON(body, &node); err != nil {
				_ = routeRows.Close()
				return nil, err
			}
			stored = append(stored, node)
		}
		_ = routeRows.Close()
		for _, node := range stored {
			value, err := t.withPath(ctx, node)
			if err != nil {
				return nil, err
			}
			nodes = append(nodes, value)
		}
	}
	sort.Slice(nodes, func(left, right int) bool { return nodes[left].ID < nodes[right].ID })
	return nodes, nil
}
