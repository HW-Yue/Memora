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

func (h *harness) repairVectorIndex(source string) int {
	h.t.Helper()
	options := write("reconcile the derived vector index")
	options.MaxAffectedRows = 1000
	result := h.run(source, nil, options)
	return int(result.AffectedRows)
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

// The acceptance the plan asks for is that incremental upkeep and a full replay
// agree: drop the derived index, reconcile, and the same query must answer with
// the same positions.
func TestRepairReplaysTheIndexFromTheTruth(t *testing.T) {
	h := newHarness(t)
	h.seedTree()
	first := h.insertAlongPath("storage engine", pathOf("architecture", "sqlite"))
	second := h.insertAlongPath("write ahead log", pathOf("architecture", "wal"))
	firstUnit := h.acceptUnitVector(first, []float32{1, 0})
	h.acceptUnitVector(second, []float32{0, 1})
	before := h.nearestUnits([]float32{1, 0}, 5)

	// The index is derived, so losing it must be recoverable from the units —
	// including the registry row claiming an index that is no longer there.
	index := h.vectorIndexName()
	if _, err := h.db.SQL().Exec(`DROP TABLE ` + index); err != nil {
		t.Fatal(err)
	}
	if found := h.repairVectorIndex(`REPAIR VECTOR INDEX IN DATABASE work LIMIT 8`); found != 2 {
		t.Fatalf("the pass must replay both units: %d", found)
	}
	after := h.nearestUnits([]float32{1, 0}, 5)
	if len(after) != len(before) {
		t.Fatalf("a rebuilt index must answer the same set: %v then %v", before, after)
	}
	for index := range before {
		if before[index] != after[index] {
			t.Fatalf("a rebuilt index must answer in the same order: %v then %v", before, after)
		}
	}
	if len(after) == 0 || after[0] != firstUnit {
		t.Fatalf("the nearest unit after the rebuild = %v, want %d first", after, firstUnit)
	}
	// A second pass has nothing left to do, which is what makes "repeat until
	// remaining is zero" a safe instruction.
	if status, err := h.db.Rows().VectorStatus(context.Background(), "work", ""); err != nil || status.NotReady != 0 {
		t.Fatalf("both units are ready: %+v, %v", status, err)
	}
	if found := h.repairVectorIndex(`REPAIR VECTOR INDEX IN DATABASE work LIMIT 8`); found != 0 {
		t.Fatalf("a converged index needs no repair: %d", found)
	}
}

// A pass is bounded like every other maintenance write, and it reports the work
// it did not do rather than pretending to have finished.
func TestRepairIsBoundedAndReportsWhatIsLeft(t *testing.T) {
	h := newHarness(t)
	h.seedTree()
	h.acceptUnitVector(h.insertAlongPath("storage engine", pathOf("architecture", "sqlite")), []float32{1, 0})
	h.acceptUnitVector(h.insertAlongPath("write ahead log", pathOf("architecture", "wal")), []float32{0, 1})
	if _, err := h.db.SQL().Exec(`DROP TABLE ` + h.vectorIndexName()); err != nil {
		t.Fatal(err)
	}

	receipt := h.run(`REPAIR VECTOR INDEX IN DATABASE work LIMIT 1`, nil, write("rebuild one unit"))
	if text(receipt.Rows[0]["repaired"]) != "1" || text(receipt.Rows[0]["remaining"]) != "1" {
		t.Fatalf("a bounded pass must report the rest: %v", receipt.Rows[0])
	}
	// And the statement refuses a limit its caller has not budgeted for.
	h.fails(`REPAIR VECTOR INDEX IN DATABASE work LIMIT 8`, nil, write("over budget"))
}

func TestVectorRecallAnswersWithAPositionAndNothingElse(t *testing.T) {
	h := newHarness(t)
	h.seedTree()
	near := h.insertAlongPath("storage engine", pathOf("architecture", "sqlite"))
	other := h.insertAlongPath("unrelated", pathOf("architecture", "wal"))
	h.acceptUnitVector(near, []float32{1, 0})
	h.acceptUnitVector(other, []float32{0, 1})

	hits, err := h.db.Rows().RecallNearest(context.Background(), "work", "", []float32{1, 0}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("both units are within k: %+v", hits)
	}
	// The nearest comes first internally, but the listing is lexicographic like
	// the keyword arm: the answer is a position, not a ranking.
	if len(hits[0].Path) == 0 || hits[0].Path[len(hits[0].Path)-1].RouteID == "" {
		t.Fatalf("a hit must carry the path navigation needs: %+v", hits[0])
	}
	if hits[0].ObjectID == "" || hits[0].Kind != "leaf" || hits[0].Database != "work" {
		t.Fatalf("hit = %+v", hits[0])
	}
	// Nothing beyond the frozen shape: a score would be a ranking in disguise.
	if hits[0].Table == "" || hits[0].Database == "" {
		t.Fatalf("hit = %+v", hits[0])
	}
}

func TestVectorRecallSkipsAVectorThatNoLongerDescribesTheText(t *testing.T) {
	h := newHarness(t)
	_, leaf := h.seedNotes()
	rowID := h.insertTitle("storage engine", []string{leaf})
	h.acceptUnitVector(rowID, []float32{1, 0})
	if hits, err := h.db.Rows().RecallNearest(context.Background(), "work", "", []float32{1, 0}, 5); err != nil || len(hits) != 1 {
		t.Fatalf("the fresh vector must be found: %+v, %v", hits, err)
	}

	refine := write("edit the text the vector was computed from")
	refine.ExpectedRevision = 1
	h.run(`UPDATE work.notes SET title = 'storage engine, reconsidered' WHERE row_id = :row`,
		map[string]any{"row": rowID}, refine)

	// The bytes are still in the index, and they describe a sentence the unit no
	// longer holds. Recall has no scores, so returning it would be a wrong answer
	// that looks exactly like a right one.
	hits, err := h.db.Rows().RecallNearest(context.Background(), "work", "", []float32{1, 0}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("a stale vector must not answer: %+v", hits)
	}

	// Re-embedding the current text makes it answerable again.
	_, currentHash := h.recallUnit(rowID)
	unitNo, _ := h.recallUnit(rowID)
	if _, err := h.db.Rows().AcceptVector(context.Background(), "work", sqlstore.VectorRecord{
		UnitNo: unitNo, ContentHash: currentHash,
		Model: "text-embedding-v4", Dimensions: 2, Vector: []float32{1, 0},
	}); err != nil {
		t.Fatal(err)
	}
	if hits, err := h.db.Rows().RecallNearest(context.Background(), "work", "", []float32{1, 0}, 5); err != nil || len(hits) != 1 {
		t.Fatalf("a re-embedded unit answers again: %+v, %v", hits, err)
	}
}

func TestVectorRecallRefusesAQueryItCannotAnswer(t *testing.T) {
	h := newHarness(t)
	h.seedTree()
	rowID := h.insertAlongPath("storage engine", pathOf("architecture", "sqlite"))
	ctx := context.Background()

	// Before any vector is accepted the Database has no identity: a vector query
	// has nothing to search, which is a different answer from "no match".
	if _, err := h.db.Rows().RecallNearest(ctx, "work", "", []float32{1, 0}, 5); err == nil {
		t.Fatal("a vector query without a locked identity must be refused")
	}
	h.acceptUnitVector(rowID, []float32{1, 0})
	if _, err := h.db.Rows().RecallNearest(ctx, "work", "", []float32{1, 0, 0}, 5); err == nil {
		t.Fatal("a query of the wrong width must be refused")
	}
}
