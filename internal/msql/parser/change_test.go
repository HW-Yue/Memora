package parser

import "testing"

func TestParseCommittedChangeReads(t *testing.T) {
	t.Parallel()
	timeline, err := Parse("SHOW CHANGES IN DATABASE work AFTER COMMIT_SEQUENCE :after CURSOR :cursor LIMIT :limit")
	if err != nil {
		t.Fatalf("Parse(timeline) error = %v", err)
	}
	show := timeline.Statement.Show
	if show == nil || show.Object != "CHANGES" || show.Database == nil || show.After == nil ||
		show.Cursor == nil || show.Limit == nil {
		t.Fatalf("timeline AST = %#v", show)
	}
	entryPage, err := Parse("SHOW CHANGE :transaction IN DATABASE work CURSOR :cursor LIMIT 32")
	if err != nil {
		t.Fatalf("Parse(entry page) error = %v", err)
	}
	show = entryPage.Statement.Show
	if show == nil || show.Object != "CHANGE" || show.Change == nil || show.Database == nil ||
		show.Cursor == nil || show.Limit == nil {
		t.Fatalf("entry AST = %#v", show)
	}
}
