package nativerouter

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/router"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

// The measurement E3 stage 7 is gated on: what a Route path costs to read
// today (a stored field) against what it would cost computed from ParentID,
// and what the stored field costs to maintain when a branch is renamed.
//
// The shape comes from the benchmark corpus (docs/development/
// route-benchmark-corpus-v1.md): depth up to 6, fanout capped at 12 (F223).

func buildTree(tb testing.TB, depth, fanout int) (*Repository, []string) {
	tb.Helper()
	file, err := nativestore.Create(filepath.Join(tb.TempDir(), "routes.memora"), nativestore.FileKindDatabase)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = file.Close() })
	repository := New(file)
	root, err := repository.CreateRoot("route_root", "db_work", "tbl_notes", "Root")
	if err != nil {
		tb.Fatal(err)
	}
	leaves := []string{}
	counter := 0
	var grow func(parentID string, level int)
	grow = func(parentID string, level int) {
		for index := 0; index < fanout; index++ {
			counter++
			id := fmt.Sprintf("route_%06d", counter)
			kind := router.KindBranch
			if level == depth {
				kind = router.KindLeaf
			}
			if _, err := repository.CreateChild(id, parentID, fmt.Sprintf("n%d", index), kind, "Node"); err != nil {
				tb.Fatal(err)
			}
			if kind == router.KindLeaf {
				leaves = append(leaves, id)
				continue
			}
			grow(id, level+1)
		}
	}
	grow(root.ID, 1)
	return repository, leaves
}

// traceFromParents computes a node's path the way stage 7 proposes to: walk
// ParentID to the root and join the names.
func traceFromParents(repository *Repository, leafID string) (string, error) {
	names := []string{}
	id := leafID
	for id != "" {
		node, err := repository.Get(id)
		if err != nil {
			return "", err
		}
		if node.Kind == router.KindRoot {
			break
		}
		names = append([]string{node.Name}, names...)
		id = node.ParentID
	}
	return "/" + strings.Join(names, "/"), nil
}

// TestTraceFromParentsMatchesTheStoredPath pins Node.Path as a pure cache:
// ParentID is the fact, Path is its materialisation, and the two never
// disagree. E3 stage 7 considered deleting Path and computing the trace
// instead; the measurement said no (docs/storage/leaf-rowid-v1.md §7.3), so
// this test is what keeps the option open — the day the three derived indexes
// stop materialising the path too, Path can go without an archaeology dig to
// prove it was derivable.
func TestTraceFromParentsMatchesTheStoredPath(t *testing.T) {
	t.Parallel()

	repository, leaves := buildTree(t, 4, 4)
	for _, leafID := range leaves {
		stored, err := repository.Get(leafID)
		if err != nil {
			t.Fatal(err)
		}
		computed, err := traceFromParents(repository, leafID)
		if err != nil {
			t.Fatal(err)
		}
		if computed != stored.Path {
			t.Fatalf("computed trace %q != stored path %q", computed, stored.Path)
		}
	}
	t.Logf("nodes=%d depth=4 fanout=4", len(leaves))
}

func BenchmarkStoredPathRead(b *testing.B) {
	repository, leaves := buildTree(b, 4, 4)
	leafID := leaves[len(leaves)/2]
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		node, err := repository.Get(leafID)
		if err != nil || node.Path == "" {
			b.Fatal(err)
		}
	}
}

func BenchmarkComputedTraceRead(b *testing.B) {
	repository, leaves := buildTree(b, 4, 4)
	leafID := leaves[len(leaves)/2]
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := traceFromParents(repository, leafID); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStoredPathReadLargeTree(b *testing.B) {
	repository, leaves := buildTree(b, 4, 6)
	leafID := leaves[len(leaves)/2]
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		node, err := repository.Get(leafID)
		if err != nil || node.Path == "" {
			b.Fatal(err)
		}
	}
}

func BenchmarkComputedTraceReadLargeTree(b *testing.B) {
	repository, leaves := buildTree(b, 4, 6)
	leafID := leaves[len(leaves)/2]
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := traceFromParents(repository, leafID); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkRepathSubtree measures what a stored Path costs to maintain: a
// RENAME one level below the root rewrites every descendant.
func BenchmarkRepathSubtree(b *testing.B) {
	repository, _ := buildTree(b, 4, 4)
	nodes, err := repository.Nodes()
	if err != nil {
		b.Fatal(err)
	}
	var target router.Node
	for _, node := range nodes {
		if node.ParentID == "route_root" {
			target = node
			break
		}
	}
	descendants := 0
	for _, node := range nodes {
		if strings.HasPrefix(node.Path, target.Path+"/") {
			descendants++
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// The scan a repath has to do before it can write anything.
		all, listErr := repository.Nodes()
		if listErr != nil {
			b.Fatal(listErr)
		}
		touched := 0
		for _, node := range all {
			if strings.HasPrefix(node.Path, target.Path+"/") {
				touched++
			}
		}
		if touched != descendants {
			b.Fatalf("touched %d, want %d", touched, descendants)
		}
	}
	b.ReportMetric(float64(descendants), "descendants/rename")
}
