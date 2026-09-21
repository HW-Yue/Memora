package sqlstore_test

import (
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/sqlstore"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
)

// Chinese is written without spaces, so a tokenizer that only splits on
// separators sees one token per run of characters and a tokenizer that needs
// three characters at a time cannot answer the two-character words Chinese
// actually uses. The index therefore holds the overlapping character pairs of
// every run, and these tests freeze what that buys and what it costs.

// The measured complaint: 实习 and 后端 are two characters, so the trigram index
// returned nothing for them while the row carrying them was right there.
func TestATwoCharacterChineseQueryReachesTheIndex(t *testing.T) {
	h := newHarness(t)
	leaves := h.seedLeaves("first", "second", "third")
	h.insertTitle("悠悠有品 后端开发实习", []string{leaves[0]})
	h.insertTitle("OPPO 影像算法", []string{leaves[1]})
	h.insertTitle("完全无关的另一条事实", []string{leaves[2]})

	for _, query := range []string{"实习", "后端", "开发", "端开"} {
		got := h.recallPaths(`RECALL FROM work MATCH :q LIMIT 5`, map[string]any{"q": query})
		if len(got) != 1 || got[0] != "root/first" {
			t.Fatalf("query %q must reach the Row that carries it, got %v", query, got)
		}
	}
	// Latin text is tokenized by the same rule, so a word and its parts both
	// reach the index — this is the one the user could already search.
	for _, query := range []string{"oppo", "OPPO", "opp"} {
		got := h.recallPaths(`RECALL FROM work MATCH :q LIMIT 5`, map[string]any{"q": query})
		if len(got) != 1 || got[0] != "root/second" {
			t.Fatalf("query %q must reach the Row that carries it, got %v", query, got)
		}
	}
	if got := h.recallPaths(`RECALL FROM work MATCH :q LIMIT 5`, map[string]any{"q": "无关"}); len(got) != 1 || got[0] != "root/third" {
		t.Fatalf("a Row with no path is still recallable: %v", got)
	}
}

// A query the user types as one phrase is not a demand for that exact string. It
// is a weaker demand: find what shares it. The Row holding the whole phrase has
// to rank first, and the Row that only shares 实习 must still be in the answer —
// an exact-string requirement is what made this look broken before.
func TestAChinesePhraseStillFindsTheRowsThatOnlyShareOneOfItsWords(t *testing.T) {
	h := newHarness(t)
	leaves := h.seedLeaves("phrase", "partial", "unrelated")
	h.insertTitle("实习经历：在悠悠有品做后端", []string{leaves[0]})
	h.insertTitle("后端开发实习", []string{leaves[1]})
	h.insertTitle("完全无关的另一条事实", []string{leaves[2]})

	got := h.recallPaths(`RECALL FROM work MATCH :q LIMIT 10`, map[string]any{"q": "实习经历"})
	if len(got) != 2 {
		t.Fatalf("a query shares what it shares: got %v", got)
	}
	if got[0] != "root/phrase" || got[1] != "root/partial" {
		t.Fatalf("the Row holding the phrase must rank first: %v", got)
	}
	// The unrelated Row shares no token and must not be dragged in by one.
	if limited := h.recallPaths(`RECALL FROM work MATCH :q LIMIT 1`, map[string]any{"q": "实习经历"}); len(limited) != 1 || limited[0] != "root/phrase" {
		t.Fatalf("LIMIT must cut the ranked listing: %v", limited)
	}
}

// Relaxed matching is not a license to ignore relevance: a Row that carries more
// of the query outranks one that carries a single piece of it. Without that order
// the extra recall would be noise rather than candidates.
func TestAChineseQueryRanksTheRowThatCarriesMoreOfItFirst(t *testing.T) {
	h := newHarness(t)
	leaves := h.seedLeaves("whole", "piece")
	h.insertTitle("后端开发实习", []string{leaves[0]})
	h.insertTitle("后端岗位，暑期实习", []string{leaves[1]})

	got := h.recallPaths(`RECALL FROM work MATCH :q LIMIT 10`, map[string]any{"q": "后端开发"})
	if len(got) != 2 || got[0] != "root/whole" || got[1] != "root/piece" {
		t.Fatalf("coverage must decide the order: %v", got)
	}
}

// A query is not FTS5 syntax. Words that happen to be operators, punctuation and
// quotes are content, and a query built by pasting them together would either
// fail to parse or silently mean something else.
func TestAQueryIsContentNotFTS5Syntax(t *testing.T) {
	h := newHarness(t)
	leaves := h.seedLeaves("one", "two")
	h.insertTitle("C++ 与 OR 的区别", []string{leaves[0]})
	h.insertTitle("完全无关的另一条事实", []string{leaves[1]})

	for _, query := range []string{"C++", `"OR"`, "or 区别", "C++ 与"} {
		got := h.recallPaths(`RECALL FROM work MATCH :q LIMIT 5`, map[string]any{"q": query})
		if len(got) != 1 || got[0] != "root/one" {
			t.Fatalf("query %q must be read as text, got %v", query, got)
		}
	}
}

