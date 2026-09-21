package sqlstore_test

import (
	"context"
	"testing"

	"github.com/HW-Yue/Memora/internal/recall"
)

// Both arms have their own relevance signal — the vector arm a distance, the
// keyword arm BM25 — and the fused answer orders by where each arm put a
// position, not by the position's own name. These tests pin that: an order a
// caller can trust is the whole point of fusing.

// Insertion order is not relevance. The first Row here matches the query once in
// a long title, the second twice in a short one; BM25 prefers the second, while
// the insertion order the keyword arm used to cut on prefers the first.
func TestKeywordRecallOrdersByRelevanceNotInsertion(t *testing.T) {
	h := newHarness(t)
	leaves := h.seedLeaves("alpha", "beta")
	h.insertTitle("共享关键词：这一段标题刻意写得很长，把匹配摊薄到整段文字里，"+
		"后面继续堆与查询无关的字，让词频密度降下来，好让排序规则自己说话", []string{leaves[0]})
	h.insertTitle("共享关键词 共享关键词", []string{leaves[1]})

	got := h.recallPaths(`RECALL FROM work MATCH :q LIMIT 2`, map[string]any{"q": "共享关键词"})
	if len(got) != 2 || got[0] != "root/beta" || got[1] != "root/alpha" {
		t.Fatalf("the keyword arm must rank by relevance, got %v", got)
	}
	// The listing is still bounded and still deterministic.
	if limited := h.recallPaths(`RECALL FROM work MATCH :q LIMIT 1`, map[string]any{"q": "共享关键词"}); len(limited) != 1 || limited[0] != "root/beta" {
		t.Fatalf("LIMIT must cut the ranked listing: %v", limited)
	}
}

// The vector arm's own order is distance, and a listing sorted by path instead
// would hide the neighbour that is actually nearest.
func TestVectorRecallOrdersByDistanceNotPath(t *testing.T) {
	h := newHarness(t)
	h.seedTree()
	alpha := h.insertAlongPath("alpha", pathOf("architecture", "alpha"))
	beta := h.insertAlongPath("beta", pathOf("architecture", "beta"))
	gamma := h.insertAlongPath("gamma", pathOf("architecture", "gamma"))
	h.acceptUnitVector(alpha, []float32{0, 1})
	h.acceptUnitVector(beta, []float32{-1, 0})
	h.acceptUnitVector(gamma, []float32{1, 0})

	hits, err := h.db.Rows().RecallNearest(context.Background(), "work", "", []float32{1, 0}, 3)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, hit := range hits {
		got = append(got, hit.Table+"/"+hit.Path[len(hit.Path)-1].Name)
	}
	want := []string{"notes/gamma", "notes/alpha", "notes/beta"}
	if len(got) != len(want) {
		t.Fatalf("the arm must answer with every position: %v", got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("the vector arm must rank by distance: got %v, want %v", got, want)
		}
	}
}

// A position both arms found outranks a position only one of them found, and the
// fused order is not the lexicographic one the union used to fall back on: here
// the both-arms position sits under a leaf whose name sorts last.
func TestRecallUnionFusesByRank(t *testing.T) {
	h := newHarness(t)
	h.seedTree()
	h.insertAlongPath("共享关键词 共享关键词", pathOf("architecture", "aaa"))
	bothArms := h.insertAlongPath("共享关键词：标题写长一点，让这一路的排名落在第二位，"+
		"后面继续堆无关的字", pathOf("architecture", "mmm"))
	vectorOnly := h.insertAlongPath("与查询无关的标题", pathOf("architecture", "zzz"))
	h.acceptUnitVector(bothArms, []float32{1, 0})
	h.acceptUnitVector(vectorOnly, []float32{0.9, 0.1})
	query, err := recall.EncodeVector([]float32{1, 0})
	if err != nil {
		t.Fatal(err)
	}

	union := h.recallPaths(`RECALL FROM work MATCH :q NEAREST :v LIMIT 3`,
		map[string]any{"q": "共享关键词", "v": query})
	want := []string{"root/architecture/mmm", "root/architecture/aaa", "root/architecture/zzz"}
	if len(union) != len(want) {
		t.Fatalf("the union must carry every position once: %v", union)
	}
	for index := range want {
		if union[index] != want[index] {
			t.Fatalf("rank fusion = %v, want %v", union, want)
		}
	}
	// A tie between two single-arm positions has to resolve the same way every
	// time, which is what the path order is kept for.
	for attempt := 0; attempt < 3; attempt++ {
		again := h.recallPaths(`RECALL FROM work MATCH :q NEAREST :v LIMIT 3`,
			map[string]any{"q": "共享关键词", "v": query})
		for index := range want {
			if again[index] != want[index] {
				t.Fatalf("the fused order moved between runs: %v then %v", union, again)
			}
		}
	}
}

// Fusion changes the order, not the answer's shape: the fields a caller may see
// are exactly the ones recall has always returned, with no number to threshold,
// rank by, or explain.
func TestRecallUnionAddsNoNumbersToTheAnswer(t *testing.T) {
	h := newHarness(t)
	h.seedTree()
	rowID := h.insertAlongPath("storage engine", pathOf("architecture", "sqlite"))
	h.acceptUnitVector(rowID, []float32{1, 0})
	query, err := recall.EncodeVector([]float32{1, 0})
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{
		"database": true, "table": true, "path": true, "kind": true, "object_id": true,
	}
	for _, row := range h.recallFrom(`RECALL FROM work MATCH :q NEAREST :v LIMIT 5`,
		map[string]any{"q": "storage engine", "v": query}).Rows {
		for field := range row {
			if !allowed[field] {
				t.Fatalf("the fused answer grew a field %q: %v", field, row)
			}
		}
	}
}
