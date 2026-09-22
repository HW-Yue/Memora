package sqlstore_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
)

// The read surface is narrow on purpose: one equality on row_id, `AND` to join
// more, and every returned Row carrying its own `route_paths` and `links`. A
// fresh agent walks into the edges of that surface with the shapes it knows from
// SQL — `IN (…)` for several Rows, `route_paths` in the projection — so each edge
// has to say what the surface is and what to do instead. These tests pin the
// message, because the message is the whole fix: the failure is not a bug, the
// silence around it was.

func (h *harness) readFailure(source string, named map[string]any) (result.Code, string) {
	h.t.Helper()
	h.seq++
	envelope := h.session.ExecuteBatch(context.Background(), executor.BatchRequest{
		RequestID: "m" + strings.Repeat("z", h.seq), Source: source,
		Statements: []executor.StatementInput{{
			Parameters: executor.Parameters{Named: named}, Mutation: executor.MutationOptions{}, Authorization: authorization,
		}},
	})
	requireDeliverable(h.t, source, envelope)
	if envelope.Error != nil {
		return envelope.Error.Code, envelope.Error.Message
	}
	if envelope.Results[0].Error == nil {
		h.t.Fatalf("%s unexpectedly succeeded", source)
	}
	failure := envelope.Results[0].Error
	return failure.Code, failure.Message
}

func TestAnUnsupportedReadOperatorSaysWhatToDoInstead(t *testing.T) {
	h := newHarness(t)
	leaves := h.seedLeaves("one", "two")
	first := h.insertTitle("第一条事实", []string{leaves[0]})
	second := h.insertTitle("第二条事实", []string{leaves[1]})

	code, message := h.readFailure(
		`SELECT title FROM work.notes WHERE row_id IN (:a, :b) LIMIT 2`,
		map[string]any{"a": first, "b": second})
	if code != result.CodeParseError {
		t.Fatalf("code = %s, want %s", code, result.CodeParseError)
	}
	for _, expected := range []string{"row_id", "not part of the read surface", "--input array"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("message must explain the read surface (%q missing): %s", expected, message)
		}
	}
}

func TestAReadLimitAboveTheBudgetNamesTheBudgetAccessor(t *testing.T) {
	h := newHarness(t)
	leaves := h.seedLeaves("one")
	h.insertTitle("唯一一条事实", []string{leaves[0]})

	code, message := h.readFailure(`SELECT title FROM work.notes LIMIT 50`, nil)
	if code != result.CodeValidation {
		t.Fatalf("code = %s, want %s", code, result.CodeValidation)
	}
	for _, expected := range []string{"select_rows", "SHOW CONFIGURATION", "SELECT_ROWS"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("a refusal must name the budget and how to read it (%q missing): %s", expected, message)
		}
	}
}

func TestProjectingAnAttachedFieldExplainsWhereItComesFrom(t *testing.T) {
	h := newHarness(t)
	leaves := h.seedLeaves("one")
	h.insertTitle("唯一一条事实", []string{leaves[0]})

	code, message := h.readFailure(`SELECT route_paths FROM work.notes LIMIT 1`, nil)
	if code != result.CodeValidation {
		t.Fatalf("code = %s, want %s", code, result.CodeValidation)
	}
	if !strings.Contains(message, "attached to every returned Row") {
		t.Fatalf("message must say route_paths rides along on the Row: %s", message)
	}
	// The projection an agent should have written keeps working, and the Row
	// still carries the membership it was looking for.
	row := h.run(`SELECT title FROM work.notes LIMIT 1`, nil, executor.MutationOptions{}).Rows[0]
	if _, ok := row["route_paths"]; !ok {
		t.Fatalf("every returned Row must carry route_paths: %v", row)
	}
}

