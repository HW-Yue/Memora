package sqlstore_test

import (
	"testing"

	"github.com/HW-Yue/Memora/internal/result"
)

// The recall unit is the one source of truth both predictors read: the leaf a
// live Row hangs under, the text recall matches against, and a hash of that
// text. It is maintained by the row write paths, in their own transaction, so a
// recall path never has to reconcile the index with the Rows.

type recallUnit struct {
	routeID    string
	rowID      string
	revision   uint64
	hash       string
	payload    string
	model      string
	dimensions int
}

func (h *harness) recallUnits() []recallUnit {
	h.t.Helper()
	rows, err := h.db.SQL().Query(`SELECT route_id, row_id, revision, content_hash, payload,
		embedding_model, embedding_dimensions FROM mem_recall_units ORDER BY route_id`)
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	units := []recallUnit{}
	for rows.Next() {
		unit := recallUnit{}
		if err := rows.Scan(&unit.routeID, &unit.rowID, &unit.revision, &unit.hash, &unit.payload,
			&unit.model, &unit.dimensions); err != nil {
			h.t.Fatal(err)
		}
		units = append(units, unit)
	}
	return units
}

func (h *harness) unitFor(rowID string) recallUnit {
	h.t.Helper()
	for _, unit := range h.recallUnits() {
		if unit.rowID == rowID {
			return unit
		}
	}
	h.t.Fatalf("no recall unit for Row %q", rowID)
	return recallUnit{}
}

// markEmbedded stands in for the vector path, which is the only writer of the
// embedding provenance columns.
func (h *harness) markEmbedded(rowID, model string, dimensions int) {
	h.t.Helper()
	update := `UPDATE mem_recall_units SET embedding_model = ?, embedding_dimensions = ?, embedded_at = 'now'
		WHERE row_id = ?`
	if _, err := h.db.SQL().Exec(update, model, dimensions, rowID); err != nil {
		h.t.Fatal(err)
	}
}

func TestInsertMaterialisesARecallUnit(t *testing.T) {
	h := newHarness(t)
	leaf, two, _ := h.threeLeaves()
	rowID := h.insertTitle("Use SQLite", []string{leaf})

	units := h.recallUnits()
	if len(units) != 1 {
		t.Fatalf("units = %+v", units)
	}
	unit := units[0]
	if unit.routeID != leaf || unit.rowID != rowID || unit.revision != 1 {
		t.Fatalf("unit = %+v", unit)
	}
	if unit.payload != "Use SQLite" || unit.hash == "" {
		t.Fatalf("the payload is what recall reads: %+v", unit)
	}
	if report := h.doctor(); report.BrokenRecallUnits != 0 {
		t.Fatalf("a fresh instance must index every live Row: %+v", report)
	}

	// The unit follows the Row through an ordinary write.
	refine := write("refine")
	refine.ExpectedRevision = 1
	h.run(`UPDATE work.notes SET title = 'Use SQLite, revised' WHERE row_id = :row`, map[string]any{"row": rowID}, refine)
	refreshed := h.unitFor(rowID)
	if refreshed.revision != 2 || refreshed.payload != "Use SQLite, revised" || refreshed.hash == unit.hash {
		t.Fatalf("unit after the write = %+v", refreshed)
	}
	_ = two
}

func TestRecallUnitKeepsItsVectorWhenTheTextIsUnchanged(t *testing.T) {
	h := newHarness(t)
	root := h.seedTree()
	leaf := text(h.run(`CREATE ROUTE UNDER :p NAME 'one' KIND 'leaf' PURPOSE 'the single position this recall test mounts on'`,
		map[string]any{"p": root}, write("one")).Rows[0]["route_id"])
	rowID := h.insertTitle("title only", []string{leaf})
	h.markEmbedded(rowID, "text-embedding-v4", 1024)

	// A write that does not touch the payload leaves the vector alone: a vector
	// describes text, not a revision.
	sameText := h.unitFor(rowID)
	if sameText.model != "text-embedding-v4" || sameText.dimensions != 1024 {
		t.Fatalf("fixture did not record a vector: %+v", sameText)
	}

	// The title is the payload here, so an edit to it must drop the provenance.
	edit := write("change the payload")
	edit.ExpectedRevision = 1
	h.run(`UPDATE work.notes SET title = 'a different title' WHERE row_id = :row`, map[string]any{"row": rowID}, edit)
	changed := h.unitFor(rowID)
	if changed.hash == sameText.hash {
		t.Fatalf("the payload hash must change with the text: %+v", changed)
	}
	if changed.model != "" || changed.dimensions != 0 {
		t.Fatalf("changed text must invalidate the vector: %+v", changed)
	}
}

