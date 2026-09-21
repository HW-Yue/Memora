package sqlstore_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
)

// The tests in this file pin the write path that the invariant work is about to
// change: how a Row is mounted on its leaf, how it is persisted, and where the
// transaction boundaries are. They describe today's behaviour — including the
// holes the next features close — so that changing it fails loudly here first.

func (h *harness) notesTableID() string {
	h.t.Helper()
	var id string
	if err := h.db.SQL().QueryRow(`SELECT id FROM mem_tables WHERE name = 'notes'`).Scan(&id); err != nil {
		h.t.Fatal(err)
	}
	return id
}

// storedLeaves reads the mount straight out of the data table. SELECT projects
// paths rather than IDs, and the point here is the stored row itself.
func (h *harness) storedLeaves(rowID string) []string {
	h.t.Helper()
	var encoded string
	query := `SELECT route_leaf_ids FROM "data_` + h.notesTableID() + `" WHERE row_id = ?`
	if err := h.db.SQL().QueryRow(query, rowID).Scan(&encoded); err != nil {
		h.t.Fatal(err)
	}
	leaves := []string{}
	if err := json.Unmarshal([]byte(encoded), &leaves); err != nil {
		h.t.Fatal(err)
	}
	return leaves
}

func (h *harness) leafHolder(leafID string) string {
	h.t.Helper()
	var body string
	query := `SELECT body FROM "routes_` + h.notesTableID() + `" WHERE route_id = ?`
	if err := h.db.SQL().QueryRow(query, leafID).Scan(&body); err != nil {
		h.t.Fatal(err)
	}
	var stored struct {
		RowID string `json:"row_id"`
	}
	if err := json.Unmarshal([]byte(body), &stored); err != nil {
		h.t.Fatal(err)
	}
	return stored.RowID
}

// deprecateLeaf retires a leaf the way a route-mutation plan does. Plans are the
// only remaining producer of a retired node, so the fixture writes the stored
// shape directly instead of driving a whole plan.
func (h *harness) deprecateLeaf(leafID string) {
	h.t.Helper()
	var body string
	query := `SELECT body FROM "routes_` + h.notesTableID() + `" WHERE route_id = ?`
	if err := h.db.SQL().QueryRow(query, leafID).Scan(&body); err != nil {
		h.t.Fatal(err)
	}
	fields := map[string]any{}
	if err := json.Unmarshal([]byte(body), &fields); err != nil {
		h.t.Fatal(err)
	}
	fields["deprecated"] = true
	encoded, err := json.Marshal(fields)
	if err != nil {
		h.t.Fatal(err)
	}
	update := `UPDATE "routes_` + h.notesTableID() + `" SET body = ?, deprecated = 1 WHERE route_id = ?`
	if _, err := h.db.SQL().Exec(update, string(encoded), leafID); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) liveRows() int {
	h.t.Helper()
	var count int
	query := `SELECT COUNT(*) FROM "data_` + h.notesTableID() + `" WHERE row_state = 'live'`
	if err := h.db.SQL().QueryRow(query).Scan(&count); err != nil {
		h.t.Fatal(err)
	}
	return count
}

func (h *harness) seedNotes() (string, string) {
	h.t.Helper()
	h.run(`CREATE DATABASE work PURPOSE 'Work memory' SCOPE 'Projects'`, nil, executor.MutationOptions{})
	h.run(`CREATE TABLE work.notes PURPOSE 'Notes' ROW SEMANTICS 'One fact' (title TEXT NOT NULL PURPOSE 'Title' ROLE title)`, nil, executor.MutationOptions{})
	root := text(h.run(`CREATE ROUTE ROOT FOR TABLE work.notes PURPOSE 'Everything'`, nil, write("root")).Rows[0]["route_id"])
	leaf := text(h.run(`CREATE ROUTE UNDER :p NAME 'sqlite' KIND 'leaf' PURPOSE 'Why sqlite'`,
		map[string]any{"p": root}, write("leaf")).Rows[0]["route_id"])
	return root, leaf
}

func (h *harness) insertTitle(title string, leaves []string) string {
	h.t.Helper()
	mutation := write("insert")
	mutation.RouteLeafIDs = leaves
	result := h.run(`INSERT INTO work.notes (title) VALUES (:title)`, map[string]any{"title": title}, mutation)
	return text(result.Rows[0]["row_id"])
}

