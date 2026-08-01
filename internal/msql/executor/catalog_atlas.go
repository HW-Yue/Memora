package executor

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/security"
)

type catalogAtlasSource interface {
	ShowDatabases(context.Context) ([]catalog.Database, error)
	ShowTables(context.Context, string) ([]catalog.Table, error)
}

func (engine *Engine) executeCatalogAtlas(ctx context.Context, request catalogPageRequest) (Output, error) {
	source, ok := engine.catalog.(catalogAtlasSource)
	if !ok {
		return Output{}, executeError(result.CodeUnsupported, "Catalog Atlas requires a complete Catalog service")
	}
	rows, err := catalogAtlasRows(ctx, source)
	if err != nil {
		return Output{}, err
	}
	pageRows, page, err := paginateCatalogAtlasRows(rows, request)
	if err != nil {
		return Output{}, err
	}
	return Output{Columns: []result.Column{}, Rows: pageRows, Page: page,
		Truncated: page.Truncated, NextCursor: page.NextCursor}, nil
}

func catalogAtlasRows(ctx context.Context, source catalogAtlasSource) ([]result.Row, error) {
	databases, err := source.ShowDatabases(ctx)
	if err != nil {
		return nil, normalizeError(err)
	}
	sort.Slice(databases, func(left, right int) bool {
		leftName, rightName := strings.ToLower(databases[left].Name), strings.ToLower(databases[right].Name)
		if leftName == rightName {
			return databases[left].ID < databases[right].ID
		}
		return leftName < rightName
	})
	authorization, scoped := security.AuthorizationFrom(ctx)
	rows := make([]result.Row, 0, len(databases))
	for _, database := range databases {
		if scoped && !security.AllowsAnyDatabase(authorization, append([]string{database.ID, database.Name}, database.Aliases...)...) {
			continue
		}
		rows = append(rows, databaseAtlasRow(database))
		tables, tableErr := source.ShowTables(ctx, database.Name)
		if tableErr != nil {
			return nil, normalizeError(tableErr)
		}
		sort.Slice(tables, func(left, right int) bool {
			leftName, rightName := strings.ToLower(tables[left].Name), strings.ToLower(tables[right].Name)
			if leftName == rightName {
				return tables[left].ID < tables[right].ID
			}
			return leftName < rightName
		})
		for _, table := range tables {
			rows = append(rows, tableAtlasRow(database, table))
		}
	}
	return rows, nil
}

func databaseAtlasRow(database catalog.Database) result.Row {
	row := result.Row{"kind": "database", "database_id": database.ID, "database": database.Name,
		"aliases": append([]string{}, database.Aliases...), "purpose": database.Purpose,
		"scope": database.Scope, "schema_version": database.SchemaVersion}
	if database.AntiScope != "" {
		row["anti_scope"] = database.AntiScope
	}
	return row
}

func tableAtlasRow(database catalog.Database, table catalog.Table) result.Row {
	row := result.Row{"kind": "table", "database_id": database.ID, "database": database.Name,
		"table_id": table.ID, "table": table.Name, "aliases": append([]string{}, table.Aliases...),
		"purpose": table.Purpose, "row_semantics": table.RowSemantics, "schema_version": table.SchemaVersion}
	if table.Scope != "" {
		row["scope"] = table.Scope
	}
	if table.AntiScope != "" {
		row["anti_scope"] = table.AntiScope
	}
	return row
}

func paginateCatalogAtlasRows(rows []result.Row, request catalogPageRequest) ([]result.Row, *result.ListPage, error) {
	snapshot, err := catalogRowsSnapshot(request.scope, rows)
	if err != nil {
		return nil, nil, executeError(result.CodeInternal, "Catalog Atlas snapshot could not be encoded")
	}
	offset := uint64(0)
	if request.decoded != nil {
		if request.decoded.Snapshot != snapshot {
			return nil, nil, executeError(result.CodeRevisionConflict, "Catalog Atlas changed after the cursor snapshot")
		}
		offset = request.decoded.Offset
		if offset == 0 || offset >= uint64(len(rows)) || offset > uint64(^uint(0)>>1) {
			return nil, nil, executeError(result.CodeValidation, "Catalog Atlas cursor offset is invalid")
		}
	}
	end, used := offset, uint64(2)
	for end < uint64(len(rows)) && end-offset < request.limit {
		encoded, encodeErr := json.Marshal(rows[int(end)])
		if encodeErr != nil {
			return nil, nil, executeError(result.CodeInternal, "Catalog Atlas row could not be encoded")
		}
		additional := uint64(len(encoded))
		if end > offset {
			additional++
		}
		if used+additional > request.bytes {
			if end == offset {
				return nil, nil, executeError(result.CodeOutputTruncated, "Catalog Atlas BYTES cannot contain one metadata entry")
			}
			break
		}
		used += additional
		end++
	}
	truncated := end < uint64(len(rows))
	next := ""
	if truncated {
		next, err = encodeCatalogCursor(catalogCursorCore{Version: catalogCursorVersion,
			Scope: request.scope, Snapshot: snapshot, Offset: end})
		if err != nil {
			return nil, nil, executeError(result.CodeInternal, "Catalog Atlas cursor could not be encoded")
		}
	}
	page := &result.ListPage{Version: result.ListPageVersion, Limit: request.limit, Cursor: request.cursor,
		Snapshot: snapshot, Truncated: truncated, NextCursor: next}
	return rows[int(offset):int(end)], page, nil
}
