package sqlstore_test

import (
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
)

// RESTORE has to leave the derived layer describing the text it brought back.
// Found by the internals audit (docs/development/audit-2026-09-23.md, A3).

// RESTORE brings back the text of an earlier revision, so recall has to describe
// that text afterwards. The unit was left carrying the payload of the revision
// the restore replaced: the replaced text kept matching and the restored text
// matched nothing, with nothing reporting it.
func TestRestoreRebuildsTheRecallUnitForTheTextItBringsBack(t *testing.T) {
	h := newHarness(t)
	// This one needs a summary Column: RESTORE brings back the text recall reads.
	h.run(`CREATE DATABASE work PURPOSE 'Work memory' SCOPE 'Projects'`, nil, executor.MutationOptions{})
	h.run(`CREATE TABLE work.notes PURPOSE 'Notes' ROW SEMANTICS 'One fact' `+
		`(title TEXT NOT NULL PURPOSE 'Title' ROLE title, body TEXT PURPOSE 'Body' ROLE summary)`,
		nil, executor.MutationOptions{})
	h.run(`CREATE ROUTE ROOT FOR TABLE work.notes PURPOSE 'Everything'`, nil, write("root"))
	mutation := write("insert the first revision")
	mutation.RoutePath = pathOf("architecture", "sqlite")
	answer := h.run(`INSERT INTO work.notes (title, body) VALUES (:title, :body)`,
		map[string]any{"title": "storage engine", "body": "alpha text about sqlite"}, mutation)
	rowID := text(answer.Rows[0]["row_id"])
	_, alphaHash := h.recallUnit(rowID)

	replace := write("replace the text")
	replace.ExpectedRevision = 1
	h.run(`UPDATE work.notes SET body = :body WHERE row_id = :row`,
		map[string]any{"body": "beta text about write ahead logs", "row": rowID}, replace)
	_, betaHash := h.recallUnit(rowID)
	if betaHash == alphaHash {
		t.Fatal("the update must rebuild the unit, or this test proves nothing")
	}

	restore := write("restore it")
	restore.ExpectedRevision = 2
	h.run(`RESTORE work.notes ROW :row TO REVISION 1`, map[string]any{"row": rowID}, restore)

	_, afterHash := h.recallUnit(rowID)
	if afterHash == betaHash {
		t.Fatalf("the unit still describes the text the restore replaced")
	}
	if afterHash != alphaHash {
		t.Fatalf("the unit must describe the restored text")
	}
	// Recall says the same thing: the restored text is findable, the replaced one
	// is not.
	if paths := h.recallPaths(`RECALL FROM work MATCH :q LIMIT 5`, map[string]any{"q": "beta"}); len(paths) != 0 {
		t.Fatalf("the replaced text must not be recallable: %v", paths)
	}
	if paths := h.recallPaths(`RECALL FROM work MATCH :q LIMIT 5`, map[string]any{"q": "alpha"}); len(paths) != 1 {
		t.Fatalf("the restored text must be recallable: %v", paths)
	}
}
