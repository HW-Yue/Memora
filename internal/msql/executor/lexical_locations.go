package executor

import (
	"context"
	"errors"
	"sort"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/lexicallocation"
	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/result"
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
	databaseIDs, err := engine.visibleLexicalDatabaseIDs(ctx)
	if err != nil {
		return Output{}, err
	}
	request := lexicallocation.Request{Query: query, DatabaseIDs: databaseIDs, Cursor: cursor,
		Limit: limit, ByteLimit: byteLimit}
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
	rows := make([]result.Row, 0, len(page.Locations))
	for _, location := range page.Locations {
		value := result.Row{"kind": string(location.Kind), "database_id": location.DatabaseID,
			"object_id": location.ObjectID, "revision": location.Revision,
			"matched_term_count": location.MatchedTermCount, "matched_field_count": location.MatchedFieldCount,
			"frequency": location.Frequency, "matched_field_ids": append([]string(nil), location.MatchedFieldIDs...)}
		if location.TableID != "" {
			value["table_id"] = location.TableID
		}
		rows = append(rows, value)
	}
	resultPage := &result.ListPage{Version: result.ListPageVersion, Limit: page.Limit, Cursor: page.Cursor,
		Snapshot: page.Snapshot, Truncated: page.Truncated, NextCursor: page.NextCursor}
	return Output{Columns: []result.Column{}, Rows: rows, Page: resultPage,
		Truncated: page.Truncated, NextCursor: page.NextCursor}, nil
}

func (engine *Engine) visibleLexicalDatabaseIDs(ctx context.Context) ([]string, error) {
	if engine == nil || engine.candidateCatalog == nil {
		return nil, executeError(result.CodeUnsupported, "lexical location query requires a complete Catalog service")
	}
	databases, err := engine.candidateCatalog.ShowDatabases(ctx)
	if err != nil {
		return nil, normalizeError(err)
	}
	authorization, scoped := security.AuthorizationFrom(ctx)
	resultIDs := make([]string, 0, len(databases))
	for _, database := range databases {
		if scoped && !security.AllowsAnyDatabase(authorization,
			append([]string{database.ID, database.Name}, database.Aliases...)...) {
			continue
		}
		resultIDs = append(resultIDs, database.ID)
	}
	sort.Strings(resultIDs)
	return resultIDs, nil
}
