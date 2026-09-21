package sqlstore_test

import (
	"context"
	"testing"

	"github.com/HW-Yue/Memora/internal/sqlstore"
	"github.com/HW-Yue/Memora/internal/sqlstore/vecext"
)

func (h *harness) vectorIndexName() string {
	h.t.Helper()
	var tableID string
	if err := h.db.SQL().QueryRow(`SELECT table_id FROM mem_recall_vec_indexes LIMIT 1`).Scan(&tableID); err != nil {
		h.t.Fatal(err)
	}
	return "mem_recall_vec_" + tableID
}

func (h *harness) nearestUnits(vector []float32, k int) []int64 {
	h.t.Helper()
	encoded, err := vecext.SerializeFloat32(vector)
	if err != nil {
		h.t.Fatal(err)
	}
	rows, err := h.db.SQL().Query(
		`SELECT rowid FROM `+h.vectorIndexName()+` WHERE embedding MATCH ? AND k = ?`, encoded, k)
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	found := []int64{}
	for rows.Next() {
		var unitNo int64
		if err := rows.Scan(&unitNo); err != nil {
			h.t.Fatal(err)
		}
		found = append(found, unitNo)
	}
	if err := rows.Err(); err != nil {
		h.t.Fatal(err)
	}
	return found
}

func (h *harness) acceptUnitVector(rowID string, vector []float32) int64 {
	h.t.Helper()
	unitNo, contentHash := h.recallUnit(rowID)
	if _, err := h.db.Rows().AcceptVector(context.Background(), "work", sqlstore.VectorRecord{
		UnitNo: unitNo, ContentHash: contentHash,
		Model: "text-embedding-v4", Dimensions: len(vector), Vector: vector,
	}); err != nil {
		h.t.Fatal(err)
	}
	return unitNo
}

func TestAcceptedVectorLandsInAnIndexOnDisk(t *testing.T) {
	h := newHarness(t)
	h.seedTree()
	rowID := h.insertAlongPath("storage engine", pathOf("architecture", "sqlite"))
	unitNo := h.acceptUnitVector(rowID, []float32{1, 0})

	if found := h.nearestUnits([]float32{1, 0}, 5); len(found) != 1 || found[0] != unitNo {
		t.Fatalf("the index must answer with the unit just accepted: %v", found)
	}
	if status, err := h.db.Rows().VectorStatus(context.Background(), "work", ""); err != nil || status.NotReady != 0 {
		t.Fatalf("an indexed unit is ready: %+v, %v", status, err)
	}

	// A second handle on the same file proves the index is on disk, not in this
	// process: the plan's acceptance for the vector path is that the index is
	// really there and survives a reopen.
	reopened, err := sqlstore.Open(h.path, sqlstore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	encoded, err := vecext.SerializeFloat32([]float32{1, 0})
	if err != nil {
		t.Fatal(err)
	}
	var rowid int64
	if err := reopened.SQL().QueryRow(
		`SELECT rowid FROM `+h.vectorIndexName()+` WHERE embedding MATCH ? AND k = 1`, encoded).
		Scan(&rowid); err != nil {
		t.Fatal(err)
	}
	if rowid != unitNo {
		t.Fatalf("after reopen the index answered %d, want %d", rowid, unitNo)
	}
}

func TestMovingARowLeavesNoIndexRowBehind(t *testing.T) {
	h := newHarness(t)
	root, leaf := h.seedNotes()
	rowID := h.insertTitle("moves away", []string{leaf})
	oldUnit := h.acceptUnitVector(rowID, []float32{1, 0})
	second := text(h.run(`CREATE ROUTE UNDER :p NAME 'second' KIND 'leaf' PURPOSE 'Second'`,
		map[string]any{"p": root}, write("second")).Rows[0]["route_id"])

	move := write("move the Row to another leaf")
	move.ExpectedRevision = 1
	move.RouteLeafIDs = []string{second}
	h.run(`UPDATE work.notes SET title = 'moved' WHERE row_id = :row`, map[string]any{"row": rowID}, move)

	// The unit is keyed to the leaf, so the old one is gone and its index row
	// has to go with it: recall returns no scores, so a row pointing at a
	// position that no longer exists would be invisible in the answer.
	if found := h.nearestUnits([]float32{1, 0}, 5); len(found) != 0 {
		t.Fatalf("the moved Row left its index row behind: %v", found)
	}
	if _, contentHash := h.recallUnit(rowID); contentHash == "" {
		t.Fatal("the moved Row must have a unit of its own")
	}
	if unitNo, _ := h.recallUnit(rowID); unitNo == oldUnit {
		t.Fatal("a unit is keyed to a leaf, so the new position is a new unit")
	}
}

func TestDeletingARowDropsItsIndexRow(t *testing.T) {
	h := newHarness(t)
	_, leaf := h.seedNotes()
	rowID := h.insertTitle("doomed", []string{leaf})
	h.acceptUnitVector(rowID, []float32{0, 1})
	if found := h.nearestUnits([]float32{0, 1}, 5); len(found) != 1 {
		t.Fatalf("the vector must be indexed before the delete: %v", found)
	}

	h.deleteRow(rowID, 1)
	if found := h.nearestUnits([]float32{0, 1}, 5); len(found) != 0 {
		t.Fatalf("a deleted Row must leave nothing in the index: %v", found)
	}
}
