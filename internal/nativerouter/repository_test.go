package nativerouter

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/router"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

func TestTableRouterShowUnderOpenAndReverseMembershipSurviveReopen(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "database.memora")
	file, err := nativestore.Create(path, nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	repository := New(file)
	root, err := repository.CreateRoot("route_root_notes", "db_work", "tbl_notes", "笔记语义入口")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateRoot("route_root_tasks", "db_work", "tbl_tasks", "任务语义入口"); err != nil {
		t.Fatal(err)
	}
	product, err := repository.CreateChild("route_product", root.ID, "产品", router.KindBranch, "产品知识")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateChild("route_storage", root.ID, "存储", router.KindBranch, "存储知识"); err != nil {
		t.Fatal(err)
	}
	decisions, err := repository.CreateChild("route_decisions", product.ID, "决策", router.KindLeaf, "产品决策 RowID")
	if err != nil {
		t.Fatal(err)
	}
	principles, err := repository.CreateChild("route_principles", product.ID, "原则", router.KindLeaf, "产品原则 RowID")
	if err != nil {
		t.Fatal(err)
	}
	locator := router.Locator{DatabaseID: "db_work", TableID: "tbl_notes", RowID: "row_ai_native", Revision: 3}
	if err := repository.Attach(decisions.ID, locator, 1); err != nil {
		t.Fatal(err)
	}
	if err := repository.Attach(principles.ID, locator, 1); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := nativestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	repository = New(reopened)
	if roots := repository.Roots("tbl_notes"); len(roots) != 1 || roots[0].ID != root.ID {
		t.Fatalf("Roots(tbl_notes) = %#v", roots)
	}
	if roots := repository.Roots("tbl_tasks"); len(roots) != 1 || roots[0].ID != "route_root_tasks" {
		t.Fatalf("Roots(tbl_tasks) = %#v", roots)
	}
	firstPage, cursor, err := repository.ShowUnder(root.ID, "", 1)
	if err != nil || len(firstPage) != 1 || cursor == "" {
		t.Fatalf("ShowUnder(first) = %#v, %q, %v", firstPage, cursor, err)
	}
	secondPage, next, err := repository.ShowUnder(root.ID, cursor, 1)
	if err != nil || len(secondPage) != 1 || next != "" || secondPage[0].ID == firstPage[0].ID {
		t.Fatalf("ShowUnder(second) = %#v, %q, %v", secondPage, next, err)
	}
	locators, truncated, err := repository.Open(decisions.ID, 10)
	if err != nil || truncated || len(locators) != 1 || locators[0] != locator {
		t.Fatalf("Open() = %#v, %v, %v", locators, truncated, err)
	}
	memberships, err := repository.Memberships(locator.RowID)
	if err != nil || len(memberships) != 2 || memberships[0].LeafID == memberships[1].LeafID {
		t.Fatalf("Memberships() = %#v, %v", memberships, err)
	}
}

func TestRouteSynopsisEncodingReadsRecordsWrittenBeforeSynopsis(t *testing.T) {
	t.Parallel()

	node := router.Node{
		Version: router.Version, ID: "route_branch", DatabaseID: "db_work",
		TableID: "tbl_notes", ParentID: "route_root", Name: "architecture",
		Aliases: []string{}, Path: "/architecture", Kind: router.KindBranch,
		Purpose: "Architecture", Revision: 1,
	}
	encoded, err := encodeNode(node)
	if err != nil || len(encoded) < 4 {
		t.Fatalf("encodeNode() = %d bytes, %v", len(encoded), err)
	}
	legacy := encoded[:len(encoded)-4] // synopsis was added as one trailing text field.
	decoded, err := decodeNode(legacy)
	if err != nil || decoded.ID != node.ID || decoded.Synopsis != "" {
		t.Fatalf("decode legacy node = %#v, %v", decoded, err)
	}
	node.Synopsis = "Current private subtree summary"
	encoded, err = encodeNode(node)
	decoded, decodeErr := decodeNode(encoded)
	if err != nil || decodeErr != nil || decoded.Synopsis != node.Synopsis {
		t.Fatalf("decode synopsis node = %#v, %v, %v", decoded, err, decodeErr)
	}
	if err := validateSynopsis(strings.Repeat("界", 1001)); err == nil {
		t.Fatal("validateSynopsis() accepted more than 1000 characters")
	}
}
