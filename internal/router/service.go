package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/HW-Yue/Memora/internal/logical"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/store"
	"github.com/google/uuid"
)

const (
	nodeBucket      = "router_nodes"
	pathBucket      = "router_paths"
	childrenBucket  = "router_children"
	leafBucket      = "router_leaf_members"
	reverseBucket   = "router_row_memberships"
	defaultChildren = 12
	defaultLeafRows = 100
	maxPageSize     = 100
	maxNameRunes    = 64
	maxPurposeRunes = 800
)

type IDSource interface {
	Next() (string, error)
}

type Options struct {
	IDs         IDSource
	MaxChildren int
	MaxLeafRows int
}

type Service struct {
	store       store.Store
	ids         IDSource
	maxChildren int
	maxLeafRows int
	mu          sync.Mutex
}

type Error struct {
	Code    result.Code
	Message string
}

func (err *Error) Error() string      { return err.Message }
func (err *Error) StableCode() string { return string(err.Code) }

func New(database store.Store, options Options) *Service {
	if options.IDs == nil {
		options.IDs = uuidSource{}
	}
	if options.MaxChildren == 0 {
		options.MaxChildren = defaultChildren
	}
	if options.MaxLeafRows == 0 {
		options.MaxLeafRows = defaultLeafRows
	}
	if options.MaxChildren < 1 {
		options.MaxChildren = 1
	}
	if options.MaxLeafRows < 1 {
		options.MaxLeafRows = 1
	}
	return &Service{
		store: database, ids: options.IDs,
		maxChildren: options.MaxChildren, maxLeafRows: options.MaxLeafRows,
	}
}

func (service *Service) CreateRootIn(
	ctx context.Context,
	tx store.Tx,
	databaseID, purpose string,
) (Node, error) {
	if strings.TrimSpace(databaseID) == "" {
		return Node{}, routerError(result.CodeValidation, "Router root requires database ID")
	}
	if err := validatePurpose(purpose); err != nil {
		return Node{}, err
	}
	if _, err := resolvePathIn(ctx, tx, databaseID, "/"); err == nil {
		return Node{}, routerError(result.CodeAlreadyExists, "Router root already exists")
	} else if !hasCode(err, result.CodeNotFound) {
		return Node{}, err
	}
	id, err := service.nextID()
	if err != nil {
		return Node{}, err
	}
	root := Node{
		Version: Version, ID: id, DatabaseID: databaseID, Name: "",
		Aliases: []string{}, Path: "/", Kind: KindRoot,
		Purpose: strings.TrimSpace(purpose), Revision: 1,
	}
	if err := putNode(ctx, tx, root); err != nil {
		return Node{}, err
	}
	if err := putPath(ctx, tx, root.DatabaseID, root.Path, root.ID); err != nil {
		return Node{}, err
	}
	return root, nil
}

func (service *Service) CreateNodeIn(
	ctx context.Context,
	tx store.Tx,
	parentID string,
	definition NodeDefinition,
) (Node, error) {
	if err := validateDefinition(definition); err != nil {
		return Node{}, err
	}
	parent, err := getNode(ctx, tx, parentID)
	if err != nil {
		return Node{}, err
	}
	if parent.Kind == KindLeaf {
		return Node{}, routerError(result.CodeConstraint, "Router leaf cannot contain child nodes")
	}
	children, err := loadStrings(ctx, tx, childrenBucket, parent.ID)
	if err != nil {
		return Node{}, err
	}
	if len(children) >= service.maxChildren {
		return Node{}, routerError(result.CodeConstraint, "Router child capacity exceeded; semantic split is required")
	}
	path := joinPath(parent.Path, definition.Name)
	if _, err := resolvePathIn(ctx, tx, parent.DatabaseID, path); err == nil {
		return Node{}, routerError(result.CodeAlreadyExists, "Router path already exists")
	} else if !hasCode(err, result.CodeNotFound) {
		return Node{}, err
	}
	id, err := service.nextID()
	if err != nil {
		return Node{}, err
	}
	node := Node{
		Version: Version, ID: id, DatabaseID: parent.DatabaseID,
		ParentID: parent.ID, Name: definition.Name, Aliases: []string{},
		Path: path, Kind: definition.Kind,
		Purpose:  strings.TrimSpace(definition.Purpose),
		Synopsis: strings.TrimSpace(definition.Synopsis), Revision: 1,
	}
	if err := putNode(ctx, tx, node); err != nil {
		return Node{}, err
	}
	if err := putPath(ctx, tx, node.DatabaseID, node.Path, node.ID); err != nil {
		return Node{}, err
	}
	children = append(children, node.ID)
	return node, saveStrings(ctx, tx, childrenBucket, parent.ID, children)
}