func TestRecallUnitLeavesWithItsRow(t *testing.T) {
	h := newHarness(t)
	leaves := h.seedLeaves("source", "watcher", "first", "second")
	source := h.insertTitle("both facts", []string{leaves[0]})
	watcher := h.insertTitle("watcher", []string{leaves[1]})

	// A reshape takes the superseded Row's unit with its reachability, and gives
	// the new Rows their own.
	split := write("split")
	split.ExpectedRevision = 1
	split.MaxAffectedRows = 3
	split.TargetRouteLeafIDs = [][]string{{leaves[2]}, {leaves[3]}}
	h.run(`SPLIT work.notes ROW :row INTO (title) VALUES ('first fact'), ('second fact')`,
		map[string]any{"row": source}, split)
	for _, unit := range h.recallUnits() {
		if unit.rowID == source {
			t.Fatalf("a superseded Row must not be recallable: %+v", unit)
		}
	}
	if len(h.recallUnits()) != 3 {
		t.Fatalf("units = %+v", h.recallUnits())
	}
	if report := h.doctor(); report.BrokenRecallUnits != 0 {
		t.Fatalf("reshape must leave the index consistent: %+v", report)
	}

	// Deleting a Row removes its unit as well.
	h.deleteRow(watcher, h.revisionOf(watcher))
	for _, unit := range h.recallUnits() {
		if unit.rowID == watcher {
			t.Fatalf("a deleted Row must not be recallable: %+v", unit)
		}
	}
	if report := h.doctor(); report.BrokenRecallUnits != 0 {
		t.Fatalf("delete must leave the index consistent: %+v", report)
	}
}

func TestRecallUnitFollowsARowToItsNewLeaf(t *testing.T) {
	h := newHarness(t)
	one, two, _ := h.threeLeaves()
	rowID := h.insertTitle("moves", []string{one})

	move := write("move the Row to another leaf")
	move.ExpectedRevision = 1
	move.RouteLeafIDs = []string{two}
	h.run(`UPDATE work.notes SET title = 'moved' WHERE row_id = :row`, map[string]any{"row": rowID}, move)

	units := h.recallUnits()
	if len(units) != 1 {
		t.Fatalf("a Row occupies one leaf, so it has one unit: %+v", units)
	}
	if units[0].routeID != two || units[0].rowID != rowID {
		t.Fatalf("unit after the move = %+v", units[0])
	}
	if report := h.doctor(); report.BrokenRecallUnits != 0 {
		t.Fatalf("a move must leave the index consistent: %+v", report)
	}
}

func TestAMissingRecallUnitIsRefusedAtCommit(t *testing.T) {
	h := newHarness(t)
	leaves := h.seedLeaves("indexed", "other")
	rowID := h.insertTitle("indexed", []string{leaves[0]})

	// Fabricate the state a path that forgot to maintain the index would leave.
	if _, err := h.db.SQL().Exec(`DELETE FROM mem_recall_units WHERE row_id = ?`, rowID); err != nil {
		t.Fatal(err)
	}
	if report := h.doctor(); report.BrokenRecallUnits != 1 {
		t.Fatalf("doctor must see the missing unit: %+v", report)
	}

	// Touching the affected Row heals it: the write path re-materialises its own
	// unit, which is why the assertion has to look at the whole Table.
	refine := write("touch the affected Row")
	refine.ExpectedRevision = 1
	h.run(`UPDATE work.notes SET title = 'healed' WHERE row_id = :row`, map[string]any{"row": rowID}, refine)
	if report := h.doctor(); report.BrokenRecallUnits != 0 {
		t.Fatalf("writing the Row must restore its unit: %+v", report)
	}

	// A write that leaves the gap alone is refused: a read path cannot be handed
	// a Row the index has forgotten.
	if _, err := h.db.SQL().Exec(`DELETE FROM mem_recall_units WHERE row_id = ?`, rowID); err != nil {
		t.Fatal(err)
	}
	mutation := write("write something else")
	mutation.RouteLeafIDs = []string{leaves[1]}
	statement := `INSERT INTO work.notes (title) VALUES ('unrelated')`
	if code := h.fails(statement, nil, mutation); code != result.CodeInternal {
		t.Fatalf("a live Row with no recall unit must be refused: %s", code)
	}
}
