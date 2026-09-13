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
// many Route nodes exist.
//
// It used to assert zero passes over the record log. That was true because the
// log carried a process-resident map of where every record lived, and E8
// stage 3 deleted that map — it was the thing that grew with how many times the
// Database had ever been written to. Without it there is no index over the log,
// so reading the log is a pass over the log, and zero is only reachable by
// putting the map back.
//
// This is the fallback path, which a Database with no generation takes. The
// production path reads the objects Tree and touches the log not at all; the
// gate for that is pagestoremigration's TestALiveWorkloadNeverSweepsTheRecordFile.
// What has to hold here is that the cost is flat.
func TestReadingOneRouteNodeCostsTheSameAtAnyTreeSize(t *testing.T) {
	t.Parallel()

	small, large := sweepsToGetNode(t, 2, 3), sweepsToGetNode(t, 4, 5)
	if small != 1 || large != 1 {
		t.Fatalf("Get took %d passes in a small tree and %d in a large one, want 1", small, large)
	}
}

// TestReadingOneRouteNodeCostsTheSameAtAnyRevisionDepth pins the other axis: a
// leaf that has been mounted, renamed and re-mounted has several revisions, and
// finding the latest must cost the same as finding the only revision of a leaf
// that has never moved. Probing revision 1, 2, 3 … was one pass each once the
// log lost its resident map; one pass that keeps this node's records is flat.
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
	if swept := file.Enumerations() - before; swept != 1 {
		t.Fatalf("reading a 31-revision Route node took %d passes, want 1", swept)
	}
}
