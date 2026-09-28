package sqlstore_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/sqlstore"
)

// A live Row that does not hold exactly one leaf has no single position a unit
// can name. The pass used to count it as rebuilt — and delete whatever unit the
// Row still had — so every round reported rebuilt=1 while the derived layer got
// worse and nobody could learn that the Row needs its mount fixed first.
func TestRepairRecallUnitsReportsRowsItCannotIndex(t *testing.T) {
	// The commit-time invariant scan reads every Row, so it would refuse the
	// damaged mount before the repair pass is reached; a damaged Instance is
	// exactly the one a repair pass exists for.
	h := newHarnessWithOptions(t, sqlstore.Options{})
	root := h.seedTree()
	rowID := h.insertAlongPath("two homes", pathOf("architecture", "sqlite"))
	first := h.mountedLeaves(rowID)
	if len(first) != 1 {
		t.Fatalf("a fresh Row holds one leaf: %v", first)
	}
	second := text(h.run(
		`CREATE ROUTE UNDER :p NAME 'other' KIND 'leaf' PURPOSE 'a second position, so one Row is mounted twice by hand'`,
		map[string]any{"p": root}, write("second leaf")))
	// The write path forbids this; an Instance from before the rule can hold it.
	h.setMount(rowID, []string{first[0], second})

	// The derived layer itself agrees with the Rows: one Row, one unit.
	if report := h.doctor(); report.BrokenRecallUnits != 0 {
		t.Fatalf("the damage is in the mount, not the derived layer: %+v", report.BrokenRecallUnits)
	}

	pass, err := h.db.Rows().RepairRecallUnits(context.Background(), "work", 100)
	if err != nil {
		t.Fatal(err)
	}
	if pass.Rebuilt != 0 {
		t.Fatalf("a Row that cannot hold one unit was not rebuilt: %+v", pass)
	}
	if pass.Blocked != 1 {
		t.Fatalf("the pass must name what it could not index: %+v", pass)
	}
	// It must not make the layer worse on its way out: the unit it had stays, and
	// what keyword recall could still reach is unchanged.
	if report := h.doctor(); report.BrokenRecallUnits != 0 {
		t.Fatalf("the blocked Row keeps the unit it had: %+v", report.BrokenRecallUnits)
	}
	if paths := h.recallPaths(`RECALL FROM work MATCH :q LIMIT 10`, map[string]any{"q": "two homes"}); len(paths) != 1 {
		t.Fatalf("the Row keyword recall could still reach must stay reachable: %v", paths)
	}
	// And it converges: the second round says exactly what the first said, instead
	// of reporting the same rebuild forever.
	again, err := h.db.Rows().RepairRecallUnits(context.Background(), "work", 100)
	if err != nil {
		t.Fatal(err)
	}
	if again != pass {
		t.Fatalf("a pass that cannot finish must be steady: %+v then %+v", pass, again)
	}

	// The statement is the surface a caller reads, so the count travels and the
	// cause is named: a bare count would read as "the rest is queued".
	options := write("rebuild the derived recall layer")
	options.MaxAffectedRows = 100
	reported := h.run(`REPAIR RECALL UNITS IN DATABASE work LIMIT :limit`,
		map[string]any{"limit": 100}, options)
	if text(reported.Rows[0]["blocked"]) != "1" || text(reported.Rows[0]["rebuilt"]) != "0" {
		t.Fatalf("receipt = %+v", reported.Rows[0])
	}
	named := false
	for _, notice := range reported.Warnings {
		if notice.Code == result.CodeRecallUnitsBlocked {
			named = true
		}
	}
	if !named {
		t.Fatalf("the refusal must be visible as a notice: %+v", reported.Warnings)
	}
}

// mountedLeaves reads the positions a Row holds, which is what the mount
// invariant constrains.
func (h *harness) mountedLeaves(rowID string) []string {
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
