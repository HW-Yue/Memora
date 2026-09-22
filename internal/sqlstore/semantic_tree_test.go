package sqlstore_test

import (
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/recall"
	"github.com/HW-Yue/Memora/internal/result"
)

// One tree, four walks. This is the fixture the gate runs instead of a provider:
// the same shape as scripts/demo-recall.sh (three domains, one fact per leaf),
// with fixed vectors so "the nearest one" is a property of the numbers rather
// than of a model's mood.
//
// Navigation, keyword recall, vector recall and the union are four ways to the
// same place — a position — and the only way back to a fact is SELECT. Having
// them all run over one tree is what makes a change to any of them visible
// against the others.
type treeFact struct {
	branch string
	leaf   string
	fact   string
	vector []float32
}

func semanticTree() []treeFact {
	return []treeFact{
		{"技术", "存储引擎", "自研页式引擎的维护成本高于收益，页格式、WAL 与崩溃恢复交给 SQLite。", []float32{1, 0, 0, 0}},
		{"技术", "向量检索", "向量只用来定位，从不产出事实；召回返回路径而不返回分数。", []float32{0, 1, 0, 0}},
		{"技术", "Go", "cgo 让 Go 能编进 C 扩展，可选模块由构建标签控制。", []float32{0, 0, 1, 0}},
		{"生活", "健康", "每周三次慢跑，睡眠优先于加班。", []float32{0, 0, 0, 1}},
		{"生活", "阅读", "《设计数据密集型应用》读到复制与分区。", []float32{0.6, 0.4, 0, 0}},
		{"工作", "Memora", "个人语义数据库：Agent 自主建模，一切都落成普通 SQLite 表。", []float32{0.2, 0.2, 0, 0.9}},
	}
}

// seedSemanticTree builds the tree and returns the Row ID of each fact, keyed by
// the fact's text.
func (h *harness) seedSemanticTree() map[string]string {
	h.t.Helper()
	h.seedTree()
	rows := map[string]string{}
	for _, entry := range semanticTree() {
		rowID := h.insertAlongPath(entry.fact, pathOf(entry.branch, entry.leaf))
		h.acceptUnitVector(rowID, entry.vector)
		rows[entry.fact] = rowID
	}
	return rows
}

func (h *harness) vectorOf(fact string) []float32 {
	h.t.Helper()
	for _, entry := range semanticTree() {
		if entry.fact == fact {
			return entry.vector
		}
	}
	h.t.Fatalf("no such fact in the fixture: %s", fact)
	return nil
}

func (h *harness) factFor(rowID string) string {
	h.t.Helper()
	rows := h.run(`SELECT title FROM work.notes WHERE row_id = :row LIMIT 1`,
		map[string]any{"row": rowID}, executor.MutationOptions{}).Rows
	if len(rows) != 1 {
		h.t.Fatalf("row %s is not readable", rowID)
	}
	return text(rows[0]["title"])
}

// Walk one: pick a layer, then a node, then the leaf, then read the fact.
func TestTheFixtureTreeIsNavigableLayerByLayer(t *testing.T) {
	h := newHarness(t)
	h.seedSemanticTree()

	domains := map[string]string{}
	for _, row := range h.rootChildren() {
		domains[text(row["name"])] = text(row["route_id"])
	}
	if len(domains) != 3 {
		t.Fatalf("the tree has three domains: %v", domains)
	}

	children := map[string]string{}
	for _, row := range h.run(`SHOW ROUTES UNDER :parent`,
		map[string]any{"parent": domains["技术"]}, executor.MutationOptions{}).Rows {
		children[text(row["name"])] = text(row["route_id"])
	}
	if len(children) != 3 {
		t.Fatalf("技术 holds three leaves here: %v", children)
	}

	leaf := children["存储引擎"]
	locator := h.run(`OPEN ROUTE :leaf LIMIT 1`, map[string]any{"leaf": leaf}, executor.MutationOptions{})
	if len(locator.Rows) != 1 {
		t.Fatalf("a leaf locates exactly one Row: %+v", locator.Rows)
	}
	fact := h.factFor(text(locator.Rows[0]["row_id"]))
	if !strings.HasPrefix(fact, "自研页式引擎") {
		t.Fatalf("the leaf held %q", fact)
	}
}