func (service *Service) RenameIn(
	ctx context.Context,
	tx store.Tx,
	nodeID, newName string,
	expectedRevision uint64,
) (Node, error) {
	if err := validateName(newName); err != nil {
		return Node{}, err
	}
	node, err := getNode(ctx, tx, nodeID)
	if err != nil {
		return Node{}, err
	}
	if node.Kind == KindRoot {
		return Node{}, routerError(result.CodeConstraint, "Router root cannot be renamed")
	}
	if expectedRevision == 0 || node.Revision != expectedRevision {
		return Node{}, routerError(result.CodeRevisionConflict, "Router node revision conflict")
	}
	parent, err := getNode(ctx, tx, node.ParentID)
	if err != nil {
		return Node{}, err
	}
	newPath := joinPath(parent.Path, newName)
	if existing, err := resolvePathIn(ctx, tx, node.DatabaseID, newPath); err == nil && existing.ID != node.ID {
		return Node{}, routerError(result.CodeAlreadyExists, "Router path already exists")
	} else if err != nil && !hasCode(err, result.CodeNotFound) {
		return Node{}, err
	}
	oldPath := node.Path
	node.Aliases = appendUnique(node.Aliases, node.Name)
	node.Name = newName
	node.Path = newPath
	node.Revision++
	if err := putNode(ctx, tx, node); err != nil {
		return Node{}, err
	}
	if err := putPath(ctx, tx, node.DatabaseID, newPath, node.ID); err != nil {
		return Node{}, err
	}
	if err := service.repathDescendants(ctx, tx, node, oldPath); err != nil {
		return Node{}, err
	}
	return node, nil
}

func (service *Service) repathDescendants(
	ctx context.Context,
	tx store.Tx,
	parent Node,
	oldParentPath string,
) error {
	children, err := loadStrings(ctx, tx, childrenBucket, parent.ID)
	if err != nil {
		return err
	}
	for _, childID := range children {
		child, err := getNode(ctx, tx, childID)
		if err != nil {
			return err
		}
		oldPath := child.Path
		child.Path = strings.Replace(child.Path, oldParentPath+"/", parent.Path+"/", 1)
		if err := putNode(ctx, tx, child); err != nil {
			return err
		}
		if err := putPath(ctx, tx, child.DatabaseID, child.Path, child.ID); err != nil {
			return err
		}
		if err := service.repathDescendants(ctx, tx, child, oldPath); err != nil {
			return err
		}
	}
	return nil
}

