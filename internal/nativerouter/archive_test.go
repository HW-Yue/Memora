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
	mountRow(t, file, repository, leaf.ID, "row_01")
	return file, repository, root, leaf
}

// mountRow points a leaf at a Row. Only the leaf side of the mount lives here;
// the Row side is a field on the Row, which this package cannot reach.
func mountRow(t *testing.T, file *nativestore.File, repository *Repository, leafID, rowID string) {
	t.Helper()
	leaf, err := repository.Get(leafID)
	if err != nil {
		t.Fatal(err)
	}
	leaf.RowID = rowID
	leaf.Revision++
	transaction, err := file.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = transaction.Rollback() }()
	if err := repository.StageNode(transaction, leaf); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
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

	if _, err := repository.LeafForOpen(leaf.ID, 10); err == nil {
		t.Fatal("expected OPEN on a deleted leaf to fail")
	}
	tombstoned, err := repository.Get(leaf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if tombstoned.RowID != "row_01" {
		t.Fatalf("deleting a leaf must not erase the Row it held, got %#v", tombstoned)
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

func TestDeletedLeafStopsHoldingItsRow(t *testing.T) {
	t.Parallel()

	file, repository, _, leaf := archiveFixture(t)
	archiveNode(t, file, repository, leaf)

	// The tombstoned leaf still records which Row it held — the record is
	// untouched — but it no longer counts as holding it.
	held, err := repository.LeavesHoldingRow("row_01")
	if err != nil {
		t.Fatal(err)
	}
	if len(held) != 0 {
		t.Fatalf("a deleted leaf must not hold a Row, got %#v", held)
	}
	tombstoned, err := repository.Get(leaf.ID)
	if err != nil || tombstoned.RowID != "row_01" {
		t.Fatalf("the leaf record must survive the delete, got %#v, %v", tombstoned, err)
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
