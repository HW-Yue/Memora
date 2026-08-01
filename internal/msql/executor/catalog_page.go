package executor

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/result"
)

const (
	defaultCatalogPageLimit = uint64(64)
	maximumCatalogPageLimit = uint64(256)
	maximumCatalogCursor    = 4096
	catalogCursorVersion    = "memora.catalog-cursor/v1"
	defaultAtlasByteLimit   = uint64(16_384)
	minimumAtlasByteLimit   = uint64(512)
	maximumAtlasByteLimit   = uint64(65_536)
)

type catalogPageRequest struct {
	limit   uint64
	cursor  string
	scope   string
	decoded *catalogCursor
	bytes   uint64
}

type catalogCursor struct {
	Version  string `json:"version"`
	Scope    string `json:"scope"`
	Snapshot string `json:"snapshot"`
	Offset   uint64 `json:"offset"`
	Checksum string `json:"checksum"`
}

type catalogCursorCore struct {
	Version  string `json:"version"`
	Scope    string `json:"scope"`
	Snapshot string `json:"snapshot"`
	Offset   uint64 `json:"offset"`
}

func catalogPageRequestFor(statement ast.Statement, bound bindings) (catalogPageRequest, bool, error) {
	show := statement.Show
	if show == nil || (show.Object != "DATABASES" && show.Object != "TABLES" && show.Object != "COLUMNS" && show.Object != "CATALOG_ATLAS") {
		return catalogPageRequest{}, false, nil
	}
	request := catalogPageRequest{limit: defaultCatalogPageLimit, scope: catalogPageScope(show)}
	if show.Object == "CATALOG_ATLAS" {
		request.bytes = defaultAtlasByteLimit
	}
	if show.Limit != nil {
		limit, err := historyPositiveInteger(show.Limit, catalog.Table{}, bound, "Catalog list LIMIT")
		if err != nil {
			return catalogPageRequest{}, true, err
		}
		if limit > maximumCatalogPageLimit {
			return catalogPageRequest{}, true, executeError(
				result.CodeValidation,
				fmt.Sprintf("Catalog list LIMIT must be between 1 and %d", maximumCatalogPageLimit),
			)
		}
		request.limit = limit
	}
	if show.Cursor != nil {
		cursor, err := relationshipString(show.Cursor, catalog.Table{}, bound, "Catalog cursor")
		if err != nil {
			return catalogPageRequest{}, true, err
		}
		if cursor == "" || len(cursor) > maximumCatalogCursor {
			return catalogPageRequest{}, true, executeError(result.CodeValidation, "Catalog cursor is invalid")
		}
		decoded, err := decodeCatalogCursor(cursor)
		if err != nil || decoded.Scope != request.scope {
			return catalogPageRequest{}, true, executeError(result.CodeValidation, "Catalog cursor is invalid for this list")
		}
		request.cursor, request.decoded = cursor, &decoded
	}
	if show.Object == "CATALOG_ATLAS" && show.ByteLimit != nil {
		byteLimit, err := historyPositiveInteger(show.ByteLimit, catalog.Table{}, bound, "Catalog Atlas BYTES")
		if err != nil {
			return catalogPageRequest{}, true, err
		}
		if byteLimit < minimumAtlasByteLimit || byteLimit > maximumAtlasByteLimit {
			return catalogPageRequest{}, true, executeError(result.CodeValidation,
				fmt.Sprintf("Catalog Atlas BYTES must be between %d and %d", minimumAtlasByteLimit, maximumAtlasByteLimit))
		}
		request.bytes = byteLimit
	}
	return request, true, nil
}