func (service *Service) ReplaceMembershipsIn(
	ctx context.Context,
	tx store.Tx,
	locator Locator,
	leafIDs []string,
) ([]Membership, error) {
	if err := validateLocator(locator); err != nil {
		return nil, err
	}
	leafIDs = uniqueSorted(leafIDs)
	for _, leafID := range leafIDs {
		leaf, err := getNode(ctx, tx, leafID)
		if err != nil {
			return nil, err
		}
		if leaf.Kind != KindLeaf || leaf.DatabaseID != locator.DatabaseID {
			return nil, routerError(result.CodeConstraint, "Router membership target must be a leaf in the same Database")
		}
	}
	reverseKey := locatorKey(locator)
	oldLeafIDs, err := loadStrings(ctx, tx, reverseBucket, reverseKey)
	if err != nil {
		return nil, err
	}
	if len(oldLeafIDs) > 0 {
		oldLocators, err := service.locatorFromReverse(ctx, tx, oldLeafIDs[0], locator)
		if err != nil {
			return nil, err
		}
		if oldLocators.Revision > locator.Revision {
			return nil, routerError(result.CodeRevisionConflict, "Router membership revision is stale")
		}
	}
	oldLeaves := make(map[string]struct{}, len(oldLeafIDs))
	for _, leafID := range oldLeafIDs {
		oldLeaves[leafID] = struct{}{}
	}
	for _, leafID := range leafIDs {
		locators, err := loadLocators(ctx, tx, leafID)
		if err != nil {
			return nil, err
		}
		_, replacingExisting := oldLeaves[leafID]
		if len(locators) >= service.maxLeafRows && !replacingExisting {
			return nil, routerError(result.CodeConstraint, "Router leaf capacity exceeded; semantic split is required")
		}
	}
	for _, leafID := range oldLeafIDs {
		if err := removeLeafLocator(ctx, tx, leafID, locator); err != nil {
			return nil, err
		}
	}
	for _, leafID := range leafIDs {
		locators, err := loadLocators(ctx, tx, leafID)
		if err != nil {
			return nil, err
		}
		locators = append(locators, locator)
		if err := saveLocators(ctx, tx, leafID, locators); err != nil {
			return nil, err
		}
	}
	if err := saveStrings(ctx, tx, reverseBucket, reverseKey, leafIDs); err != nil {
		return nil, err
	}
	return memberships(locator, leafIDs), nil
}

func (service *Service) locatorFromReverse(
	ctx context.Context,
	tx store.Tx,
	leafID string,
	locator Locator,
) (Locator, error) {
	values, err := loadLocators(ctx, tx, leafID)
	if err != nil {
		return Locator{}, err
	}
	for _, value := range values {
		if sameRow(value, locator) {
			return value, nil
		}
	}
	return Locator{}, routerError(result.CodeInternal, "Router reverse membership is corrupt")
}

func (service *Service) DeleteNodeIn(
	ctx context.Context,
	tx store.Tx,
	nodeID string,
	expectedRevision uint64,
) error {
	node, err := getNode(ctx, tx, nodeID)
	if err != nil {
		return err
	}
	if node.Kind == KindRoot {
		return routerError(result.CodeConstraint, "Router root cannot be deleted")
	}
	if expectedRevision == 0 || node.Revision != expectedRevision {
		return routerError(result.CodeRevisionConflict, "Router node revision conflict")
	}
	return service.deleteSubtree(ctx, tx, node)
}

func (service *Service) deleteSubtree(ctx context.Context, tx store.Tx, node Node) error {
	children, err := loadStrings(ctx, tx, childrenBucket, node.ID)
	if err != nil {
		return err
	}
	for _, childID := range children {
		child, err := getNode(ctx, tx, childID)
		if err != nil {
			return err
		}
		if err := service.deleteSubtree(ctx, tx, child); err != nil {
			return err
		}
	}
	if node.Kind == KindLeaf {
		locators, err := loadLocators(ctx, tx, node.ID)
		if err != nil {
			return err
		}
		for _, locator := range locators {
			key := locatorKey(locator)
			leafIDs, err := loadStrings(ctx, tx, reverseBucket, key)
			if err != nil {
				return err
			}
			leafIDs = removeString(leafIDs, node.ID)
			if err := saveStrings(ctx, tx, reverseBucket, key, leafIDs); err != nil {
				return err
			}
		}
		if err := saveLocators(ctx, tx, node.ID, []Locator{}); err != nil {
			return err
		}
	}
	node.Deleted = true
	node.Revision++
	if err := putNode(ctx, tx, node); err != nil {
		return err
	}
	if node.ParentID != "" {
		siblings, err := loadStrings(ctx, tx, childrenBucket, node.ParentID)
		if err != nil {
			return err
		}
		if err := saveStrings(ctx, tx, childrenBucket, node.ParentID, removeString(siblings, node.ID)); err != nil {
			return err
		}
	}
	return nil
}

func (service *Service) Get(ctx context.Context, nodeID string) (Node, error) {
	tx, err := service.store.Begin(ctx, store.ReadOnly)
	if err != nil {
		return Node{}, stableError(err)
	}
	defer func() { _ = tx.Rollback() }()
	return service.GetIn(ctx, tx, nodeID)
}

