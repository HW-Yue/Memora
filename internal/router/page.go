package router

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

	"github.com/HW-Yue/Memora/internal/result"
)

const (
	ReadCursorVersion = "memora.route-cursor/v1"
	maximumCursorSize = 4096
)

type ReadPage struct {
	Snapshot   string
	NextCursor string
}

type readCursor struct {
	Version  string `json:"version"`
	Scope    string `json:"scope"`
	Snapshot string `json:"snapshot"`
	Offset   uint64 `json:"offset"`
	Checksum string `json:"checksum"`
}

type readCursorCore struct {
	Version  string `json:"version"`
	Scope    string `json:"scope"`
	Snapshot string `json:"snapshot"`
	Offset   uint64 `json:"offset"`
}

type pageNode struct {
	ID       string `json:"route_id"`
	ParentID string `json:"parent_id,omitempty"`
	Path     string `json:"path"`
	Name     string `json:"name"`
	Kind     Kind   `json:"kind"`
	Purpose  string `json:"purpose"`
	Revision uint64 `json:"revision"`
}

func PaginateNodes(scope, cursor string, limit int, nodes []Node) ([]Node, ReadPage, error) {
	values := make([]pageNode, 0, len(nodes))
	for _, node := range nodes {
		values = append(values, pageNode{
			ID: node.ID, ParentID: node.ParentID, Path: node.Path, Name: node.Name,
			Kind: node.Kind, Purpose: node.Purpose, Revision: node.Revision,
		})
	}
	start, page, err := paginateRead("children", scope, cursor, limit, values)
	if err != nil {
		return nil, ReadPage{}, err
	}
	end := boundedPageEnd(start, limit, len(nodes))
	return nodes[start:end], page, nil
}

func PaginateLocators(scope, cursor string, limit int, locators []Locator) ([]Locator, ReadPage, error) {
	start, page, err := paginateRead("locators", scope, cursor, limit, locators)
	if err != nil {
		return nil, ReadPage{}, err
	}
	end := boundedPageEnd(start, limit, len(locators))
	return locators[start:end], page, nil
}

func paginateRead[T any](kind, scope, cursor string, limit int, values []T) (int, ReadPage, error) {
	if strings.TrimSpace(scope) == "" || limit < 1 {
		return 0, ReadPage{}, routerError(result.CodeValidation, "Route page scope and positive limit are required")
	}
	if values == nil {
		values = []T{}
	}
	scopeDigest := readDigest(struct {
		Kind  string `json:"kind"`
		Scope string `json:"scope"`
	}{Kind: kind, Scope: scope})
	snapshot, err := readSnapshot(kind, scopeDigest, values)
	if err != nil {
		return 0, ReadPage{}, routerError(result.CodeInternal, "Route page snapshot could not be encoded")
	}
	start := 0
	if cursor != "" {
		decoded, err := decodeReadCursor(cursor)
		if err != nil || decoded.Scope != scopeDigest {
			return 0, ReadPage{}, routerError(result.CodeValidation, "Route cursor is invalid for this scope")
		}
		if decoded.Snapshot != snapshot {
			return 0, ReadPage{}, routerError(result.CodeRevisionConflict, "Route changed after the cursor snapshot")
		}
		if decoded.Offset == 0 || decoded.Offset >= uint64(len(values)) || decoded.Offset > uint64(^uint(0)>>1) {
			return 0, ReadPage{}, routerError(result.CodeValidation, "Route cursor offset is invalid")
		}
		start = int(decoded.Offset)
	}
	end := boundedPageEnd(start, limit, len(values))
	page := ReadPage{Snapshot: snapshot}
	if end < len(values) {
		page.NextCursor, err = encodeReadCursor(readCursorCore{
			Version: ReadCursorVersion, Scope: scopeDigest, Snapshot: snapshot, Offset: uint64(end),
		})
		if err != nil {
			return 0, ReadPage{}, routerError(result.CodeInternal, "Route cursor could not be encoded")
		}
	}
	return start, page, nil
}

func readSnapshot[T any](kind, scope string, values []T) (string, error) {
	encoded, err := json.Marshal(struct {
		Kind   string `json:"kind"`
		Scope  string `json:"scope"`
		Values []T    `json:"values"`
	}{Kind: kind, Scope: scope, Values: values})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func encodeReadCursor(core readCursorCore) (string, error) {
	checksum, err := readCursorChecksum(core)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(readCursor{
		Version: core.Version, Scope: core.Scope, Snapshot: core.Snapshot,
		Offset: core.Offset, Checksum: checksum,
	})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func decodeReadCursor(value string) (readCursor, error) {
	if value == "" || len(value) > maximumCursorSize {
		return readCursor{}, fmt.Errorf("invalid Route cursor size")
	}
	encoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(encoded) == 0 || len(encoded) > maximumCursorSize {
		return readCursor{}, fmt.Errorf("invalid Route cursor encoding")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var decoded readCursor
	if err := decoder.Decode(&decoded); err != nil || decoded.Version != ReadCursorVersion ||
		decoded.Scope == "" || decoded.Snapshot == "" || decoded.Offset == 0 || decoded.Checksum == "" {
		return readCursor{}, fmt.Errorf("invalid Route cursor payload")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return readCursor{}, fmt.Errorf("invalid Route cursor trailing data")
	}
	canonical, err := json.Marshal(decoded)
	if err != nil || !bytes.Equal(encoded, canonical) {
		return readCursor{}, fmt.Errorf("non-canonical Route cursor")
	}
	want, err := readCursorChecksum(readCursorCore{
		Version: decoded.Version, Scope: decoded.Scope, Snapshot: decoded.Snapshot, Offset: decoded.Offset,
	})
	if err != nil || subtle.ConstantTimeCompare([]byte(decoded.Checksum), []byte(want)) != 1 {
		return readCursor{}, fmt.Errorf("invalid Route cursor checksum")
	}
	return decoded, nil
}

func readCursorChecksum(core readCursorCore) (string, error) {
	encoded, err := json.Marshal(core)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func readDigest(value any) string {
	encoded, _ := json.Marshal(value)
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func boundedPageEnd(start, limit, length int) int {
	if limit >= length-start {
		return length
	}
	return start + limit
}
