package sqlstore_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/recall"
	"github.com/HW-Yue/Memora/internal/result"
)

// Keyword recall answers one question: where does a match sit in the semantic
// tree. The tests below are as much about what the answer must not contain as
// about what it finds.

func (h *harness) recallFrom(source string, named map[string]any) result.StatementResult {
	h.t.Helper()
	return h.run(source, named, executor.MutationOptions{})
}

func (h *harness) recallPaths(source string, named map[string]any) []string {
	h.t.Helper()
	paths := []string{}
	for _, row := range h.recallFrom(source, named).Rows {
		segments := []recall.Segment{}
		if err := json.Unmarshal([]byte(text(row["path"])), &segments); err != nil {
			h.t.Fatalf("path is not JSON: %v (%v)", row["path"], err)
		}
		labels := []string{}
		for _, segment := range segments {
			if segment.RouteID == "" {
				h.t.Fatalf("every segment must carry the id navigation needs: %+v", segments)
			}
			labels = append(labels, segment.Name)
		}
		paths = append(paths, strings.Join(labels, "/"))
	}
	return paths
}

func TestKeywordRecallFindsARowWithoutAPath(t *testing.T) {
	h := newHarness(t)
	leaves := h.seedLeaves("one", "two")
	first := h.insertTitle("为什么选 SQLite 作为存储引擎", []string{leaves[0]})
	h.insertTitle("完全无关的另一条事实", []string{leaves[1]})

	hits := h.recallFrom(`RECALL FROM work MATCH :q LIMIT 10`, map[string]any{"q": "存储引擎"})
	if len(hits.Rows) != 1 {
		t.Fatalf("hits = %v", hits.Rows)
	}
	row := hits.Rows[0]
	if text(row["object_id"]) != first {
		t.Fatalf("object_id = %v, want %q", row["object_id"], first)
	}
	if text(row["database"]) != "work" || text(row["table"]) != "notes" || text(row["kind"]) != "leaf" {
		t.Fatalf("hit = %v", row)
	}
	// The path is what makes the hit navigable, and it is all a caller gets.
	if paths := h.recallPaths(`RECALL FROM work MATCH :q LIMIT 10`, map[string]any{"q": "存储引擎"}); len(paths) != 1 || paths[0] != "root/one" {
		t.Fatalf("paths = %v", paths)
	}

	// Nothing about relevance, and no content, may leave the engine.
	for _, forbidden := range []string{"score", "rank", "reason", "distance", "matched_fields", "payload", "summary"} {
		if _, present := row[forbidden]; present {
			t.Fatalf("recall must not report %q: %v", forbidden, row)
		}
	}
	for _, column := range hits.Columns {
		if column.Name == "score" || column.Name == "rank" || column.Name == "reason" {
			t.Fatalf("recall must not expose %q", column.Name)
		}
	}
}

func TestKeywordRecallScopeAndOrder(t *testing.T) {
	h := newHarness(t)
	leaves := h.seedLeaves("alpha", "beta")
	h.insertTitle("共享关键词第一条", []string{leaves[0]})
	h.insertTitle("共享关键词第二条", []string{leaves[1]})

	// A database-wide recall reaches every Table; a Table-scoped one does not
	// reach past it.
	wide := h.recallPaths(`RECALL FROM work MATCH :q LIMIT 10`, map[string]any{"q": "共享关键词"})
	if len(wide) != 2 {
		t.Fatalf("database recall = %v", wide)
	}
	// Both Rows match the same term in the same words, so BM25 ties and the unit
	// number decides — the same query twice must give the same listing, and the
	// answer carries nothing that would reveal it did not.
	if wide[0] != "root/alpha" || wide[1] != "root/beta" {
		t.Fatalf("a tie must resolve deterministically: %v", wide)
	}
	narrow := h.recallPaths(`RECALL FROM work IN notes MATCH :q LIMIT 10`, map[string]any{"q": "共享关键词"})
	if len(narrow) != 2 {
		t.Fatalf("table-scoped recall = %v", narrow)
	}
	// A Table that does not exist is refused rather than answered with an empty
	// list, which would read as "nothing matched".
	missing := `RECALL FROM work IN missing MATCH :q LIMIT 10`
	if code := h.fails(missing, map[string]any{"q": "共享关键词"}, executor.MutationOptions{}); code != result.CodeNotFound {
		t.Fatalf("unknown Table: code = %s", code)
	}

	// The limit bounds the answer, and the answer stays ordered.
	limited := h.recallPaths(`RECALL FROM work MATCH :q LIMIT 1`, map[string]any{"q": "共享关键词"})
	if len(limited) != 1 || limited[0] != "root/alpha" {
		t.Fatalf("limited recall = %v", limited)
	}
}