// One character is refused: a single character is in almost every Row, so the
// listing would be the whole Database in index order, which reads like an answer
// and is not one. Two characters is a word and must work.
func TestKeywordRecallRefusesOnlyQueriesShorterThanAWord(t *testing.T) {
	h := newHarness(t)
	leaves := h.seedLeaves("one")
	h.insertTitle("为什么选 SQLite 作为存储引擎", []string{leaves[0]})

	source := `RECALL FROM work MATCH :q LIMIT 5`
	for _, query := range []string{"", " ", "存"} {
		if code := h.fails(source, map[string]any{"q": query}, executor.MutationOptions{}); code != result.CodeValidation {
			t.Fatalf("query %q: code = %s", query, code)
		}
	}
	if got := h.recallPaths(source, map[string]any{"q": "存储"}); len(got) != 1 || got[0] != "root/one" {
		t.Fatalf("a two-character word must be answered, got %v", got)
	}
}

// The index is derived, so the tokenizer can move without touching a Row: the
// payload stays the truth and the vectors that describe it stay valid. What must
// not survive is the old index's content — an Instance opened by this binary has
// to be reindexed, and its file has to say so, because an older binary would
// otherwise read the new index as if it were the old one.
func TestAnInstanceIndexedBeforeTheBigramsIsRebuiltOnOpen(t *testing.T) {
	h := newHarness(t)
	leaves := h.seedLeaves("first", "second")
	h.insertTitle("悠悠有品 后端开发实习", []string{leaves[0]})
	h.insertTitle("完全无关的另一条事实", []string{leaves[1]})

	// Put the file back the way the trigram binary left it: an index declared
	// trigram, over the folded text as one token.
	h.execRaw(`DROP TABLE mem_recall_fts`)
	h.execRaw(`CREATE VIRTUAL TABLE mem_recall_fts USING fts5(
		payload_index, content='mem_recall_units', content_rowid='unit_no', tokenize='trigram')`)
	h.execRaw(`INSERT INTO mem_recall_fts(rowid, payload_index) SELECT unit_no, payload FROM mem_recall_units`)
	h.execRaw(`UPDATE mem_recall_units SET payload_index = payload`)
	h.execRaw(`PRAGMA user_version = 1`)
	h.reopen()

	if version := h.userVersion(); version != 2 {
		t.Fatalf("the file must record the index it now holds, got version %d", version)
	}
	if declaration := h.recallIndexDeclaration(); !strings.Contains(declaration, "unicode61") {
		t.Fatalf("the keyword index must be rebuilt on the pair tokenizer:\n%s", declaration)
	}
	if got := h.recallPaths(`RECALL FROM work MATCH :q LIMIT 5`, map[string]any{"q": "实习"}); len(got) != 1 || got[0] != "root/first" {
		t.Fatalf("the rebuilt index must answer a two-character query, got %v", got)
	}
}

// A file written by the bigram binary is not readable by the earlier binary,
// which would rebuild the index as trigram and answer nothing for the queries
// this Feature exists to serve. The version is the guard, so it has to be read.
func TestAnInstanceFromANewerFileSchemaIsRefused(t *testing.T) {
	h := newHarness(t)
	h.seedTree()
	h.execRaw(`PRAGMA user_version = 99`)
	if err := h.reopenErr(); err == nil || !strings.Contains(err.Error(), "newer Memora") {
		t.Fatalf("opening a newer file must refuse to migrate it backwards, got %v", err)
	}
}

// execRaw is how a test puts a file back into the shape an earlier binary left
// it in, or asks what the file says about itself.
func (h *harness) execRaw(source string) {
	h.t.Helper()
	if _, err := h.db.SQL().Exec(source); err != nil {
		h.t.Fatalf("%s: %v", source, err)
	}
}

func (h *harness) userVersion() int {
	h.t.Helper()
	version := 0
	if err := h.db.SQL().QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		h.t.Fatal(err)
	}
	return version
}

func (h *harness) recallIndexDeclaration() string {
	h.t.Helper()
	declaration := ""
	if err := h.db.SQL().QueryRow(
		`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'mem_recall_fts'`).Scan(&declaration); err != nil {
		h.t.Fatal(err)
	}
	return declaration
}

func (h *harness) reopenErr() error {
	h.t.Helper()
	if err := h.db.Close(); err != nil {
		h.t.Fatal(err)
	}
	db, err := sqlstore.Open(h.path, sqlstore.Options{CheckInvariants: true})
	if err != nil {
		return err
	}
	h.t.Cleanup(func() { _ = db.Close() })
	h.db = db
	return nil
}
