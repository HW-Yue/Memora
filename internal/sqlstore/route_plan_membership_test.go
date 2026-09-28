package sqlstore_test

import (
	"encoding/json"
	"testing"

	"github.com/HW-Yue/Memora/internal/change"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/routemutationplan"
)

// Merging two leaves into one moves their Rows onto the new leaf. That is a write
// to those Rows, and it used to be the one write that left no trace: a bare
// `UPDATE ... SET route_leaf_ids`, with the revision, the history and the change
// log all untouched, while the receipt still counted a membership revision. An
// audit that cannot see a mount change is not an audit.
func TestMergingLeavesAccountsForTheMovedRows(t *testing.T) {
	h := newHarness(t)
	root := h.seedTree()
	moving := h.insertAlongPath("the Row that moves", pathOf("first"))
	firstLeaf := h.mountedLeaves(moving)[0]
	// One leaf holds the Row and the other is empty: merging them is legal
	// (one Row per leaf), and it moves the Row onto the new leaf.
	secondLeaf := text(h.run(
		`CREATE ROUTE UNDER :p NAME 'second' KIND 'leaf' PURPOSE 'an empty second position to merge'`,
		map[string]any{"p": root}, write("second leaf")).Rows[0]["route_id"])
	// A leaf created by the INSERT that mounted a Row carries whatever revision
	// that took; the proposal has to name it, so read it rather than guess.
	firstRevision := h.run(`DESCRIBE ROUTE :r`, map[string]any{"r": firstLeaf}, executor.MutationOptions{}).Rows[0]["revision"]
	secondRevision := h.run(`DESCRIBE ROUTE :r`, map[string]any{"r": secondLeaf}, executor.MutationOptions{}).Rows[0]["revision"]
	revisionBefore := h.rawRowField(moving, "revision")
	historyBefore := h.historyCount(moving)

	proposal := map[string]any{
		"version": "memora.route-mutation-proposal/v1", "proposal_id": "route-proposal-merge",
		"operation": "MERGE", "actor": "agent:test", "source_event_id": "test:merge-leaves",
		"reason": "keep the two positions as one",
		"sources": []any{
			map[string]any{"route_id": firstLeaf, "expected_revision": firstRevision},
			map[string]any{"route_id": secondLeaf, "expected_revision": secondRevision},
		},
		"targets": []any{map[string]any{
			"key": "merged", "name": "merged", "purpose": "the two positions kept as one",
		}},
	}
	planned := h.run(`PLAN ROUTE MUTATION FOR TABLE work.notes USING :proposal`,
		map[string]any{"proposal": proposal}, executor.MutationOptions{})
	if status := text(planned.Rows[0]["status"]); status != "review_required" {
		t.Fatalf("plan status = %q", status)
	}
	applied := h.runAuthorized(approvedFor(text(planned.Rows[0]["plan_hash"])),
		`APPLY ROUTE MUTATION PLAN :plan FOR TABLE work.notes`,
		map[string]any{"plan": planned.Rows[0]["route_mutation_plan"]}, write("apply the reviewed plan"))

	// The Row moved, so it was written: the revision advanced.
	if after := h.rawRowField(moving, "revision"); after == revisionBefore {
		t.Fatalf("a membership move is a write: revision stayed at %s", after)
	}
	// History records it, carrying the plan's own provenance — not a system default.
	records := h.historyRecords(moving)
	if len(records) != historyBefore+1 {
		t.Fatalf("history records = %d, want %d", len(records), historyBefore+1)
	}
	moved := records[len(records)-1]
	if moved.Actor != "agent:test" || moved.Reason != "keep the two positions as one" ||
		moved.Source != "test:merge-leaves" {
		t.Fatalf("the move's history must carry the plan's provenance: %+v", moved)
	}
	// And the change log names the Row, which is what an audit walks.
	if !h.changeLogNamesRow(moving) {
		t.Fatalf("the change log must contain an entry for the moved Row")
	}
	// The Row now sits in the merged leaf, and the leaf it left is gone.
	merged := h.mountedLeaves(moving)
	if len(merged) != 1 || merged[0] == firstLeaf {
		t.Fatalf("the moved Row must sit in the merged leaf: %v", merged)
	}
	if after := h.mountedLeaves(moving); len(after) != 1 {
		t.Fatalf("a Row holds exactly one leaf: %v", after)
	}
	// The receipt's claim is now backed by the write above.
	receipt, ok := applied.Rows[0]["route_mutation_receipt"].(routemutationplan.Receipt)
	if !ok {
		t.Fatalf("receipt is %T", applied.Rows[0]["route_mutation_receipt"])
	}
	if receipt.MembershipRevisions != 1 {
		t.Fatalf("receipt = %+v", receipt)
	}
}

// changeLogNamesRow reports whether any committed change envelope carries an
// entry for this Row's own update.
func (h *harness) changeLogNamesRow(rowID string) bool {
	h.t.Helper()
	rows, err := h.db.SQL().Query(`SELECT body FROM mem_changes ORDER BY sequence`)
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			h.t.Fatal(err)
		}
		envelope := change.Envelope{}
		if err := json.Unmarshal([]byte(body), &envelope); err != nil {
			h.t.Fatal(err)
		}
		for _, entry := range envelope.Entries {
			if entry.ObjectKind == change.ObjectRow && entry.ObjectID == rowID && entry.Operation == change.OperationUpdate {
				return true
			}
		}
	}
	if err := rows.Err(); err != nil {
		h.t.Fatal(err)
	}
	return false
}
