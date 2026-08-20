package executor

import (
	"errors"
	"testing"

	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/router"
)

func TestRouteReadRejectsCrossScopeAndDuplicateBackendValues(t *testing.T) {
	t.Parallel()

	valid := router.Node{
		Version: router.Version, ID: "route_child", DatabaseID: "db_work", TableID: "tbl_notes",
		ParentID: "route_root", Name: "child", Path: "/child", Kind: router.KindLeaf,
		Purpose: "Child", Revision: 1,
	}
	if err := validateRouteChildren([]router.Node{valid}, "db_work", "tbl_notes", "route_root", false); err != nil {
		t.Fatal(err)
	}
	// The archive listing is the mirror image: an archived page must contain
	// only archived nodes, and the live listing only live ones.
	archived := valid
	archived.Deleted = true
	if err := validateRouteChildren([]router.Node{archived}, "db_work", "tbl_notes", "route_root", true); err != nil {
		t.Fatal(err)
	}
	assertRouteReadInternal(t, validateRouteChildren([]router.Node{archived}, "db_work", "tbl_notes", "route_root", false))
	assertRouteReadInternal(t, validateRouteChildren([]router.Node{valid}, "db_work", "tbl_notes", "route_root", true))

	crossScope := valid
	crossScope.DatabaseID = "db_private"
	assertRouteReadInternal(t, validateRouteChildren([]router.Node{crossScope}, "db_work", "tbl_notes", "route_root", false))
	assertRouteReadInternal(t, validateRouteChildren([]router.Node{valid, valid}, "db_work", "tbl_notes", "route_root", false))

	leaf := valid
	locator := router.Locator{DatabaseID: "db_work", TableID: "tbl_notes", RowID: "row_one", Revision: 1}
	if err := validateRouteLocators([]router.Locator{locator}, leaf); err != nil {
		t.Fatal(err)
	}
	crossTable := locator
	crossTable.TableID = "tbl_private"
	assertRouteReadInternal(t, validateRouteLocators([]router.Locator{crossTable}, leaf))
	assertRouteReadInternal(t, validateRouteLocators([]router.Locator{locator, locator}, leaf))
}

func assertRouteReadInternal(t *testing.T, err error) {
	t.Helper()
	var stable interface{ StableCode() string }
	if !errors.As(err, &stable) || stable.StableCode() != string(result.CodeInternal) {
		t.Fatalf("error = %v, want %s", err, result.CodeInternal)
	}
}
