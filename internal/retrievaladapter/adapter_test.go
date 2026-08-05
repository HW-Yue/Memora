package retrievaladapter_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/retrievaladapter"
)

func TestBuildMIRACLNormalizesQueriesAndQrels(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "topics", "topics.miracl-v1.0-zh-dev.tsv"), "q1\t第一个问题\nq2\t第二个问题\n")
	write(t, filepath.Join(root, "qrels", "qrels.miracl-v1.0-zh-dev.tsv"), "q1\tQ0\td1\t1\nq1\tQ0\td2\t0\nq2\tQ0\td3\t2\n")
	suite, err := retrievaladapter.Build(retrievaladapter.Config{Kind: "miracl-zh", Root: root, Split: "dev"})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if suite.SuiteID != "miracl-zh-dev" || len(suite.Queries) != 2 || len(suite.Qrels) != 3 || suite.Hash == "" {
		t.Fatalf("suite = %#v", suite)
	}
	if suite.Queries[0].Text != "第一个问题" {
		t.Fatalf("query text = %q", suite.Queries[0].Text)
	}
}

func TestBuildMTRAGFiltersUnanswerableQueriesAndParsesHeader(t *testing.T) {
	root := filepath.Join(t.TempDir(), "dataset")
	queryPath := filepath.Join(root, "mtrag-human", "retrieval_tasks", "clapnq")
	write(t, filepath.Join(queryPath, "clapnq_lastturn.jsonl"), "{\"_id\":\"q1\",\"text\":\"|user|: one\"}\n{\"_id\":\"q2\",\"text\":\"|user|: two\"}\n{\"_id\":\"q3\",\"text\":\"|user|: no answer\"}\n")
	write(t, filepath.Join(queryPath, "qrels", "dev.tsv"), "query-id\tcorpus-id\tscore\nq1\td1\t1\nq2\td2\t1\n")
	suite, err := retrievaladapter.Build(retrievaladapter.Config{Kind: "mtrag", Root: root, Domain: "clapnq", QueryMode: "lastturn"})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(suite.Queries) != 2 || suite.Queries[0].Text != "|user|: one" {
		t.Fatalf("suite queries = %#v", suite.Queries)
	}
}

func TestBuildRejectsOrphanQrel(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "topics", "topics.miracl-v1.0-zh-dev.tsv"), "q1\t问题\n")
	write(t, filepath.Join(root, "qrels", "qrels.miracl-v1.0-zh-dev.tsv"), "q2\tQ0\td1\t1\n")
	if _, err := retrievaladapter.Build(retrievaladapter.Config{Kind: "miracl-zh", Root: root, Split: "dev"}); err == nil || !strings.Contains(err.Error(), "qrel") {
		t.Fatalf("Build() error = %v", err)
	}
}

func write(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
		t.Fatal(err)
	}
}