func (service *Service) GetIn(ctx context.Context, tx store.Tx, nodeID string) (Node, error) {
	return getNode(ctx, tx, nodeID)
}

func (service *Service) ResolvePath(ctx context.Context, databaseID, path string) (Node, error) {
	tx, err := service.store.Begin(ctx, store.ReadOnly)
	if err != nil {
		return Node{}, stableError(err)
	}
	defer func() { _ = tx.Rollback() }()
	return service.ResolvePathIn(ctx, tx, databaseID, path)
}

func (service *Service) ResolvePathIn(
	ctx context.Context,
	tx store.Tx,
	databaseID, path string,
) (Node, error) {
	return resolvePathIn(ctx, tx, databaseID, path)
}

func (service *Service) ListLeaf(ctx context.Context, leafID string, limit int) ([]Locator, error) {
	locators, _, err := service.ListLeafPage(ctx, leafID, limit)
	return locators, err
}

func (service *Service) ListLeafPage(
	ctx context.Context,
	leafID string,
	limit int,
) ([]Locator, bool, error) {
	locators, page, err := service.ListLeafCursorPage(ctx, leafID, "", limit)
	return locators, page.NextCursor != "", err
}

func (service *Service) ListLeafCursorPage(
	ctx context.Context,
	leafID, cursor string,
	limit int,
) ([]Locator, ReadPage, error) {
	if limit < 1 || limit > maxPageSize {
		return nil, ReadPage{}, routerError(result.CodeValidation, "Router leaf limit must be between 1 and 100")
	}
	tx, err := service.store.Begin(ctx, store.ReadOnly)
	if err != nil {
		return nil, ReadPage{}, stableError(err)
	}
	defer func() { _ = tx.Rollback() }()
	return service.ListLeafCursorPageIn(ctx, tx, leafID, cursor, limit)
}

func (service *Service) ListLeafIn(
	ctx context.Context,
	tx store.Tx,
	leafID string,
	limit int,
) ([]Locator, error) {
	locators, _, err := service.ListLeafPageIn(ctx, tx, leafID, limit)
	return locators, err
}

func (service *Service) ListLeafPageIn(
	ctx context.Context,
	tx store.Tx,
	leafID string,
	limit int,
) ([]Locator, bool, error) {
	locators, page, err := service.ListLeafCursorPageIn(ctx, tx, leafID, "", limit)
	return locators, page.NextCursor != "", err
}

func (service *Service) ListLeafCursorPageIn(
	ctx context.Context,
	tx store.Tx,
	leafID, cursor string,
	limit int,
) ([]Locator, ReadPage, error) {
	if limit < 1 || limit > maxPageSize {
		return nil, ReadPage{}, routerError(result.CodeValidation, "Router leaf limit must be between 1 and 100")
	}
	node, err := getNode(ctx, tx, leafID)
	if err != nil {
		return nil, ReadPage{}, err
	}
	if node.Kind != KindLeaf {
		return nil, ReadPage{}, routerError(result.CodeConstraint, "Router node is not a leaf")
	}
	locators, err := loadLocators(ctx, tx, leafID)
	if err != nil {
		return nil, ReadPage{}, err
	}
	return PaginateLocators("leaf:"+leafID, cursor, limit, locators)
}

func (service *Service) MembershipsForRow(
	ctx context.Context,
	databaseID, tableID, rowID string,
) ([]Membership, error) {
	locator := Locator{DatabaseID: databaseID, TableID: tableID, RowID: rowID, Revision: 1}
	if err := validateLocator(locator); err != nil {
		return nil, err
	}
	tx, err := service.store.Begin(ctx, store.ReadOnly)
	if err != nil {
		return nil, stableError(err)
	}
	defer func() { _ = tx.Rollback() }()
	return service.MembershipsForRowIn(ctx, tx, databaseID, tableID, rowID)
}

