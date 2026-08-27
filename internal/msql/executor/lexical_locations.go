package executor

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/fulltext"
	"github.com/HW-Yue/Memora/internal/lexicallocation"
	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/security"
)

func (engine *Engine) showLexicalLocations(
	ctx context.Context, show *ast.ShowStatement, bound bindings,
) (Output, error) {
	if engine == nil || engine.lexicalLocations == nil {
		return Output{}, executeError(result.CodeUnsupported, "lexical location query is not supported by this backend")
	}
	if show == nil || show.Query == nil || show.Limit == nil || show.ByteLimit == nil {
		return Output{}, executeError(result.CodeValidation, "SHOW LEXICAL LOCATIONS is incomplete")
	}
	query, err := relationshipString(show.Query, catalog.Table{}, bound, "lexical location query")
	if err != nil {
		return Output{}, err
	}
	limit, err := historyPositiveInteger(show.Limit, catalog.Table{}, bound, "SHOW LEXICAL LOCATIONS LIMIT")
	if err != nil || limit > 64 {
		if err != nil {
			return Output{}, err
		}
		return Output{}, executeError(result.CodeValidation, "SHOW LEXICAL LOCATIONS LIMIT must be between 1 and 64")
	}
	byteLimit, err := historyPositiveInteger(show.ByteLimit, catalog.Table{}, bound, "SHOW LEXICAL LOCATIONS BYTES")
	if err != nil || byteLimit < 256 || byteLimit > 65536 {
		if err != nil {
			return Output{}, err
		}
		return Output{}, executeError(result.CodeValidation, "SHOW LEXICAL LOCATIONS BYTES must be between 256 and 65536")
	}
	cursor := ""
	if show.Cursor != nil {
		cursor, err = relationshipString(show.Cursor, catalog.Table{}, bound, "lexical location cursor")
		if err != nil || cursor == "" {
			if err != nil {
				return Output{}, err
			}
			return Output{}, executeError(result.CodeValidation, "lexical location cursor must not be empty")
		}
	}
	scope, err := engine.visibleLexicalScope(ctx)
	if err != nil {
		return Output{}, err
	}
	request := lexicallocation.Request{Query: query, DatabaseIDs: scope.databaseIDs, TableIDs: scope.tableIDs,
		Cursor: cursor, Limit: limit, ByteLimit: byteLimit}
	if err := lexicallocation.Validate(request); err != nil {
		return Output{}, executeError(result.CodeValidation, err.Error())
	}
	page, err := engine.lexicalLocations.SearchLexicalLocations(ctx, request)
	if err != nil {
		switch {
		case errors.Is(err, lexicallocation.ErrInvalid):
			return Output{}, executeError(result.CodeValidation, err.Error())
		case errors.Is(err, lexicallocation.ErrConflict):
			return Output{}, executeError(result.CodeRevisionConflict, err.Error())
		case errors.Is(err, lexicallocation.ErrOutputTooSmall):
			return Output{}, executeError(result.CodeOutputTruncated, err.Error())
		case errors.Is(err, lexicallocation.ErrSourceScope):
			return Output{}, executeError(result.CodeInternal, "lexical source violated its authorized scope")
		default:
			return Output{}, normalizeError(err)
		}
	}
	if err := lexicallocation.ValidatePage(page, request); err != nil {
		if errors.Is(err, lexicallocation.ErrSourceScope) {
			return Output{}, executeError(result.CodeInternal, "lexical source violated its authorized scope")
		}
		return Output{}, executeError(result.CodeInternal, "lexical source returned an invalid page")
	}
	// Retrieval answers where in the semantic tree the hit is. The counts and
	// the matched field list are renamed scores: exposing them makes the caller
	// rank and filter by them, which puts a second authority beside the Router.
	// See docs/query/predictor-path-only-v1.md.
	paths, err := engine.semanticPaths(ctx)
	if err != nil {
		return Output{}, err
	}
	rows := make([]result.Row, 0, len(page.Locations))
	for _, location := range page.Locations {
		value := result.Row{
			"kind": string(location.Kind), "database_id": location.DatabaseID,
			"object_id": location.ObjectID,
		}
		if location.TableID != "" {
			value["table_id"] = location.TableID
		}
		switch location.Kind {
		case fulltext.KindRow:
			// A Row hangs under any number of leaves, so its answer is a list.
			// The field is absent rather than empty when it hangs nowhere: an
			// unrouted Row has no place in the tree, and saying so with an
			// empty list would read as "the root".
			if found := engine.rowPaths(ctx, scope, paths, location); len(found) != 0 {
				value["paths"] = found
			}
		case fulltext.KindColumn:
			if path, exists := columnPath(scope, paths, location); exists {
				value["path"] = path
			}
		default:
			if path, exists := semanticPathFor(paths, location); exists {
				value["path"] = path
			}
		}
		rows = append(rows, value)
	}
	resultPage := &result.ListPage{Version: result.ListPageVersion, Limit: page.Limit, Cursor: page.Cursor,
		Snapshot: page.Snapshot, Truncated: page.Truncated, NextCursor: page.NextCursor}
	return Output{Columns: []result.Column{}, Rows: rows, Page: resultPage,
		Truncated: page.Truncated, NextCursor: page.NextCursor}, nil
}

// lexicalScope is what a lexical search may touch, plus the names a hit has to
// be resolved through. It is read in one pass because every part of it comes
// from the same Catalog walk: reading it twice would let the two halves
// disagree about which Tables exist.
type lexicalScope struct {
	databaseIDs []string
	tableIDs    []string
	// databaseNames and tables let a location's IDs be turned back into the
	// names a Row read takes, and a column ID into the column's name.
	databaseNames map[string]string
	tables        map[string]catalog.Table
}

