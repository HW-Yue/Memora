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
	firstPage, childrenPage, err := repository.ShowUnderPage(root.ID, "", 1)
	if err != nil || len(firstPage) != 1 || childrenPage.Snapshot == "" || childrenPage.NextCursor == "" {
		t.Fatalf("ShowUnderPage(first) = %#v, %#v, %v", firstPage, childrenPage, err)
	}
	secondPage, continued, err := repository.ShowUnderPage(root.ID, childrenPage.NextCursor, 1)
	if err != nil || len(secondPage) != 1 || continued.NextCursor != "" ||
		continued.Snapshot != childrenPage.Snapshot || secondPage[0].ID == firstPage[0].ID {
		t.Fatalf("ShowUnderPage(second) = %#v, %#v, %v", secondPage, continued, err)
	}
	locators, locatorPage, err := repository.OpenPage(decisions.ID, "", 1)
	if err != nil || len(locators) != 1 || locatorPage.Snapshot == "" || locatorPage.NextCursor != "" {
		t.Fatalf("OpenPage(single Row) = %#v, %#v, %v", locators, locatorPage, err)
	}
	if _, err := repository.CreateChild("route_extra", root.ID, "额外", router.KindLeaf, "额外知识"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.ShowUnderPage(root.ID, childrenPage.NextCursor, 1); !nativeRouteCode(err, result.CodeRevisionConflict) {
		t.Fatalf("changed native children error = %v", err)
	}
	memberships, err := repository.Memberships(locator.RowID)
	if err != nil || len(memberships) != 2 || memberships[0].LeafID == memberships[1].LeafID {
		t.Fatalf("Memberships() = %#v, %v", memberships, err)
	}
}

func TestOpenRejectsLegacyLeafWithMultipleLiveRows(t *testing.T) {
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
	leaf, err := repository.CreateChild("route_leaf", root.ID, "one", router.KindLeaf, "One Row")
	if err != nil {
		t.Fatal(err)
	}
	first := router.Membership{LeafID: leaf.ID, MembershipRevision: 1, Locator: router.Locator{
		DatabaseID: "db_work", TableID: "tbl_notes", RowID: "row_first", Revision: 1,
	}}
	if err := repository.Attach(first.LeafID, first.Locator, first.MembershipRevision); err != nil {
		t.Fatal(err)
	}
	second := first
	second.RowID = "row_second"
	payload, err := encodeMembership(second)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Put(nativestore.ObjectKindRouteMembership, schemaVersion, membershipRecordID(second), payload); err != nil {
		t.Fatal(err)
	}

	if _, _, err := repository.OpenPage(leaf.ID, "", 10); !nativeRouteCode(err, result.CodeConstraint) {
		t.Fatalf("OPEN legacy multi-Row leaf error = %v", err)
	}
	deleted := first
	deleted.MembershipRevision, deleted.Deleted = 2, true
	if err := repository.ValidateMembershipChanges([]router.Membership{deleted}); err != nil {
		t.Fatalf("monotonic legacy repair validation = %v", err)
	}
	transaction, err := file.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.StageMembership(transaction, deleted); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	locators, _, err := repository.OpenPage(leaf.ID, "", 1)
	if err != nil || len(locators) != 1 || locators[0].RowID != second.RowID {
		t.Fatalf("OPEN after monotonic legacy repair = %#v, %v", locators, err)
	}
}

func TestValidateMembershipChangesAllowsAtomicLeafTransfer(t *testing.T) {
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
	leaf, err := repository.CreateChild("route_leaf", root.ID, "exact", router.KindLeaf, "Exact Row")
	if err != nil {
		t.Fatal(err)
	}
	first := router.Membership{LeafID: leaf.ID, MembershipRevision: 1, Locator: router.Locator{
		DatabaseID: "db_work", TableID: "tbl_notes", RowID: "row_first", Revision: 1,
	}}
	if err := repository.Attach(first.LeafID, first.Locator, first.MembershipRevision); err != nil {
		t.Fatal(err)
	}
	deleted := first
	deleted.MembershipRevision, deleted.Deleted = 2, true
	second := first
	second.RowID = "row_second"
	changes := []router.Membership{deleted, second}
	if err := repository.ValidateMembershipChanges(changes); err != nil {
		t.Fatalf("atomic transfer validation = %v", err)
	}
	transaction, err := file.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for _, membership := range changes {
		if err := repository.StageMembership(transaction, membership); err != nil {
			t.Fatal(err)
		}
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	locators, _, err := repository.OpenPage(leaf.ID, "", 1)
	if err != nil || len(locators) != 1 || locators[0].RowID != second.RowID {
		t.Fatalf("OPEN after atomic transfer = %#v, %v", locators, err)
	}
}

func TestReverseMembershipFollowsSoftDeleteAndTransfer(t *testing.T) {
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
	locator := router.Locator{DatabaseID: "db_work", TableID: "tbl_notes", RowID: "row_moved", Revision: 1}
	if err := repository.Attach(firstLeaf.ID, locator, 1); err != nil {
		t.Fatal(err)
	}
	if memberships, err := repository.Memberships(locator.RowID); err != nil || len(memberships) != 1 || memberships[0].LeafID != firstLeaf.ID {
		t.Fatalf("reverse Memberships after attach = %#v, %v", memberships, err)
	}
	deleted := router.Membership{LeafID: firstLeaf.ID, MembershipRevision: 2, Deleted: true, Locator: locator}
	replaced := router.Membership{LeafID: secondLeaf.ID, MembershipRevision: 1, Locator: locator}
	transaction, err := file.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.StageMembership(transaction, deleted); err != nil {
		t.Fatal(err)
	}
	if err := repository.StageMembership(transaction, replaced); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	if memberships, err := repository.Memberships(locator.RowID); err != nil || len(memberships) != 1 || memberships[0].LeafID != secondLeaf.ID {
		t.Fatalf("reverse Memberships after transfer = %#v, %v", memberships, err)
	}
	if includingDeleted, err := repository.MembershipsIncludingDeleted(locator.RowID); err != nil ||
		len(includingDeleted) != 2 {
		t.Fatalf("reverse MembershipsIncludingDeleted after transfer = %#v, %v", includingDeleted, err)
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