func TestInsertMountsTheRowOnItsLeaf(t *testing.T) {
	h := newHarness(t)
	_, leaf := h.seedNotes()

	rowID := h.insertTitle("Use SQLite", []string{leaf})
	if leaves := h.storedLeaves(rowID); len(leaves) != 1 || leaves[0] != leaf {
		t.Fatalf("stored leaves = %v", leaves)
	}
	if holder := h.leafHolder(leaf); holder != rowID {
		t.Fatalf("leaf holds %q, want %q", holder, rowID)
	}
	opened := h.run(`OPEN ROUTE :leaf LIMIT 1`, map[string]any{"leaf": leaf}, executor.MutationOptions{})
	if len(opened.Rows) != 1 || text(opened.Rows[0]["row_id"]) != rowID {
		t.Fatalf("open route = %v", opened.Rows)
	}
}

func TestInsertRejectsLeavesThatCannotHoldTheRow(t *testing.T) {
	h := newHarness(t)
	root, leaf := h.seedNotes()
	branch := text(h.run(`CREATE ROUTE UNDER :p NAME 'branch' KIND 'branch' PURPOSE 'Grouping'`,
		map[string]any{"p": root}, write("branch")).Rows[0]["route_id"])
	retired := text(h.run(`CREATE ROUTE UNDER :p NAME 'retired' KIND 'leaf' PURPOSE 'Retired'`,
		map[string]any{"p": root}, write("retired")).Rows[0]["route_id"])
	h.deprecateLeaf(retired)
	h.insertTitle("first", []string{leaf})

	cases := []struct {
		name   string
		leaves []string
		code   result.Code
	}{
		{name: "branch", leaves: []string{branch}, code: result.CodeConstraint},
		{name: "deprecated leaf", leaves: []string{retired}, code: result.CodeConstraint},
		{name: "occupied leaf", leaves: []string{leaf}, code: result.CodeConstraint},
		{name: "unknown leaf", leaves: []string{"route_missing"}, code: result.CodeNotFound},
	}
	for _, tc := range cases {
		mutation := write("reject")
		mutation.RouteLeafIDs = tc.leaves
		if code := h.fails(`INSERT INTO work.notes (title) VALUES ('rejected')`, nil, mutation); code != tc.code {
			t.Fatalf("%s: code = %s, want %s", tc.name, code, tc.code)
		}
	}
	if h.liveRows() != 1 {
		t.Fatalf("a rejected insert must write nothing: live rows = %d", h.liveRows())
	}
}

// A leaf whose holder is no longer live is free again. This is how a Deleted
// Row's leaf gets reused without any detach step.
func TestInsertReassignsALeafWhoseHolderIsNoLongerLive(t *testing.T) {
	h := newHarness(t)
	_, leaf := h.seedNotes()
	first := h.insertTitle("first", []string{leaf})

	remove := write("delete")
	remove.ExpectedRevision = 1
	h.run(`DELETE FROM work.notes WHERE row_id = :row`, map[string]any{"row": first}, remove)

	second := h.insertTitle("second", []string{leaf})
	if holder := h.leafHolder(leaf); holder != second {
		t.Fatalf("leaf holds %q, want %q", holder, second)
	}
	opened := h.run(`OPEN ROUTE :leaf LIMIT 1`, map[string]any{"leaf": leaf}, executor.MutationOptions{})
	if len(opened.Rows) != 1 || text(opened.Rows[0]["row_id"]) != second {
		t.Fatalf("open route = %v", opened.Rows)
	}
}

// Deleting a Row leaves its leaf pointing at it; reachability is decided when
// the leaf is read, not by detaching it. The whole delete path is replaced by
// the archive feature later, which is why this is pinned now.
func TestDeleteKeepsTheLeafPointingButStopsNavigating(t *testing.T) {
	h := newHarness(t)
	_, leaf := h.seedNotes()
	rowID := h.insertTitle("doomed", []string{leaf})

	remove := write("delete")
	remove.ExpectedRevision = 1
	h.run(`DELETE FROM work.notes WHERE row_id = :row`, map[string]any{"row": rowID}, remove)

	if leaves := h.storedLeaves(rowID); len(leaves) != 1 || leaves[0] != leaf {
		t.Fatalf("stored leaves after delete = %v", leaves)
	}
	if holder := h.leafHolder(leaf); holder != rowID {
		t.Fatalf("leaf holder after delete = %q", holder)
	}
	if opened := h.run(`OPEN ROUTE :leaf LIMIT 1`, map[string]any{"leaf": leaf}, executor.MutationOptions{}); len(opened.Rows) != 0 {
		t.Fatalf("a deleted row must not be navigable: %v", opened.Rows)
	}
	selected := h.run("SELECT * FROM `work`.`notes` WHERE row_id = :row LIMIT 1", map[string]any{"row": rowID}, executor.MutationOptions{})
	if len(selected.Rows) != 0 {
		t.Fatalf("a deleted row must not read: %v", selected.Rows)
	}
}