// visibleLexicalScope returns the Databases and Tables a lexical search may
// touch. The Table scope is not redundant with the Database scope: archiving a
// Table leaves its postings in the index, so without an explicit Table
// allow-list an archived Table's Rows keep matching SHOW LEXICAL LOCATIONS.
func (engine *Engine) visibleLexicalScope(ctx context.Context) (lexicalScope, error) {
	if engine == nil || engine.candidateCatalog == nil {
		return lexicalScope{}, executeError(result.CodeUnsupported, "lexical location query requires a complete Catalog service")
	}
	databases, err := engine.candidateCatalog.ShowDatabases(ctx)
	if err != nil {
		return lexicalScope{}, normalizeError(err)
	}
	authorization, scoped := security.AuthorizationFrom(ctx)
	scope := lexicalScope{
		databaseIDs: make([]string, 0, len(databases)), tableIDs: []string{},
		databaseNames: map[string]string{}, tables: map[string]catalog.Table{},
	}
	for _, database := range databases {
		if scoped && !security.AllowsAnyDatabase(authorization,
			append([]string{database.ID, database.Name}, database.Aliases...)...) {
			continue
		}
		scope.databaseIDs = append(scope.databaseIDs, database.ID)
		scope.databaseNames[database.ID] = database.Name
		tables, err := engine.candidateCatalog.ShowTables(ctx, database.ID)
		if err != nil {
			return lexicalScope{}, normalizeError(err)
		}
		for _, table := range tables {
			scope.tableIDs = append(scope.tableIDs, table.ID)
			scope.tables[table.ID] = table
		}
	}
	sort.Strings(scope.databaseIDs)
	sort.Strings(scope.tableIDs)
	return scope, nil
}

// semanticPathIndex maps what a lexical hit names to where it sits in the tree.
type semanticPathIndex struct {
	byRouteID map[string]string
	byTableID map[string]string
}

// semanticPaths reads the Router once per query and indexes the two lookups a
// location listing needs.
//
// The Router is the only place a path is spelled. Recomputing one from names
// here would be a second spelling, and the two would drift the first time a
// RENAME landed between them.
func (engine *Engine) semanticPaths(ctx context.Context) (semanticPathIndex, error) {
	index := semanticPathIndex{
		byRouteID: map[string]string{}, byTableID: map[string]string{},
	}
	source, err := engine.lexicalRouteSource(ctx)
	if err != nil {
		return semanticPathIndex{}, err
	}
	latest := make(map[string]router.Node, len(source.Routes))
	for _, node := range source.Routes {
		if current, exists := latest[node.ID]; exists && current.Revision > node.Revision {
			continue
		}
		latest[node.ID] = node
	}
	for _, node := range latest {
		if node.Deleted {
			continue
		}
		index.byRouteID[node.ID] = node.Path
		if node.Kind == router.KindRoot {
			index.byTableID[node.TableID] = node.Path
		}
	}
	return index, nil
}

// semanticPathFor resolves the path of a location that names a node of the tree
// directly. Rows and Columns are resolved by rowPaths and columnPath, which
// have to reach past the Router to answer.
func semanticPathFor(index semanticPathIndex, location lexicallocation.Location) (string, bool) {
	switch location.Kind {
	case fulltext.KindRoute:
		path, exists := index.byRouteID[location.ObjectID]
		return path, exists
	case fulltext.KindTable:
		path, exists := index.byTableID[location.ObjectID]
		return path, exists
	default:
		return "", false
	}
}

// rowPaths is where in the tree a Row sits: the path of every leaf holding it.
//
// The Row stores its own leaves (docs/storage/leaf-rowid-v1.md), so this is a
// point read and a map lookup per leaf, never a search for who points at it.
// The paths themselves still come from the Router — it is the only place a path
// is spelled, and recomputing one here would drift at the next RENAME.
//
// A Row that cannot be read right now yields no paths rather than an error.
// This listing is a hint about where to navigate, and a Row deleted between the
// index read and this one has no place in the tree; failing the whole page over
// it would turn a stale hint into a broken query.
func (engine *Engine) rowPaths(
	ctx context.Context, scope lexicalScope, index semanticPathIndex, location lexicallocation.Location,
) []string {
	if engine == nil || engine.rows == nil {
		return nil
	}
	table, known := scope.tables[location.TableID]
	databaseName, named := scope.databaseNames[location.DatabaseID]
	if !known || !named {
		return nil
	}
	value, err := engine.rows.Get(ctx, databaseName, table.Name, location.ObjectID)
	if err != nil {
		return nil
	}
	paths := make([]string, 0, len(value.RouteLeafIDs))
	seen := make(map[string]bool, len(value.RouteLeafIDs))
	for _, leafID := range value.RouteLeafIDs {
		path, exists := index.byRouteID[leafID]
		if !exists || seen[path] {
			continue
		}
		seen[path] = true
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

// columnPath is a Column's place: its Table's semantic root, then the column's
// own name. A Column is not a node of the Router, so its path is composed —
// but only its last segment is composed here, and the part that could drift
// still comes from the Router.
func columnPath(
	scope lexicalScope, index semanticPathIndex, location lexicallocation.Location,
) (string, bool) {
	root, exists := index.byTableID[location.TableID]
	if !exists {
		return "", false
	}
	table, known := scope.tables[location.TableID]
	if !known {
		return "", false
	}
	for _, column := range table.Columns {
		if column.ID == location.ObjectID {
			return strings.TrimSuffix(root, "/") + "/" + column.Name, true
		}
	}
	return "", false
}
