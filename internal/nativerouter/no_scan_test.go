package nativerouter

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/router"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

// sweepsToGetNode measures the full-file sweeps one Route node read costs in a
// tree of the given shape.
func sweepsToGetNode(t *testing.T, depth, fanout int) uint64 {
	t.Helper()
	repository, leaves := buildTree(t, depth, fanout)
	target := leaves[len(leaves)/2]
	before := repository.file.Enumerations()
	node, err := repository.Get(target)
	if err != nil || node.ID != target {
		t.Fatalf("Get(%s) = %#v, %v", target, node, err)
	}
	return repository.file.Enumerations() - before
}

// TestReadingOneRouteNodeCostsTheSameAtAnyTreeSize pins the property that makes
// route_paths affordable: resolving one Route node by ID must not depend on how
// many Route nodes exist. Every SELECT resolves one node per leaf per Row, so a
// read that sweeps the file turns a result page into a full-database scan.
func TestReadingOneRouteNodeCostsTheSameAtAnyTreeSize(t *testing.T) {
	t.Parallel()

	small, large := sweepsToGetNode(t, 2, 3), sweepsToGetNode(t, 4, 5)
	if small != 0 || large != 0 {
		t.Fatalf("Get swept the file %d times in a small tree and %d in a large one", small, large)
	}
}

// TestReadingOneRouteNodeCostsTheSameAtAnyRevisionDepth pins the other axis: a
// leaf that has been mounted, renamed and re-mounted has several revisions, and
// finding the latest must not cost more than a bounded probe.
func TestReadingOneRouteNodeCostsTheSameAtAnyRevisionDepth(t *testing.T) {
	t.Parallel()

	file, err := nativestore.Create(filepath.Join(t.TempDir(), "routes.memora"), nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	repository := New(file)
	root, err := repository.CreateRoot("route_root", "db_work", "tbl_notes", "Root")
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := repository.CreateChild("route_leaf", root.ID, "leaf", router.KindLeaf, "Leaf")
	if err != nil {
		t.Fatal(err)
	}
	for revision := 0; revision < 30; revision++ {
		leaf.Revision++
		leaf.Synopsis = fmt.Sprintf("revision %d", leaf.Revision)
		transaction, beginErr := file.Begin()
		if beginErr != nil {
			t.Fatal(beginErr)
		}
		if err := repository.StageNode(transaction, leaf); err != nil {
			t.Fatal(err)
		}
		if err := transaction.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	before := file.Enumerations()
	node, err := repository.Get(leaf.ID)
	if err != nil || node.Revision != leaf.Revision {
		t.Fatalf("Get() = %#v, %v", node, err)
	}
	if swept := file.Enumerations() - before; swept != 0 {
		t.Fatalf("reading a 31-revision Route node swept the file %d times", swept)
	}
}
