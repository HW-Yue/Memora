package router

import (
	"encoding/base64"
	"errors"
	"testing"

	"github.com/HW-Yue/Memora/internal/result"
)

func TestRoutePageCursorBindsScopeSnapshotAndOffset(t *testing.T) {
	t.Parallel()

	nodes := []Node{
		{ID: "route_a", ParentID: "route_root", Name: "a", Path: "/a", Kind: KindLeaf, Purpose: "A", Revision: 1},
		{ID: "route_b", ParentID: "route_root", Name: "b", Path: "/b", Kind: KindLeaf, Purpose: "B", Revision: 1},
		{ID: "route_c", ParentID: "route_root", Name: "c", Path: "/c", Kind: KindLeaf, Purpose: "C", Revision: 1},
	}
	first, page, err := PaginateNodes("parent:route_root", "", 2, nodes)
	if err != nil || len(first) != 2 || page.Snapshot == "" || page.NextCursor == "" {
		t.Fatalf("first page = %#v, %#v, %v", first, page, err)
	}
	second, continued, err := PaginateNodes("parent:route_root", page.NextCursor, 2, nodes)
	if err != nil || len(second) != 1 || second[0].ID != "route_c" ||
		continued.Snapshot != page.Snapshot || continued.NextCursor != "" {
		t.Fatalf("second page = %#v, %#v, %v", second, continued, err)
	}

	changed := append(append([]Node{}, nodes...), Node{
		ID: "route_d", ParentID: "route_root", Name: "d", Path: "/d", Kind: KindLeaf, Purpose: "D", Revision: 1,
	})
	if _, _, err := PaginateNodes("parent:route_root", page.NextCursor, 2, changed); !routeCode(err, result.CodeRevisionConflict) {
		t.Fatalf("changed snapshot error = %v", err)
	}
	if _, _, err := PaginateNodes("parent:other", page.NextCursor, 2, nodes); !routeCode(err, result.CodeValidation) {
		t.Fatalf("cross-scope cursor error = %v", err)
	}
	tampered := page.NextCursor[:len(page.NextCursor)-1] + "A"
	if page.NextCursor[len(page.NextCursor)-1] == 'A' {
		tampered = page.NextCursor[:len(page.NextCursor)-1] + "B"
	}
	if _, _, err := PaginateNodes("parent:route_root", tampered, 2, nodes); !routeCode(err, result.CodeValidation) {
		t.Fatalf("tampered cursor error = %v", err)
	}
	if _, _, err := PaginateLocators("parent:route_root", page.NextCursor, 2, []Locator{
		{DatabaseID: "db", TableID: "table", RowID: "row", Revision: 1},
	}); !routeCode(err, result.CodeValidation) {
		t.Fatalf("cross-kind cursor error = %v", err)
	}
	decoded, err := decodeReadCursor(page.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	forged, err := encodeReadCursor(readCursorCore{
		Version: decoded.Version, Scope: decoded.Scope, Snapshot: decoded.Snapshot, Offset: 99,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := PaginateNodes("parent:route_root", forged, 2, nodes); !routeCode(err, result.CodeValidation) {
		t.Fatalf("out-of-range cursor error = %v", err)
	}

	encoded, err := base64.RawURLEncoding.DecodeString(page.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	nonCanonical := append([]byte(" "), encoded...)
	if _, err := decodeReadCursor(base64.RawURLEncoding.EncodeToString(nonCanonical)); err == nil {
		t.Fatal("non-canonical cursor succeeded")
	}
	unknown := append(encoded[:len(encoded)-1], []byte(`,"unknown":true}`)...)
	if _, err := decodeReadCursor(base64.RawURLEncoding.EncodeToString(unknown)); err == nil {
		t.Fatal("cursor with unknown field succeeded")
	}
	trailing := append(append([]byte{}, encoded...), []byte(`{}`)...)
	if _, err := decodeReadCursor(base64.RawURLEncoding.EncodeToString(trailing)); err == nil {
		t.Fatal("cursor with trailing data succeeded")
	}
}

func routeCode(err error, code result.Code) bool {
	var stable interface{ StableCode() string }
	return errors.As(err, &stable) && stable.StableCode() == string(code)
}
