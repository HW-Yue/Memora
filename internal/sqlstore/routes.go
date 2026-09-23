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
	if err := router.CheckPurpose("root", purpose); err != nil {
		return router.Node{}, err
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

func (t *tx) tableRootChildren(ctx context.Context, databaseID, tableID string) ([]router.Node, router.ReadPage, error) {
	var root sql.NullString
	err := t.q().QueryRowContext(ctx, `SELECT router_root_id FROM mem_tables WHERE id = ? AND database_id = ?`, tableID, databaseID).Scan(&root)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, router.ReadPage{}, fail(result.CodeNotFound, "table %q was not found", tableID)
	}
	if err != nil {
		return nil, router.ReadPage{}, err
	}
	if !root.Valid || root.String == "" {
		return router.CompleteNodes("table-root:"+databaseID+":"+tableID, []router.Node{})
	}
	return t.children(ctx, root.String)
}

func (t *tx) children(ctx context.Context, parentID string) ([]router.Node, router.ReadPage, error) {
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
	return router.CompleteNodes("parent:"+parentID, nodes)
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
	// Every new Route passes here — CREATE ROUTE UNDER and each segment an
	// implicit path completes — so the rule is checked once. An existing Route
	// never reaches this point, which is what keeps a library whose purposes
	// still repeat their names writable.
	if err := router.CheckPurpose(name, definition.Purpose); err != nil {
		return router.Node{}, err
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

// ensureRoutePath resolves an implicit path under the Table's root, creating
// the segments that are missing, and returns the leaf it ends at. The Agent
// named every segment and gave its purpose, so completing the path is this
// write's consequence rather than the engine inventing semantics. The rules are
// in docs/query/implicit-route-path-v1.md.
//
// Resolution and creation share one name rule (trimmed, exact, sibling-unique
// case-insensitively) because createNode enforces it: resolving more strictly
// than the engine creates would dead-end on a name it already holds.
func (t *tx) ensureRoutePath(ctx context.Context, table catalog.Table, segments []router.PathSegment) (string, error) {
	if len(segments) == 0 {
		return "", fail(result.CodeValidation, "an implicit route path needs at least one segment")
	}
	var root sql.NullString
	if err := t.q().QueryRowContext(ctx, `SELECT router_root_id FROM mem_tables WHERE id = ?`, table.ID).Scan(&root); err != nil {
		return "", err
	}
	if !root.Valid || root.String == "" {
		return "", fail(result.CodeValidation,
			"table %q has no route root yet: create it explicitly, its purpose is table-level semantics", table.Name)
	}
	parent, err := t.readRoute(ctx, table.ID, root.String)
	if err != nil {
		return "", err
	}
	for index, segment := range segments {
		name := strings.TrimSpace(segment.Name)
		purpose := strings.TrimSpace(segment.Purpose)
		last := index == len(segments)-1
		if name == "" || strings.Contains(name, "/") || purpose == "" ||
			(segment.Kind != router.KindBranch && segment.Kind != router.KindLeaf) {
			return "", fail(result.CodeValidation, "invalid route path segment %q", segment.Name)
		}
		if last && segment.Kind != router.KindLeaf {
			return "", fail(result.CodeValidation,
				"the last segment of a route path must be a leaf, not a %s", segment.Kind)
		}
		if !last && segment.Kind == router.KindLeaf {
			return "", fail(result.CodeConstraint,
				"route path segment %q is a leaf, so nothing can hang below it", name)
		}
		children, err := t.liveChildren(ctx, parent)
		if err != nil {
			return "", err
		}
		found := -1
		for index := range children {
			if strings.EqualFold(children[index].Name, name) {
				found = index
				break
			}
		}
		if found < 0 {
			created, err := t.createNode(ctx, parent.ID, router.NodeDefinition{
				Name: name, Kind: segment.Kind, Purpose: purpose,
			})
			if err != nil {
				return "", err
			}
			if last {
				return created.ID, nil
			}
			if parent, err = t.readRoute(ctx, table.ID, created.ID); err != nil {
				return "", err
			}
			continue
		}
		child := children[found]
		if child.Kind != segment.Kind {
			return "", fail(result.CodeConstraint,
				"route %q already exists as a %s on this path, not a %s", name, child.Kind, segment.Kind)
		}
		if last {
			if child.Purpose != purpose {
				return "", fail(result.CodeConstraint,
					"route leaf %q already exists with purpose %q: mount on its id, or change its purpose explicitly",
					name, child.Purpose)
			}
			return child.ID, nil
		}
		parent = child
	}
	return "", fail(result.CodeInternal, "route path resolution ended without a leaf")
}

// pruneEmptyBranches removes branches that no longer carry a live child, and
// keeps going while each removal empties its own parent. See
// docs/product/route-companion-table.md「致空场景清单」.
//
// Two things are deliberately never pruned. A leaf, because an empty leaf is
// where the next Row is mounted — it is the resource the mount refusal points
// at, not a shell. And a retired node, because it exists so that a route_id
// still held outside the tree can be followed to its successors.
func (t *tx) pruneEmptyBranches(ctx context.Context, table catalog.Table) error {
	for {
		nodes, err := t.tableRouteNodes(ctx, table.ID)
		if err != nil {
			return err
		}
		victim := ""
		for _, node := range nodes {
			if node.Deprecated || node.Kind == router.KindLeaf {
				continue
			}
			children, err := t.liveChildren(ctx, node)
			if err != nil {
				return err
			}
			if len(children) == 0 {
				victim = node.ID
				break
			}
		}
		if victim == "" {
			return nil
		}
		if err := t.removeNode(ctx, table, victim); err != nil {
			return err
		}
	}
}

// removeNode drops one node and its index entry. Detaching an empty root leaves
// the Table with no router at all, which is the legal "no semantic tree yet"
// state: SHOW ROUTES AT ROOT answers with an empty page.
func (t *tx) removeNode(ctx context.Context, table catalog.Table, routeID string) error {
	node, err := t.readRoute(ctx, table.ID, routeID)
	if err != nil {
		return err
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
	} else if _, err := t.q().ExecContext(ctx, `UPDATE mem_tables SET router_root_id = NULL WHERE id = ?`, table.ID); err != nil {
		return err
	}
	if _, err := t.q().ExecContext(ctx, `DELETE FROM `+routeTable(table.ID)+` WHERE route_id = ?`, routeID); err != nil {
		return err
	}
	_, err = t.q().ExecContext(ctx, `DELETE FROM mem_route_index WHERE route_id = ?`, routeID)
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

// updatePurpose rewrites what a Route says it holds. The same rule a new Route
// passes applies here — a purpose that only repeats the name is refused — so
// the two write surfaces cannot drift apart: an amendment that could launder a
// name through a space or a full-width letter would make the create-side
// refusal decorative. It is judged against the Route's current name, read in
// this transaction, because that is the only name the purpose is compared to.
//
// Nothing downstream is rebuilt: a Route's purpose and aliases do not enter the
// recall index, which is built from the Row's own title and summary, and the
// walk reads the purpose live off the node.
func (t *tx) updatePurpose(ctx context.Context, routeID, purpose string, expected uint64) (router.Node, error) {
	table, node, err := t.liveRouteForWrite(ctx, routeID, expected)
	if err != nil {
		return router.Node{}, err
	}
	if strings.TrimSpace(purpose) == "" {
		return router.Node{}, fail(result.CodeValidation, "route %q requires a purpose", routeID)
	}
	if err := router.CheckPurpose(node.Name, purpose); err != nil {
		return router.Node{}, err
	}
	node.Purpose = purpose
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