// A census is how an agent proves it has seen everything, so the answer has to
// say when it has not. `SELECT row_id, title FROM t LIMIT 2` over three Rows used
// to come back `truncated: false` — the scan was complete, the *listing* was cut
// by the caller's own LIMIT, and nothing distinguished the two. An agent reading
// that would report three Rows as two and call it complete.
func TestACensusCutByItsOwnLimitSaysSo(t *testing.T) {
	h := newHarness(t)
	leaves := h.seedLeaves("one", "two", "three")
	h.insertTitle("第一条事实", []string{leaves[0]})
	h.insertTitle("第二条事实", []string{leaves[1]})
	third := h.insertTitle("第三条事实", []string{leaves[2]})

	cut := h.run(`SELECT row_id, title FROM work.notes LIMIT 2`, nil, executor.MutationOptions{})
	if len(cut.Rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(cut.Rows))
	}
	if !cut.Truncated {
		t.Fatal("a listing cut by its own LIMIT must be reported as truncated")
	}
	// And the refusal is actionable: the answer names the one read surface that
	// can finish the enumeration, so the reader does not have to remember it.
	hints := 0
	for _, notice := range cut.Warnings {
		if notice.Code != result.CodeOutputTruncated {
			continue
		}
		hints++
		if hint, _ := notice.Details["enumerate_with"].(string); !strings.Contains(hint, "SHOW ROUTES FROM TABLE work.notes") {
			t.Fatalf("a truncated census must name the enumerator: %v", notice.Details)
		}
	}
	if hints != 1 {
		t.Fatalf("a truncated census must carry exactly one output_truncated notice, got %d", hints)
	}
	// Reading the whole table in one statement is not truncated, so the flag
	// still distinguishes "you asked for less" from "there is more".
	whole := h.run(`SELECT row_id, title FROM work.notes LIMIT 3`, nil, executor.MutationOptions{})
	if whole.Truncated {
		t.Fatal("a listing that returned every Row must not be reported as truncated")
	}
	for _, notice := range whole.Warnings {
		if notice.Code == result.CodeOutputTruncated {
			t.Fatal("a complete census must not carry an output_truncated notice")
		}
	}
	// A point read is never a cut listing either.
	point := h.run(`SELECT title FROM work.notes WHERE row_id = :row LIMIT 1`,
		map[string]any{"row": third}, executor.MutationOptions{})
	if point.Truncated {
		t.Fatal("a point read must not be reported as truncated")
	}
}

// `truncated` has to mean one thing on every surface: "at least one more exists".
// Two of them used to set it from `len(items) == limit`, which reports more when
// the listing merely filled the page — a reader that trusts the flag would claim
// completeness it does not have, and the same reader is told elsewhere that the
// flag is the completeness signal.
func TestTruncatedMeansMoreExistsOnEverySurface(t *testing.T) {
	h := newHarness(t)
	leaves := h.seedLeaves("one", "two")
	h.insertTitle("共享关键词的第一条", []string{leaves[0]})
	h.insertTitle("共享关键词的第二条", []string{leaves[1]})

	// Recall, exactly at the limit: two matches, LIMIT 2 is everything.
	exact := h.run(`RECALL FROM work MATCH :q LIMIT 2`,
		map[string]any{"q": "共享关键词"}, executor.MutationOptions{})
	if len(exact.Rows) != 2 || exact.Truncated {
		t.Fatalf("a listing that returned every hit must not be truncated: %d rows, truncated=%v",
			len(exact.Rows), exact.Truncated)
	}
	// Recall, cut by the limit: two matches, LIMIT 1 leaves one behind.
	cut := h.run(`RECALL FROM work MATCH :q LIMIT 1`,
		map[string]any{"q": "共享关键词"}, executor.MutationOptions{})
	if len(cut.Rows) != 1 || !cut.Truncated {
		t.Fatalf("a cut listing must be truncated: %d rows, truncated=%v", len(cut.Rows), cut.Truncated)
	}
	// The pending-vector listing follows the same definition: both Rows have a
	// unit and no vector yet, so LIMIT 2 is the whole backlog and LIMIT 1 is not.
	pending := h.run(`SHOW PENDING VECTORS IN DATABASE work LIMIT 2`, nil, executor.MutationOptions{})
	if len(pending.Rows) != 2 || pending.Truncated {
		t.Fatalf("the whole backlog must not be truncated: %d rows, truncated=%v",
			len(pending.Rows), pending.Truncated)
	}
	pendingCut := h.run(`SHOW PENDING VECTORS IN DATABASE work LIMIT 1`, nil, executor.MutationOptions{})
	if len(pendingCut.Rows) != 1 || !pendingCut.Truncated {
		t.Fatalf("a cut backlog must be truncated: %d rows, truncated=%v",
			len(pendingCut.Rows), pendingCut.Truncated)
	}
}