func (service *Service) MembershipsForRowIn(
	ctx context.Context,
	tx store.Tx,
	databaseID, tableID, rowID string,
) ([]Membership, error) {
	locator := Locator{DatabaseID: databaseID, TableID: tableID, RowID: rowID, Revision: 1}
	if err := validateLocator(locator); err != nil {
		return nil, err
	}
	leafIDs, err := loadStrings(ctx, tx, reverseBucket, locatorKey(locator))
	if err != nil || len(leafIDs) == 0 {
		return []Membership{}, err
	}
	current, err := service.locatorFromReverse(ctx, tx, leafIDs[0], locator)
	if err != nil {
		return nil, err
	}
	return memberships(current, leafIDs), nil
}

func (service *Service) ListChildren(
	ctx context.Context,
	parentID, cursor string,
	limit int,
) ([]Node, string, error) {
	nodes, page, err := service.ListChildrenPage(ctx, parentID, cursor, limit)
	return nodes, page.NextCursor, err
}

func (service *Service) ListChildrenPage(
	ctx context.Context,
	parentID, cursor string,
	limit int,
) ([]Node, ReadPage, error) {
	tx, err := service.store.Begin(ctx, store.ReadOnly)
	if err != nil {
		return nil, ReadPage{}, stableError(err)
	}
	defer func() { _ = tx.Rollback() }()
	return service.ListChildrenPageIn(ctx, tx, parentID, cursor, limit)
}

func (service *Service) ListChildrenIn(
	ctx context.Context,
	tx store.Tx,
	parentID, cursor string,
	limit int,
) ([]Node, string, error) {
	nodes, page, err := service.ListChildrenPageIn(ctx, tx, parentID, cursor, limit)
	return nodes, page.NextCursor, err
}

func (service *Service) ListChildrenPageIn(
	ctx context.Context,
	tx store.Tx,
	parentID, cursor string,
	limit int,
) ([]Node, ReadPage, error) {
	if limit < 1 || limit > maxPageSize {
		return nil, ReadPage{}, routerError(result.CodeValidation, "Router child limit must be between 1 and 100")
	}
	parent, err := getNode(ctx, tx, parentID)
	if err != nil {
		return nil, ReadPage{}, err
	}
	if parent.Kind == KindLeaf {
		return nil, ReadPage{}, routerError(result.CodeConstraint, "Router children require a root or branch")
	}
	ids, err := loadStrings(ctx, tx, childrenBucket, parentID)
	if err != nil {
		return nil, ReadPage{}, err
	}
	nodes := make([]Node, 0, len(ids))
	for _, id := range ids {
		node, err := getNode(ctx, tx, id)
		if err != nil {
			return nil, ReadPage{}, err
		}
		nodes = append(nodes, node)
	}
	sort.Slice(nodes, func(left, right int) bool { return nodes[left].Name < nodes[right].Name })
	return PaginateNodes("parent:"+parentID, cursor, limit, nodes)
}

// ListNodes returns the current live semantic nodes in stable ID order. It is
// intentionally separate from Route memberships and never exposes Row data.
func (service *Service) ListNodes(ctx context.Context) ([]Node, error) {
	tx, err := service.store.Begin(ctx, store.ReadOnly)
	if err != nil {
		return nil, stableError(err)
	}
	defer func() { _ = tx.Rollback() }()
	return service.ListNodesIn(ctx, tx)
}

func (service *Service) ListNodesIn(ctx context.Context, tx store.Tx) ([]Node, error) {
	if service == nil || tx == nil {
		return nil, routerError(result.CodeInternal, "Router node source is unavailable")
	}
	entries, err := tx.Scan(ctx, nodeBucket)
	if err != nil {
		return nil, stableError(err)
	}
	nodes := make([]Node, 0, len(entries))
	for _, entry := range entries {
		var node Node
		if decodeJSON(entry.Value, &node) != nil || node.Version != Version ||
			node.ID != entry.Key || node.DatabaseID == "" || node.Revision == 0 {
			return nil, routerError(result.CodeInternal, "Router node source is corrupt")
		}
		if !node.Deleted {
			node.Aliases = append([]string{}, node.Aliases...)
			nodes = append(nodes, node)
		}
	}
	sort.Slice(nodes, func(left, right int) bool { return nodes[left].ID < nodes[right].ID })
	return nodes, nil
}

func (service *Service) nextID() (string, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	value, err := service.ids.Next()
	if err != nil || strings.TrimSpace(value) == "" {
		return "", routerError(result.CodeInternal, "Router ID allocation failed")
	}
	return "route_" + value, nil
}

