package nativerouter

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/HW-Yue/Memora/internal/store/objectindex"
	"github.com/HW-Yue/Memora/internal/store/page"
	"github.com/HW-Yue/Memora/internal/store/treecommit"
	"github.com/HW-Yue/Memora/internal/store/wal"
)

// TestWalkingRoutesThroughTheObjectsTreeNeverSweepsTheFile is E7 stage 2's read
// gate.
//
// Resolving one node by ID already cost nothing (see no_scan_test.go), but
// everything that walks the tree — Roots, Children, Nodes, LeavesHoldingRow, and
// so SHOW UNDER and route_paths above them — went through nodes(), which listed
// every Route record the Database had ever written. That is a full pass over the
// record log's in-memory record table, and it grows with how many revisions have
// been written, not with how many Routes exist.
//
// Reading through the objects Tree replaces it with a range scan over one kind.
// The gate is both halves: the answers must be the ones the record log gives,
// and getting them must cost zero sweeps.
func TestWalkingRoutesThroughTheObjectsTreeNeverSweepsTheFile(t *testing.T) {
	records, leaves := buildTree(t, 3, 3)
	// Give one leaf a Row so the Row-to-leaves direction is exercised too.
	mounted := leaves[len(leaves)/2]
	node, err := records.Get(mounted)
	if err != nil {
		t.Fatal(err)
	}
	node.RowID, node.Revision = "row_mounted", node.Revision+1
	transaction, err := records.file.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := records.StageNode(transaction, node); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}

	tree := NewWithObjects(records.file, staticObjects{newRouteObjects(t, records)})

	parent := leafParentOf(t, records, mounted)
	for name, read := range map[string]func(*Repository) any{
		"Roots":    func(r *Repository) any { return r.Roots("tbl_notes") },
		"Children": func(r *Repository) any { return r.Children(parent) },
		"Nodes": func(r *Repository) any {
			values, err := r.Nodes()
			if err != nil {
				t.Fatal(err)
			}
			return values
		},
		"LeavesHoldingRow": func(r *Repository) any {
			values, err := r.LeavesHoldingRow("row_mounted")
			if err != nil {
				t.Fatal(err)
			}
			return values
		},
		"Get": func(r *Repository) any {
			value, err := r.Get(mounted)
			if err != nil {
				t.Fatal(err)
			}
			return value
		},
	} {
		want := read(records)
		before := records.file.Enumerations()
		got := read(tree)
		if swept := records.file.Enumerations() - before; swept != 0 {
			t.Fatalf("%s through the objects Tree swept the file %d times", name, swept)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s through the objects Tree = %#v, want %#v", name, got, want)
		}
	}
}

func leafParentOf(t *testing.T, repository *Repository, leafID string) string {
	t.Helper()
	node, err := repository.Get(leafID)
	if err != nil {
		t.Fatal(err)
	}
	return node.ParentID
}

// newRouteObjects builds an objects Tree holding the repository's current
// Routes, the way a generation build seeds one.
func newRouteObjects(t *testing.T, repository *Repository) *objectindex.Index {
	t.Helper()
	directory := t.TempDir()
	set, err := wal.CreateSegmentSet(filepath.Join(directory, "wal"), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = set.Close() })
	manager, err := page.Create(filepath.Join(directory, "objects.pages"), routeTestSpaceID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	runtime, _, err := treecommit.OpenRuntime(set, manager, treecommit.RuntimeConfig{
		SpaceID: routeTestSpaceID, Capacity: 256, OldFrames: 128,
	})
	if err != nil {
		t.Fatal(err)
	}
	index, err := objectindex.Open(runtime)
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := repository.nodes()
	if err != nil {
		t.Fatal(err)
	}
	stored := make([]objectindex.Record, 0, len(nodes))
	for _, value := range nodes {
		body, err := EncodeNode(value)
		if err != nil {
			t.Fatal(err)
		}
		stored = append(stored, objectindex.Record{
			Kind: ObjectKind, ID: value.ID, Revision: value.Revision, Body: body,
		})
	}
	if _, err := index.Bootstrap(1, stored); err != nil {
		t.Fatal(err)
	}
	return index
}

const routeTestSpaceID = uint64(0x4d454d4f424a)

// staticObjects is an ObjectSource over one Tree. Production hands out the
// Authority's current generation, which a COW rebuild can replace; a test that
// never rebuilds has one Tree for its lifetime.
type staticObjects struct{ index *objectindex.Index }

func (source staticObjects) RouteObjects() *objectindex.Index { return source.index }
