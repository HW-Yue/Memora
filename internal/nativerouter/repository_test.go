package nativerouter

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/router"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

func TestTableRouterShowUnderOpenAndReverseMountSurviveReopen(t *testing.T) {
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
	mountRow(t, file, repository, decisions.ID, "row_ai_native")
	mountRow(t, file, repository, principles.ID, "row_ai_native")
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
	firstPage, childrenPage, err := repository.ShowUnderPage(root.ID, "", 1)
	if err != nil || len(firstPage) != 1 || childrenPage.Snapshot == "" || childrenPage.NextCursor == "" {
		t.Fatalf("ShowUnderPage(first) = %#v, %#v, %v", firstPage, childrenPage, err)
	}
	secondPage, continued, err := repository.ShowUnderPage(root.ID, childrenPage.NextCursor, 1)
	if err != nil || len(secondPage) != 1 || continued.NextCursor != "" ||
		continued.Snapshot != childrenPage.Snapshot || secondPage[0].ID == firstPage[0].ID {
		t.Fatalf("ShowUnderPage(second) = %#v, %#v, %v", secondPage, continued, err)
	}
	leaf, err := repository.LeafForOpen(decisions.ID, 1)
	if err != nil || leaf.RowID != "row_ai_native" {
		t.Fatalf("LeafForOpen() = %#v, %v", leaf, err)
	}
	if _, err := repository.CreateChild("route_extra", root.ID, "额外", router.KindLeaf, "额外知识"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.ShowUnderPage(root.ID, childrenPage.NextCursor, 1); !nativeRouteCode(err, result.CodeRevisionConflict) {
		t.Fatalf("changed native children error = %v", err)
	}
	// One Row may still hang under several leaves; only the other direction is
	// capped at one.
	held, err := repository.LeavesHoldingRow("row_ai_native")
	if err != nil || len(held) != 2 || held[0] == held[1] {
		t.Fatalf("LeavesHoldingRow() = %#v, %v", held, err)
	}
}

func TestLeafTransferAndReverseLookupFollowTheLeafField(t *testing.T) {
	t.Parallel()

	file, err := nativestore.Create(filepath.Join(t.TempDir(), "database.memora"), nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	repository := New(file)
	root, err := repository.CreateRoot("route_root", "db_work", "tbl_notes", "Notes")
	if err != nil {
		t.Fatal(err)
	}
	firstLeaf, err := repository.CreateChild("route_first", root.ID, "first", router.KindLeaf, "First leaf")
	if err != nil {
		t.Fatal(err)
	}
	secondLeaf, err := repository.CreateChild("route_second", root.ID, "second", router.KindLeaf, "Second leaf")
	if err != nil {
		t.Fatal(err)
	}
	mountRow(t, file, repository, firstLeaf.ID, "row_moved")
	if held, err := repository.LeavesHoldingRow("row_moved"); err != nil ||
		len(held) != 1 || held[0] != firstLeaf.ID {
		t.Fatalf("LeavesHoldingRow() after mount = %#v, %v", held, err)
	}

	// Moving a Row is two leaf edits: one releases it, the other takes it.
	mountRow(t, file, repository, firstLeaf.ID, "")
	mountRow(t, file, repository, secondLeaf.ID, "row_moved")
	if held, err := repository.LeavesHoldingRow("row_moved"); err != nil ||
		len(held) != 1 || held[0] != secondLeaf.ID {
		t.Fatalf("LeavesHoldingRow() after transfer = %#v, %v", held, err)
	}
	if leaf, err := repository.LeafForOpen(firstLeaf.ID, 1); err != nil || leaf.RowID != "" {
		t.Fatalf("released leaf = %#v, %v", leaf, err)
	}
}

func TestNodesReturnsStableCurrentRoutesAfterReopen(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "database.memora")
	file, err := nativestore.Create(path, nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	repository := New(file)
	root, err := repository.CreateRoot("route_root", "db_work", "tbl_notes", "Notes")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateChild("route_zeta", root.ID, "zeta", router.KindLeaf, "Zeta"); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateChild("route_alpha", root.ID, "alpha", router.KindLeaf, "Alpha"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := nativestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	nodes, err := New(reopened).Nodes()
	if err != nil || len(nodes) != 3 || nodes[0].ID != "route_alpha" ||
		nodes[1].ID != "route_root" || nodes[2].ID != "route_zeta" {
		t.Fatalf("Nodes() = %#v, %v", nodes, err)
	}
}

func nativeRouteCode(err error, code result.Code) bool {
	var stable interface{ StableCode() string }
	return errors.As(err, &stable) && stable.StableCode() == string(code)
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

func TestNodesRejectsRouteRevisionGap(t *testing.T) {
	file, err := nativestore.Create(filepath.Join(t.TempDir(), "database.memora"), nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	repository := New(file)
	root, err := repository.CreateRoot("route_root", "db_work", "tbl_notes", "All notes")
	if err != nil {
		t.Fatal(err)
	}
	root.Revision = 3
	payload, err := encodeNode(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Put(nativestore.ObjectKindRoute, schemaVersion, nodeRecordID(root.ID, root.Revision), payload); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Nodes(); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("revision-gap Nodes() error = %v", err)
	}
}
