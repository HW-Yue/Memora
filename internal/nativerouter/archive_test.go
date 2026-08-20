package nativerouter

import (
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/router"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

func archiveFixture(t *testing.T) (*nativestore.File, *Repository, router.Node, router.Node) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "database.memora")
	file, err := nativestore.Create(path, nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	repository := New(file)
	root, err := repository.CreateRoot("route_root", "db_work", "tbl_notes", "笔记语义入口")
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := repository.CreateChild("route_leaf", root.ID, "决策", router.KindLeaf, "产品决策 RowID")
	if err != nil {
		t.Fatal(err)
	}
	locator := router.Locator{DatabaseID: "db_work", TableID: "tbl_notes", RowID: "row_01", Revision: 3}
	if err := repository.Attach(leaf.ID, locator, 1); err != nil {
		t.Fatal(err)
	}
	return file, repository, root, leaf
}

func archiveNode(t *testing.T, file *nativestore.File, repository *Repository, node router.Node) router.Node {
	t.Helper()
	transaction, err := file.Begin()
	if err != nil {
		t.Fatal(err)
	}
	archived := node
	archived.Revision, archived.Deleted = node.Revision+1, true
	if err := repository.StageNode(transaction, archived); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	return archived
}

func TestArchivedLeafIsNotOpenableAndKeepsItsMemberships(t *testing.T) {
	t.Parallel()

	file, repository, _, leaf := archiveFixture(t)
	archiveNode(t, file, repository, leaf)

	if _, _, err := repository.OpenPage(leaf.ID, "", 10); err == nil {
		t.Fatal("expected OPEN on an archived leaf to fail")
	}
	locators, _, err := repository.InspectLeafPage(leaf.ID, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(locators) != 1 || locators[0].RowID != "row_01" {
		t.Fatalf("archiving a leaf must not destroy its memberships, got %#v", locators)
	}
}

func TestArchivedRootLeavesTheRootListing(t *testing.T) {
	t.Parallel()

	file, repository, root, _ := archiveFixture(t)
	archiveNode(t, file, repository, root)

	if roots := repository.Roots("tbl_notes"); len(roots) != 0 {
		t.Fatalf("archived root must not be listed, got %#v", roots)
	}
}

func TestArchivedLeafLeavesRowMemberships(t *testing.T) {
	t.Parallel()

	file, repository, _, leaf := archiveFixture(t)
	archiveNode(t, file, repository, leaf)

	memberships, err := repository.Memberships("row_01")
	if err != nil {
		t.Fatal(err)
	}
	if len(memberships) != 0 {
		t.Fatalf("membership pointing at an archived leaf must be hidden, got %#v", memberships)
	}
	all, err := repository.MembershipsIncludingDeleted("row_01")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("membership must survive archiving, got %#v", all)
	}
}

func TestArchivedNodeCanBeRestoredAndSurvivesReopen(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "database.memora")
	file, err := nativestore.Create(path, nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	repository := New(file)
	root, err := repository.CreateRoot("route_root", "db_work", "tbl_notes", "笔记语义入口")
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := repository.CreateChild("route_leaf", root.ID, "决策", router.KindLeaf, "产品决策 RowID")
	if err != nil {
		t.Fatal(err)
	}
	locator := router.Locator{DatabaseID: "db_work", TableID: "tbl_notes", RowID: "row_01", Revision: 3}
	if err := repository.Attach(leaf.ID, locator, 1); err != nil {
		t.Fatal(err)
	}
	archived := archiveNode(t, file, repository, leaf)

	transaction, err := file.Begin()
	if err != nil {
		t.Fatal(err)
	}
	restored := archived
	restored.Revision, restored.Deleted = archived.Revision+1, false
	if err := repository.StageNode(transaction, restored); err != nil {
		t.Fatalf("restoring an archived node must be allowed: %v", err)
	}
	if err := transaction.Commit(); err != nil {
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
	after := New(reopened)
	node, err := after.Get(leaf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if node.Deleted || node.Revision != 3 {
		t.Fatalf("restored node state is wrong: %#v", node)
	}
	locators, _, err := after.OpenPage(leaf.ID, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(locators) != 1 || locators[0].RowID != "row_01" {
		t.Fatalf("restore must bring memberships back, got %#v", locators)
	}
	memberships, err := after.Memberships("row_01")
	if err != nil {
		t.Fatal(err)
	}
	if len(memberships) != 1 {
		t.Fatalf("restore must bring reverse memberships back, got %#v", memberships)
	}
}

func TestArchivedNodeRejectsEditsOtherThanRestore(t *testing.T) {
	t.Parallel()

	file, repository, _, leaf := archiveFixture(t)
	archived := archiveNode(t, file, repository, leaf)

	transaction, err := file.Begin()
	if err != nil {
		t.Fatal(err)
	}
	edited := archived
	edited.Revision, edited.Purpose = archived.Revision+1, "改写用途"
	if err := repository.StageNode(transaction, edited); err == nil {
		t.Fatal("expected an edit on an archived node to be refused")
	}
	_ = transaction.Rollback()
}
