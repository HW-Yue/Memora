package sqlstore_test

import (
	"context"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
)

// Rows written before the recall layer have no unit, and keyword recall cannot
// find what has no unit — silently, because the notice only covers vectors. The
// repair pass rebuilds the derived layer from the Rows themselves.
func TestRepairRecallUnitsRebuildsWhatTheLayerMissed(t *testing.T) {
	h := newHarness(t)
	h.seedTree()
	for _, title := range []string{"storage engine", "write ahead log", "query planner"} {
		h.insertAlongPath(title, pathOf("architecture", title))
	}
	// Erase the derived layer the way it would look on an Instance written before
	// it existed: no units, and the keyword index holding nothing.
	if _, err := h.db.SQL().Exec(`DELETE FROM mem_recall_units`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.SQL().Exec(`INSERT INTO mem_recall_fts(mem_recall_fts) VALUES('rebuild')`); err != nil {
		t.Fatal(err)
	}
	if paths := h.recallPaths(`RECALL FROM work MATCH :q LIMIT 10`, map[string]any{"q": "storage"}); len(paths) != 0 {
		t.Fatalf("with no units there is nothing to find: %v", paths)
	}
	if report := h.doctor(); report.BrokenRecallUnits != 3 {
		t.Fatalf("three Rows without units: %+v", report.BrokenRecallUnits)
	}

	receipt, err := h.db.Rows().RepairRecallUnits(context.Background(), "work", 100)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Rebuilt != 3 || receipt.Dropped != 0 || receipt.Remaining != 0 {
		t.Fatalf("receipt = %+v", receipt)
	}
	if report := h.doctor(); report.BrokenRecallUnits != 0 {
		t.Fatalf("the pass must leave the layer agreeing with the Rows: %d", report.BrokenRecallUnits)
	}
	// And recall works again for the Rows that had been invisible.
	if paths := h.recallPaths(`RECALL FROM work MATCH :q LIMIT 10`, map[string]any{"q": "storage"}); len(paths) != 1 {
		t.Fatalf("the rebuilt unit must be findable: %v", paths)
	}
}

// An orphaned unit — one whose Row is gone — points at a position that is not
// there, so the pass drops it rather than leaving it to be recalled.
func TestRepairRecallUnitsDropsUnitsWhoseRowIsGone(t *testing.T) {
	h := newHarness(t)
	h.seedTree()
	rowID := h.insertAlongPath("storage engine", pathOf("architecture", "sqlite"))
	if _, err := h.db.SQL().Exec(`UPDATE mem_recall_units SET row_id = 'row_gone' WHERE row_id = ?`, rowID); err != nil {
		t.Fatal(err)
	}
	if report := h.doctor(); report.BrokenRecallUnits != 2 {
		t.Fatalf("one orphan and one Row without a unit: %+v", report.BrokenRecallUnits)
	}
	receipt, err := h.db.Rows().RepairRecallUnits(context.Background(), "work", 100)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Dropped != 1 || receipt.Rebuilt != 1 {
		t.Fatalf("receipt = %+v", receipt)
	}
	if report := h.doctor(); report.BrokenRecallUnits != 0 {
		t.Fatalf("after the pass: %+v", report.BrokenRecallUnits)
	}
	// The pass is repeatable: a converged layer has nothing left to do.
	again, err := h.db.Rows().RepairRecallUnits(context.Background(), "work", 100)
	if err != nil {
		t.Fatal(err)
	}
	if again.Rebuilt != 0 || again.Dropped != 0 || again.Remaining != 0 {
		t.Fatalf("a converged pass must do nothing: %+v", again)
	}
	_ = executor.MutationOptions{}
}

// The statement is the entry a user or agent actually has; the store call alone
// would leave the pass unreachable.
func TestRepairRecallUnitsThroughTheLanguage(t *testing.T) {
	h := newHarness(t)
	h.seedTree()
	h.insertAlongPath("storage engine", pathOf("architecture", "sqlite"))
	if _, err := h.db.SQL().Exec(`DELETE FROM mem_recall_units`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.SQL().Exec(`INSERT INTO mem_recall_fts(mem_recall_fts) VALUES('rebuild')`); err != nil {
		t.Fatal(err)
	}

	options := write("rebuild the derived recall layer")
	options.MaxAffectedRows = 100
	repaired := h.run(`REPAIR RECALL UNITS IN DATABASE work LIMIT :limit`,
		map[string]any{"limit": 100}, options)
	if text(repaired.Rows[0]["rebuilt"]) != "1" || text(repaired.Rows[0]["remaining"]) != "0" {
		t.Fatalf("receipt = %+v", repaired.Rows[0])
	}
	if paths := h.recallPaths(`RECALL FROM work MATCH :q LIMIT 10`, map[string]any{"q": "storage"}); len(paths) != 1 {
		t.Fatalf("the rebuilt unit must be findable: %v", paths)
	}
	// And the pass is bounded like the others.
	h.fails(`REPAIR RECALL UNITS IN DATABASE work LIMIT 100`, nil, write("over budget"))
}