func validateDefinition(definition NodeDefinition) error {
	if definition.Kind != KindBranch && definition.Kind != KindLeaf {
		return routerError(result.CodeValidation, "Router node kind must be branch or leaf")
	}
	if err := validateName(definition.Name); err != nil {
		return err
	}
	if err := validatePurpose(definition.Purpose); err != nil {
		return err
	}
	if !utf8.ValidString(definition.Synopsis) || utf8.RuneCountInString(definition.Synopsis) > 1000 {
		return routerError(result.CodeValidation, "Router synopsis must contain at most 1000 characters")
	}
	return nil
}

func validateName(name string) error {
	if name != strings.ToLower(strings.TrimSpace(name)) || name == "" ||
		utf8.RuneCountInString(name) > maxNameRunes {
		return routerError(result.CodeValidation, "Router name must be a lowercase path segment of at most 64 characters")
	}
	for _, value := range name {
		if unicode.IsLetter(value) || unicode.IsNumber(value) || value == '-' || value == '_' {
			continue
		}
		return routerError(result.CodeValidation, "Router name must contain letters, numbers, - or _")
	}
	return nil
}

func validatePurpose(purpose string) error {
	if strings.TrimSpace(purpose) == "" || !utf8.ValidString(purpose) ||
		utf8.RuneCountInString(purpose) > maxPurposeRunes {
		return routerError(result.CodeValidation, "Router purpose must contain 1–800 characters")
	}
	return nil
}

func validateLocator(locator Locator) error {
	if locator.DatabaseID == "" || locator.TableID == "" || locator.Revision == 0 {
		return routerError(result.CodeValidation, "Router locator is incomplete")
	}
	if _, err := logical.Validate(logical.Constraint{
		Name: "row_id", Definition: logical.Definition{Kind: logical.KindRelationID},
	}, locator.RowID); err != nil {
		return err
	}
	return nil
}

func putNode(ctx context.Context, tx store.Tx, node Node) error {
	return putJSON(ctx, tx, nodeBucket, node.ID, node)
}

func getNode(ctx context.Context, tx store.Tx, nodeID string) (Node, error) {
	encoded, err := tx.Get(ctx, nodeBucket, nodeID)
	if errors.Is(err, store.ErrNotFound) {
		return Node{}, routerError(result.CodeNotFound, "Router node was not found")
	}
	if err != nil {
		return Node{}, stableError(err)
	}
	var node Node
	if decodeJSON(encoded, &node) != nil || node.Version != Version || node.ID != nodeID ||
		node.DatabaseID == "" || node.Revision == 0 || node.Deleted {
		return Node{}, routerError(result.CodeNotFound, "Router node was not found")
	}
	return node, nil
}

func putPath(ctx context.Context, tx store.Tx, databaseID, path, nodeID string) error {
	return tx.Put(ctx, pathBucket, databaseID+"\x00"+path, []byte(nodeID))
}

func resolvePathIn(ctx context.Context, tx store.Tx, databaseID, path string) (Node, error) {
	encoded, err := tx.Get(ctx, pathBucket, databaseID+"\x00"+canonicalPath(path))
	if errors.Is(err, store.ErrNotFound) {
		return Node{}, routerError(result.CodeNotFound, "Router path was not found")
	}
	if err != nil {
		return Node{}, stableError(err)
	}
	node, err := getNode(ctx, tx, string(encoded))
	if err != nil {
		return Node{}, err
	}
	if node.DatabaseID != databaseID {
		return Node{}, routerError(result.CodeInternal, "Router path index is corrupt")
	}
	return node, nil
}

func loadStrings(ctx context.Context, tx store.Tx, bucket, key string) ([]string, error) {
	encoded, err := tx.Get(ctx, bucket, key)
	if errors.Is(err, store.ErrNotFound) {
		return []string{}, nil
	}
	if err != nil {
		return nil, stableError(err)
	}
	var value stringIndex
	if decodeJSON(encoded, &value) != nil || value.Version != Version || value.Values == nil {
		return nil, routerError(result.CodeInternal, "Router index is corrupt")
	}
	return value.Values, nil
}