// DELETE ROUTE is retired from the Agent surface: removing a node is a
// consequence of whatever emptied it, and the engine prunes empty branches
// itself. The diagnostic is specific rather than a generic syntax error.
func TestDeleteRouteIsRetired(t *testing.T) {
	h := newHarness(t)
	_, leaf := h.seedNotes()

	if code := h.fails(`DELETE ROUTE :r`, map[string]any{"r": leaf}, write("retire")); code != result.CodeUnsupported {
		t.Fatalf("code = %s", code)
	}
	if holder := h.leafHolder(leaf); holder != "" {
		t.Fatalf("the refused statement must change nothing: leaf holds %q", holder)
	}
}

// Values are keyed by column ID so that renaming a column never rewrites data.
// The invariant work has to keep that property while it changes what else the
// data table stores.
func TestValuesAreStoredByColumnID(t *testing.T) {
	h := newHarness(t)
	_, leaf := h.seedNotes()
	rowID := h.insertTitle("Use SQLite", []string{leaf})

	var encoded string
	query := `SELECT values_json FROM "data_` + h.notesTableID() + `" WHERE row_id = ?`
	if err := h.db.SQL().QueryRow(query, rowID).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	values := map[string]any{}
	if err := json.Unmarshal([]byte(encoded), &values); err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 {
		t.Fatalf("values = %v", values)
	}
	for key, value := range values {
		if !strings.HasPrefix(key, "col_") {
			t.Fatalf("value key %q must be a column ID, not a name", key)
		}
		if value != "Use SQLite" {
			t.Fatalf("stored value = %v", value)
		}
	}
}

// One statement is one transaction unless an explicit one is open, and a
// rolled-back transaction leaves nothing behind.
func TestTransactionBoundaries(t *testing.T) {
	h := newHarness(t)
	_, leaf := h.seedNotes()

	mutation := write("insert")
	mutation.RouteLeafIDs = []string{leaf}
	h.run(`BEGIN`, nil, executor.MutationOptions{})
	h.run(`INSERT INTO work.notes (title) VALUES ('in flight')`, nil, mutation)
	inside := h.run(`SELECT * FROM work.notes WHERE title = 'in flight' LIMIT 1`, nil, executor.MutationOptions{})
	if len(inside.Rows) != 1 {
		t.Fatalf("a transaction must read its own writes: %v", inside.Rows)
	}
	h.run(`ROLLBACK`, nil, executor.MutationOptions{})
	if h.liveRows() != 0 {
		t.Fatalf("rollback left %d rows behind", h.liveRows())
	}

	h.insertTitle("committed", []string{leaf})
	if h.liveRows() != 1 {
		t.Fatalf("a statement without BEGIN must commit on its own: %d rows", h.liveRows())
	}
	report, err := h.db.Doctor(context.Background())
	if err != nil || report.Integrity != "ok" {
		t.Fatalf("doctor = %+v, %v", report, err)
	}
}

// Required columns and the text ceiling are enforced before anything is
// written, so a rejected insert is not half-applied.
func TestRequiredColumnAndTextCeilingAreEnforcedBeforeWriting(t *testing.T) {
	h := newHarness(t)
	_, leaf := h.seedNotes()

	mutation := write("incomplete")
	mutation.RouteLeafIDs = []string{leaf}
	if code := h.fails(`INSERT INTO work.notes (title) VALUES (NULL)`, nil, mutation); code != result.CodeConstraint {
		t.Fatalf("NULL title code = %s", code)
	}
	tooLong := write("too long")
	tooLong.RouteLeafIDs = []string{leaf}
	code := h.fails(`INSERT INTO work.notes (title) VALUES (:title)`,
		map[string]any{"title": strings.Repeat("字", 1201)}, tooLong)
	if code != result.CodeValueTooLong {
		t.Fatalf("over the ceiling code = %s", code)
	}
	if h.liveRows() != 0 {
		t.Fatalf("rejected inserts must write nothing: %d rows", h.liveRows())
	}
}
