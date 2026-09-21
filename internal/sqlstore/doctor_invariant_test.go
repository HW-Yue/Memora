package sqlstore_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/HW-Yue/Memora/internal/sqlstore"
)

// The mount invariant is enforced on the write path, so doctor can only ever
// see data that predates the rule or a bug that got past it. These helpers
// fabricate those shapes directly; the report has to name what the write path
// would have refused.

func (h *harness) setMount(rowID string, leaves []string) {
	h.t.Helper()
	encoded, err := json.Marshal(leaves)
	if err != nil {
		h.t.Fatal(err)
	}
	query := `UPDATE "data_` + h.notesTableID() + `" SET route_leaf_ids = ? WHERE row_id = ?`
	if _, err := h.db.SQL().Exec(query, string(encoded), rowID); err != nil {
		h.t.Fatal(err)
	}
}

// setLeafHolder points a leaf at a Row without touching the Row, which is how a
// mount disagreement is made.
func (h *harness) setLeafHolder(leafID, rowID string) {
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
	if rowID == "" {
		delete(fields, "row_id")
	} else {
		fields["row_id"] = rowID
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		h.t.Fatal(err)
	}
	update := `UPDATE "routes_` + h.notesTableID() + `" SET body = ? WHERE route_id = ?`
	if _, err := h.db.SQL().Exec(update, string(encoded), leafID); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) doctor() sqlstore.Report {
	h.t.Helper()
	report, err := h.db.Doctor(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	return report
}

func TestDoctorCountsMountViolations(t *testing.T) {
	h := newHarness(t)
	root, first := h.seedNotes()
	second := text(h.run(`CREATE ROUTE UNDER :p NAME 'second' KIND 'leaf' PURPOSE 'Second'`,
		map[string]any{"p": root}, write("second")).Rows[0]["route_id"])
	third := text(h.run(`CREATE ROUTE UNDER :p NAME 'third' KIND 'leaf' PURPOSE 'Third'`,
		map[string]any{"p": root}, write("third")).Rows[0]["route_id"])

	orphan := h.insertTitle("loses its leaf", []string{first})
	multi := h.insertTitle("gains a second leaf", []string{second})
	h.insertTitle("its leaf points elsewhere", []string{third})

	healthy := h.doctor()
	if healthy.OrphanRows != 0 || healthy.MultiLeafRows != 0 || healthy.MismatchedMounts != 0 {
		t.Fatalf("a healthy instance must report zero violations: %+v", healthy)
	}
	if healthy.Rows != 3 {
		t.Fatalf("rows = %d", healthy.Rows)
	}

	h.setMount(orphan, []string{})
	h.setMount(multi, []string{second, third})
	h.setLeafHolder(third, orphan)

	report := h.doctor()
	if report.OrphanRows != 1 || report.MultiLeafRows != 1 || report.MismatchedMounts != 1 {
		t.Fatalf("violations = orphan %d, multi %d, mismatched %d",
			report.OrphanRows, report.MultiLeafRows, report.MismatchedMounts)
	}
	if report.Status != "healthy" || report.Integrity != "ok" {
		t.Fatalf("counting violations must not change integrity reporting: %+v", report)
	}
}