func saveStrings(ctx context.Context, tx store.Tx, bucket, key string, values []string) error {
	return putJSON(ctx, tx, bucket, key, stringIndex{Version: Version, Values: values})
}

func loadLocators(ctx context.Context, tx store.Tx, leafID string) ([]Locator, error) {
	encoded, err := tx.Get(ctx, leafBucket, leafID)
	if errors.Is(err, store.ErrNotFound) {
		return []Locator{}, nil
	}
	if err != nil {
		return nil, stableError(err)
	}
	var value locatorIndex
	if decodeJSON(encoded, &value) != nil || value.Version != Version || value.Locators == nil {
		return nil, routerError(result.CodeInternal, "Router leaf index is corrupt")
	}
	return value.Locators, nil
}

func saveLocators(ctx context.Context, tx store.Tx, leafID string, values []Locator) error {
	sort.Slice(values, func(left, right int) bool {
		if values[left].TableID != values[right].TableID {
			return values[left].TableID < values[right].TableID
		}
		return values[left].RowID < values[right].RowID
	})
	return putJSON(ctx, tx, leafBucket, leafID, locatorIndex{Version: Version, Locators: values})
}

func removeLeafLocator(ctx context.Context, tx store.Tx, leafID string, locator Locator) error {
	values, err := loadLocators(ctx, tx, leafID)
	if err != nil {
		return err
	}
	filtered := values[:0]
	for _, value := range values {
		if !sameRow(value, locator) {
			filtered = append(filtered, value)
		}
	}
	if len(filtered) == len(values) {
		return routerError(result.CodeInternal, "Router reverse membership is corrupt")
	}
	return saveLocators(ctx, tx, leafID, filtered)
}

func putJSON(ctx context.Context, tx store.Tx, bucket, key string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return routerError(result.CodeInternal, "Router record could not be encoded")
	}
	if err := tx.Put(ctx, bucket, key, encoded); err != nil {
		return stableError(err)
	}
	return nil
}

func decodeJSON(encoded []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	return decoder.Decode(target)
}

func memberships(locator Locator, leafIDs []string) []Membership {
	values := make([]Membership, len(leafIDs))
	for index, leafID := range leafIDs {
		values[index] = Membership{LeafID: leafID, Locator: locator}
	}
	return values
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	resultValues := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		resultValues = append(resultValues, value)
	}
	sort.Strings(resultValues)
	return resultValues
}

func removeString(values []string, target string) []string {
	filtered := values[:0]
	for _, value := range values {
		if value != target {
			filtered = append(filtered, value)
		}
	}
	return filtered
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func sameRow(left, right Locator) bool {
	return left.DatabaseID == right.DatabaseID && left.TableID == right.TableID &&
		left.RowID == right.RowID
}

func locatorKey(locator Locator) string {
	return locator.DatabaseID + "\x00" + locator.TableID + "\x00" + locator.RowID
}

func joinPath(parent, name string) string {
	if parent == "/" {
		return "/" + name
	}
	return parent + "/" + name
}

func canonicalPath(path string) string {
	value := strings.TrimSpace(path)
	if value == "" {
		return "/"
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	value = strings.TrimRight(value, "/")
	if value == "" {
		return "/"
	}
	return value
}

func hasCode(err error, code result.Code) bool {
	var stable interface{ StableCode() string }
	return errors.As(err, &stable) && stable.StableCode() == string(code)
}

func routerError(code result.Code, message string) error {
	return &Error{Code: code, Message: message}
}

func stableError(err error) error {
	if err == nil {
		return nil
	}
	var stable interface{ StableCode() string }
	if errors.As(err, &stable) {
		return err
	}
	switch {
	case errors.Is(err, context.Canceled):
		return routerError(result.CodeCancelled, "Router operation was cancelled")
	case errors.Is(err, context.DeadlineExceeded):
		return routerError(result.CodeDeadlineExceeded, "Router operation exceeded its deadline")
	default:
		return routerError(result.CodeInternal, "Router operation failed")
	}
}

type uuidSource struct{}

func (uuidSource) Next() (string, error) { return uuid.NewString(), nil }