// A reader that can only see `type: "TEXT"` cannot check the write rule it is
// told to follow (~1,000 CJK characters inside a TEXT(2500) column), and a reader
// that can only see a `revision` cannot tell whether a Row that reads like a
// living log has actually been updated since it was created. Both belong in the
// read surface: the declared ceiling on the column, and the write time on the Row.
func TestAReadCarriesTheColumnCeilingAndTheRowWriteTime(t *testing.T) {
	h := newHarness(t)
	leaves := h.seedLeaves("one")
	rowID := h.insertTitle("唯一一条事实", []string{leaves[0]})

	read := h.run(`SELECT title, row_id, revision FROM work.notes LIMIT 1`, nil, executor.MutationOptions{})
	title := result.Column{}
	for _, column := range read.Columns {
		if column.Name == "title" {
			title = column
		}
	}
	if title.Name == "" || title.MaxCharacters <= 0 {
		t.Fatalf("a TEXT column must carry its declared ceiling: %+v", read.Columns)
	}

	point := h.run(`SELECT title, row_id, revision FROM work.notes WHERE row_id = :row LIMIT 1`,
		map[string]any{"row": rowID}, executor.MutationOptions{})
	if point.RowDetail == nil || point.RowDetail.UpdatedAt == "" || point.RowDetail.CreatedAt == "" {
		t.Fatalf("a point read must carry its freshness anchor: %+v", point.RowDetail)
	}
	if _, err := time.Parse(time.RFC3339, point.RowDetail.UpdatedAt); err != nil {
		t.Fatalf("updated_at must be a timestamp a reader can parse: %v", err)
	}
}

// The ceiling is counted in code points, not bytes, and a reader now sees the
// same number it is enforced against (`columns[].max_characters`). CJK is where
// the two differ by a factor of three, so the boundary is tested with CJK: a
// 1,000-character Chinese document fits a TEXT(1200) column, a 1,201-character
// one is refused, and the refusal reports the code-point count rather than the
// byte count a reader would otherwise have to guess at.
func TestTheTextCeilingCountsCodePointsNotBytes(t *testing.T) {
	h := newHarness(t)
	leaves := h.seedLeaves("one", "two")
	atLimit, overLimit := leaves[0], leaves[1]

	fit := write("boundary fits")
	fit.RouteLeafIDs = []string{atLimit}
	h.run(`INSERT INTO work.notes (title) VALUES (:title)`,
		map[string]any{"title": strings.Repeat("记", 1200)}, fit)

	over := write("boundary over")
	over.RouteLeafIDs = []string{overLimit}
	code := h.fails(`INSERT INTO work.notes (title) VALUES (:title)`,
		map[string]any{"title": strings.Repeat("记", 1201)}, over)
	if code != result.CodeValueTooLong {
		t.Fatalf("one code point over the ceiling must be refused with %s, got %s",
			result.CodeValueTooLong, code)
	}
	// The at-limit document is 1200 code points and ~3600 bytes: if the ceiling
	// were counted in bytes it would have been refused, so reaching here with the
	// over-limit refusal means the unit is code points. The refused Row left no
	// mount behind either.
	if holder := h.leafHolder(overLimit); holder != "" {
		t.Fatalf("a refused insert must not mount a Row, found %s", holder)
	}
}

// The display map is a property of the Table, not of the request: a caller that
// projected only `title` still needs to know which column holds the document.
// Deriving it from the projection made `row_detail.display` change shape with
// every read — a caller could not rely on `summary_column` being there.
func TestTheDisplayMapNamesTheTablesColumnsNotTheProjection(t *testing.T) {
	h := newHarness(t)
	h.run(`CREATE DATABASE work PURPOSE 'Work memory' SCOPE 'Documents'`, nil, executor.MutationOptions{})
	h.run(`CREATE TABLE work.docs PURPOSE 'Documents' ROW SEMANTICS 'One document per row' `+
		`(title TEXT NOT NULL PURPOSE 'Title' ROLE title, body TEXT(2500) NOT NULL PURPOSE 'Body' ROLE summary)`,
		nil, executor.MutationOptions{})
	root := text(h.run(`CREATE ROUTE ROOT FOR TABLE work.docs PURPOSE 'Documents'`, nil, write("root")).Rows[0]["route_id"])
	leaf := text(h.run(`CREATE ROUTE UNDER :parent NAME 'one' KIND 'leaf' PURPOSE 'First document'`,
		map[string]any{"parent": root}, write("leaf")).Rows[0]["route_id"])
	insert := write("insert document")
	insert.RouteLeafIDs = []string{leaf}
	rowID := text(h.run(`INSERT INTO work.docs (title, body) VALUES (:title, :body)`,
		map[string]any{"title": "一条文档", "body": "正文"}, insert).Rows[0]["row_id"])

	// Only `title` is projected; the map must still name the document column.
	partial := h.run(`SELECT title FROM work.docs WHERE row_id = :row LIMIT 1`,
		map[string]any{"row": rowID}, executor.MutationOptions{})
	if partial.RowDetail == nil {
		t.Fatal("a point read must carry a row detail")
	}
	if partial.RowDetail.Display.TitleColumn != "title" || partial.RowDetail.Display.SummaryColumn != "body" {
		t.Fatalf("the display map must name the Table's columns: %+v", partial.RowDetail.Display)
	}
}