func TestKeywordRecallRefusesQueriesItCannotIndex(t *testing.T) {
	h := newHarness(t)
	leaves := h.seedLeaves("one")
	h.insertTitle("为什么选 SQLite", []string{leaves[0]})

	// A single character is in almost every Row, so an answer would be the whole
	// Database in index order, and an empty list would read exactly like "not
	// found". The statement refuses instead; two characters is a word and is
	// answered, which TestKeywordRecallRefusesOnlyQueriesShorterThanAWord covers.
	for _, query := range []string{"", "存"} {
		source := `RECALL FROM work MATCH :q LIMIT 5`
		if code := h.fails(source, map[string]any{"q": query}, executor.MutationOptions{}); code != result.CodeValidation {
			t.Fatalf("query %q: code = %s", query, code)
		}
	}
	// A missing limit is not a request for everything.
	if code := h.fails(`RECALL FROM work MATCH '存储引擎'`, nil, executor.MutationOptions{}); code == "" {
		t.Fatal("recall without LIMIT must be refused")
	}
}

func TestKeywordRecallForgetsDeletedAndSupersededRows(t *testing.T) {
	h := newHarness(t)
	leaves := h.seedLeaves("one", "two", "first", "second")
	source := h.insertTitle("即将被拆分的独特事实", []string{leaves[0]})
	other := h.insertTitle("另一条独特事实", []string{leaves[1]})

	split := write("split")
	split.ExpectedRevision = 1
	split.MaxAffectedRows = 3
	split.TargetRouteLeafIDs = [][]string{{leaves[2]}, {leaves[3]}}
	h.run(`SPLIT work.notes ROW :row INTO (title) VALUES ('拆分后的第一部分'), ('拆分后的第二部分')`,
		map[string]any{"row": source}, split)

	// The superseded Row left the index with its reachability; the new Rows are
	// recallable under their own text.
	//
	// The query has to be made of the old Row's own characters: matching is by
	// shared pairs, so a query that still shares a pair with a live Row is
	// answered by that Row by design (see TestAChinesePhraseStillFinds...). What
	// is asserted here is that nothing answers with the words that are gone.
	if paths := h.recallPaths(`RECALL FROM work MATCH :q LIMIT 10`, map[string]any{"q": "即将被"}); len(paths) != 0 {
		t.Fatalf("a superseded Row must not be recalled: %v", paths)
	}
	// Both new Rows share the words 拆分后的第…部分; the one that carries more of
	// the query is the one that carries it all, and it ranks first.
	if paths := h.recallPaths(`RECALL FROM work MATCH :q LIMIT 10`, map[string]any{"q": "拆分后的第一部分"}); len(paths) != 2 || paths[0] != "root/first" {
		t.Fatalf("the new Rows must be recallable: %v", paths)
	}

	// Deletion withdraws the text from the index as well.
	h.deleteRow(other, h.revisionOf(other))
	if paths := h.recallPaths(`RECALL FROM work MATCH :q LIMIT 10`, map[string]any{"q": "另一条独特事实"}); len(paths) != 0 {
		t.Fatalf("a deleted Row must not be recalled: %v", paths)
	}
}

func TestKeywordRecallFindsEditsAndFoldsWidths(t *testing.T) {
	h := newHarness(t)
	leaves := h.seedLeaves("one")
	rowID := h.insertTitle("旧的标题在此", []string{leaves[0]})

	edit := write("change the payload")
	edit.ExpectedRevision = 1
	h.run(`UPDATE work.notes SET title = '新的标题在这里' WHERE row_id = :row`, map[string]any{"row": rowID}, edit)

	// The index follows the text: the new words are there and the old ones are
	// not. 旧的 shares no pair with the new title while 的标/标题/题在 do, so this
	// asks about the edit rather than about the characters both titles happen to
	// share — a query that shares a pair with live text is answered by design.
	if paths := h.recallPaths(`RECALL FROM work MATCH :q LIMIT 10`, map[string]any{"q": "旧的"}); len(paths) != 0 {
		t.Fatalf("the index kept the old text: %v", paths)
	}
	if paths := h.recallPaths(`RECALL FROM work MATCH :q LIMIT 10`, map[string]any{"q": "新的标题"}); len(paths) != 1 {
		t.Fatalf("the index missed the new text: %v", paths)
	}

	// Full-width and half-width spellings meet: the stored text is indexed folded.
	full := write("write a full-width title")
	full.ExpectedRevision = 2
	h.run(`UPDATE work.notes SET title = 'ＡＢＣ 全角标题' WHERE row_id = :row`, map[string]any{"row": rowID}, full)
	if paths := h.recallPaths(`RECALL FROM work MATCH :q LIMIT 10`, map[string]any{"q": "ABC"}); len(paths) != 1 {
		t.Fatalf("width folding failed: %v", paths)
	}
}