// Walk two: remember a word.
func TestTheFixtureTreeAnswersByKeyword(t *testing.T) {
	h := newHarness(t)
	h.seedSemanticTree()
	paths := h.recallPaths(`RECALL FROM work MATCH :q LIMIT 10`, map[string]any{"q": "SQLite"})
	if !containsPath(paths, "root/技术/存储引擎") {
		t.Fatalf("the keyword walk must find the SQLite fact: %v", paths)
	}
}

// Walk three: remember only the meaning. The query is the stored vector nudged
// off centre, so the nearest is a property of the numbers, not of an exact hit.
func TestTheFixtureTreeAnswersByMeaning(t *testing.T) {
	h := newHarness(t)
	rows := h.seedSemanticTree()
	target := h.vectorOf("《设计数据密集型应用》读到复制与分区。")
	query, err := recall.EncodeVector([]float32{target[0] + 0.05, target[1], target[2], target[3]})
	if err != nil {
		t.Fatal(err)
	}
	hits := h.recallFrom(`RECALL FROM work NEAREST :v LIMIT 1`, map[string]any{"v": query})
	if len(hits.Rows) != 1 {
		t.Fatalf("the vector walk must answer with one position: %+v", hits.Rows)
	}
	if got, want := text(hits.Rows[0]["object_id"]), rows["《设计数据密集型应用》读到复制与分区。"]; got != want {
		t.Fatalf("nearest = %s, want %s", got, want)
	}
}

// Walk four: both at once.
//
// LIMIT truncates the merged listing, so "the union is wider" is not something a
// small limit can show. What it can show, and what matters, is that the two arms
// overlap here — 存储引擎 mentions SQLite and is also the second nearest to the
// nudged reading vector — and that a position both arms found is still one
// position in the result. The wide call is the one that can see the whole tree.
func TestTheFixtureTreeMergesBothWalks(t *testing.T) {
	h := newHarness(t)
	rows := h.seedSemanticTree()
	query, err := recall.EncodeVector(h.vectorOf("《设计数据密集型应用》读到复制与分区。"))
	if err != nil {
		t.Fatal(err)
	}
	reading := rows["《设计数据密集型应用》读到复制与分区。"]
	sqlite := rows["自研页式引擎的维护成本高于收益，页格式、WAL 与崩溃恢复交给 SQLite。"]

	keywordHits := h.recallFrom(`RECALL FROM work MATCH :q LIMIT 2`, map[string]any{"q": "SQLite"})
	vectorHits := h.recallFrom(`RECALL FROM work NEAREST :v LIMIT 2`, map[string]any{"v": query})
	if !holdsRow(keywordHits.Rows, sqlite) {
		t.Fatalf("the keyword arm must find the SQLite fact: %+v", keywordHits.Rows)
	}
	if !holdsRow(vectorHits.Rows, reading) || !holdsRow(vectorHits.Rows, sqlite) {
		t.Fatalf("the vector arm's two nearest are 阅读 and 存储引擎: %+v", vectorHits.Rows)
	}

	// Full width, so the merge is what is being read rather than the truncation.
	union := h.recallFrom(`RECALL FROM work MATCH :q NEAREST :v LIMIT 10`,
		map[string]any{"q": "SQLite", "v": query})
	seen := map[string]int{}
	for _, row := range union.Rows {
		seen[text(row["object_id"])]++
	}
	if len(seen) != len(semanticTree()) {
		t.Fatalf("full width must list every position once: %d of %d", len(seen), len(semanticTree()))
	}
	if seen[sqlite] != 1 {
		t.Fatalf("a position both arms found appeared %d times", seen[sqlite])
	}
	for rowID, count := range seen {
		if count != 1 {
			t.Fatalf("position %s appeared %d times", rowID, count)
		}
	}
}

func holdsRow(rows []result.Row, rowID string) bool {
	for _, row := range rows {
		if text(row["object_id"]) == rowID {
			return true
		}
	}
	return false
}

func containsPath(paths []string, want string) bool {
	for _, path := range paths {
		if path == want {
			return true
		}
	}
	return false
}