func catalogPageScope(show *ast.ShowStatement) string {
	type scope struct {
		Object   string   `json:"object"`
		Database []string `json:"database"`
		Table    []string `json:"table"`
		Compact  bool     `json:"compact"`
	}
	value := scope{Object: show.Object, Compact: show.Compact}
	if show.Database != nil {
		value.Database = catalogNameParts(*show.Database)
	}
	if show.Table != nil {
		value.Table = catalogNameParts(*show.Table)
	}
	encoded, _ := json.Marshal(value)
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func catalogNameParts(name ast.Name) []string {
	parts := make([]string, 0, len(name.Parts))
	for _, part := range name.Parts {
		parts = append(parts, strings.ToLower(strings.TrimSpace(part.Value)))
	}
	return parts
}

func paginateCatalogRows(rows []result.Row, request catalogPageRequest) ([]result.Row, *result.ListPage, error) {
	snapshot, err := catalogRowsSnapshot(request.scope, rows)
	if err != nil {
		return nil, nil, executeError(result.CodeInternal, "Catalog snapshot could not be encoded")
	}
	offset := uint64(0)
	if request.decoded != nil {
		if request.decoded.Snapshot != snapshot {
			return nil, nil, executeError(result.CodeRevisionConflict, "Catalog changed after the cursor snapshot")
		}
		offset = request.decoded.Offset
		if offset == 0 || offset >= uint64(len(rows)) || offset > uint64(^uint(0)>>1) {
			return nil, nil, executeError(result.CodeValidation, "Catalog cursor offset is invalid")
		}
	}
	end := offset + request.limit
	if end > uint64(len(rows)) {
		end = uint64(len(rows))
	}
	truncated := end < uint64(len(rows))
	next := ""
	if truncated {
		next, err = encodeCatalogCursor(catalogCursorCore{
			Version: catalogCursorVersion, Scope: request.scope, Snapshot: snapshot, Offset: end,
		})
		if err != nil {
			return nil, nil, executeError(result.CodeInternal, "Catalog cursor could not be encoded")
		}
	}
	page := &result.ListPage{
		Version: result.ListPageVersion, Limit: request.limit, Cursor: request.cursor,
		Snapshot: snapshot, Truncated: truncated, NextCursor: next,
	}
	return rows[int(offset):int(end)], page, nil
}

func catalogRowsSnapshot(scope string, rows []result.Row) (string, error) {
	encoded, err := json.Marshal(struct {
		Scope string       `json:"scope"`
		Rows  []result.Row `json:"rows"`
	}{Scope: scope, Rows: rows})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func encodeCatalogCursor(core catalogCursorCore) (string, error) {
	checksum, err := catalogCursorChecksum(core)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(catalogCursor{
		Version: core.Version, Scope: core.Scope, Snapshot: core.Snapshot,
		Offset: core.Offset, Checksum: checksum,
	})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func decodeCatalogCursor(value string) (catalogCursor, error) {
	encoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(encoded) == 0 || len(encoded) > maximumCatalogCursor {
		return catalogCursor{}, fmt.Errorf("invalid cursor encoding")
	}
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.DisallowUnknownFields()
	var decoded catalogCursor
	if err := decoder.Decode(&decoded); err != nil || decoded.Version != catalogCursorVersion ||
		decoded.Scope == "" || decoded.Snapshot == "" || decoded.Offset == 0 || decoded.Checksum == "" {
		return catalogCursor{}, fmt.Errorf("invalid cursor payload")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return catalogCursor{}, fmt.Errorf("invalid cursor trailing data")
	}
	canonical, err := json.Marshal(decoded)
	if err != nil || !bytes.Equal(encoded, canonical) {
		return catalogCursor{}, fmt.Errorf("non-canonical cursor payload")
	}
	want, err := catalogCursorChecksum(catalogCursorCore{
		Version: decoded.Version, Scope: decoded.Scope, Snapshot: decoded.Snapshot, Offset: decoded.Offset,
	})
	if err != nil || subtle.ConstantTimeCompare([]byte(decoded.Checksum), []byte(want)) != 1 {
		return catalogCursor{}, fmt.Errorf("invalid cursor checksum")
	}
	return decoded, nil
}

func catalogCursorChecksum(core catalogCursorCore) (string, error) {
	encoded, err := json.Marshal(core)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}
