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
	// Re-read rather than trusting the node the caller is holding: mounting a
	// Row advances the leaf, so a node captured before the mount is already a
	// revision behind. Every client editing a Route has to do this now.
	latest, err := repository.Get(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := file.Begin()
	if err != nil {
		t.Fatal(err)
	}
	archived := latest
	archived.Revision, archived.Deleted = latest.Revision+1, true
	if err := repository.StageNode(transaction, archived); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	return archived
}

func TestDeletedLeafIsNotOpenable(t *testing.T) {
	t.Parallel()

	file, repository, _, leaf := archiveFixture(t)
	archiveNode(t, file, repository, leaf)

	if _, _, err := repository.OpenPage(leaf.ID, "", 10); err == nil {
		t.Fatal("expected OPEN on a deleted leaf to fail")
	}
	locators, _, err := repository.InspectLeafPage(leaf.ID, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(locators) != 1 || locators[0].RowID != "row_01" {
		t.Fatalf("deleting a leaf must not corrupt its membership records, got %#v", locators)
	}
}

func TestDeletedRootLeavesTheRootListing(t *testing.T) {
	t.Parallel()

	file, repository, root, _ := archiveFixture(t)
	archiveNode(t, file, repository, root)

	if roots := repository.Roots("tbl_notes"); len(roots) != 0 {
		t.Fatalf("deleted root must not be listed, got %#v", roots)
	}
}

func TestDeletedLeafLeavesRowMemberships(t *testing.T) {
	t.Parallel()

	file, repository, _, leaf := archiveFixture(t)
	archiveNode(t, file, repository, leaf)

	memberships, err := repository.Memberships("row_01")
	if err != nil {
		t.Fatal(err)
	}
	if len(memberships) != 0 {
		t.Fatalf("membership pointing at a deleted leaf must be hidden, got %#v", memberships)
	}
	all, err := repository.MembershipsIncludingDeleted("row_01")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("membership record must survive the delete, got %#v", all)
	}
}

// TestDeletedNodeStaysDeletedAcrossReopen pins that a Route node's tombstone is
// final: an index entry carries no content of its own and is rebuilt by
// creating a new one, so no revision may follow its delete.
func TestDeletedNodeStaysDeletedAcrossReopen(t *testing.T) {
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
	deleted := archiveNode(t, file, repository, leaf)

	transaction, err := file.Begin()
	if err != nil {
		t.Fatal(err)
	}
	restored := deleted
	restored.Revision, restored.Deleted = deleted.Revision+1, false
	if err := repository.StageNode(transaction, restored); err == nil {
		t.Fatal("a deleted Route node must not accept a follow-up revision")
	}
	_ = transaction.Rollback()
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
	if !node.Deleted {
		t.Fatalf("the node must still be deleted after reopen: %#v", node)
	}
	if children := after.Children(root.ID); len(children) != 0 {
		t.Fatalf("a deleted node must leave the child listing, got %#v", children)
	}
}

func TestDeletedNodeRejectsEveryFollowUpRevision(t *testing.T) {
	t.Parallel()

	file, repository, _, leaf := archiveFixture(t)
	deleted := archiveNode(t, file, repository, leaf)

	transaction, err := file.Begin()
	if err != nil {
		t.Fatal(err)
	}
	edited := deleted
	edited.Revision, edited.Purpose = deleted.Revision+1, "改写用途"
	if err := repository.StageNode(transaction, edited); err == nil {
		t.Fatal("expected an edit on a deleted node to be refused")
	}
	_ = transaction.Rollback()
}
