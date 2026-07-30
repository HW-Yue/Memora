package parser_test

import (
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/parser"
)

func TestParsePublicSplitAndMerge(t *testing.T) {
	t.Parallel()

	split, err := parser.Parse(
		"SPLIT work.notes ROW :source INTO (title) VALUES (:first), (:second)",
	)
	if err != nil || split.Statement.Reshape == nil ||
		split.Statement.Reshape.Mode != "SPLIT" ||
		len(split.Statement.Reshape.Sources) != 1 ||
		len(split.Statement.Reshape.Values) != 2 {
		t.Fatalf("SPLIT AST = %#v, %v", split, err)
	}
	merge, err := parser.Parse(
		"MERGE work.notes ROWS (:first, :second) INTO (title) VALUES (:merged)",
	)
	if err != nil || merge.Statement.Reshape == nil ||
		merge.Statement.Reshape.Mode != "MERGE" ||
		len(merge.Statement.Reshape.Sources) != 2 ||
		len(merge.Statement.Reshape.Values) != 1 {
		t.Fatalf("MERGE AST = %#v, %v", merge, err)
	}
}
