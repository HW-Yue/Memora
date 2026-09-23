package sqlstore_test

import (
	"encoding/json"
	"testing"

	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/msql/executor"
)

// A reshape changes a Row's identity, not its content, so the sources stop at
// their last in-place revision and the new Rows carry the lineage instead:
// history records only in-place edits, and a pointer on the new Row's first
// record is what makes the chain back to before the reshape readable.
// See docs/product/history-lineage.md.

func (h *harness) rawRowField(rowID, column string) string {
	h.t.Helper()
	var value string
	query := `SELECT ` + column + ` FROM "data_` + h.notesTableID() + `" WHERE row_id = ?`
	if err := h.db.SQL().QueryRow(query, rowID).Scan(&value); err != nil {
		h.t.Fatal(err)
	}
	return value
}

func (h *harness) historyRecords(rowID string) []history.Record {
	h.t.Helper()
	var count int
	query := `SELECT COUNT(*) FROM "history_` + h.notesTableID() + `" WHERE row_id = ?`
	if err := h.db.SQL().QueryRow(query, rowID).Scan(&count); err != nil {
		h.t.Fatal(err)
	}
	records := []history.Record{}
	rows, err := h.db.SQL().Query(`SELECT body FROM "history_`+h.notesTableID()+`" WHERE row_id = ? ORDER BY revision`, rowID)
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			h.t.Fatal(err)
		}
		record := history.Record{}
		if err := json.Unmarshal([]byte(body), &record); err != nil {
			h.t.Fatal(err)
		}
		records = append(records, record)
	}
	if len(records) != count {
		h.t.Fatalf("history rows = %d, decoded %d", count, len(records))
	}
	return records
}

type splitLeaves struct {
	seed, first, second string
}

func (h *harness) seedThreeLeaves() (splitLeaves, string) {
	h.t.Helper()
	root := h.seedTree()
	leaves := splitLeaves{}
	for index, name := range []string{"seed", "first", "second"} {
		id := text(h.run(`CREATE ROUTE UNDER :p NAME :name KIND 'leaf' PURPOSE :purpose`,
			map[string]any{"p": root, "name": name, "purpose": name + " purpose"}, write(name)).Rows[0]["route_id"])
		switch index {
		case 0:
			leaves.seed = id
		case 1:
			leaves.first = id
		case 2:
			leaves.second = id
		}
	}
	return leaves, root
}

func TestSplitLeavesTheSourceRevisionAloneAndPointsTheNewHistoriesAtIt(t *testing.T) {
	h := newHarness(t)
	leaves, root := h.seedThreeLeaves()
	source := h.insertTitle("both facts", []string{leaves.seed})

	split := write("split")
	split.ExpectedRevision = 1
	split.MaxAffectedRows = 3
	split.TargetRouteLeafIDs = [][]string{{leaves.first}, {leaves.second}}
	created := h.run(`SPLIT work.notes ROW :row INTO (title) VALUES ('fact one'), ('fact two')`,
		map[string]any{"row": source}, split)

	// The source keeps its revision and its single in-place history record, so
	// (row_id, revision) has no hole; successor_ids carries the identity change.
	if revision := h.rawRowField(source, "revision"); revision != "1" {
		t.Fatalf("source revision = %s, want 1", revision)
	}
	if state := h.rawRowField(source, "row_state"); state != "superseded" {
		t.Fatalf("source state = %s", state)
	}
	sourceHistory := h.historyRecords(source)
	if len(sourceHistory) != 1 || sourceHistory[0].Operation != history.OperationInsert {
		t.Fatalf("source history = %+v", sourceHistory)
	}
	if sourceHistory[0].Revision != 1 {
		t.Fatalf("source history revision = %d", sourceHistory[0].Revision)
	}
	var successors []string
	if err := json.Unmarshal([]byte(h.rawRowField(source, "successor_ids")), &successors); err != nil {
		t.Fatal(err)
	}
	if len(successors) != len(created.Rows) {
		t.Fatalf("successor_ids = %v, created %d", successors, len(created.Rows))
	}

	// Each new Row's first history record names the Row it continues, at the
	// revision the source stopped at.
	for _, row := range created.Rows {
		rowID := text(row["row_id"])
		records := h.historyRecords(rowID)
		if len(records) != 1 || records[0].Operation != history.OperationInsert {
			t.Fatalf("target %s history = %+v", rowID, records)
		}
		if len(records[0].Origins) != 1 {
			t.Fatalf("target %s origins = %+v", rowID, records[0].Origins)
		}
		if records[0].Origins[0].RowID != source || records[0].Origins[0].Revision != 1 {
			t.Fatalf("target %s origin = %+v", rowID, records[0].Origins[0])
		}
	}

	// The lineage is readable through the surface, not only in the file.
	shown := h.run(`SHOW HISTORY FROM work.notes FOR ROW :row LIMIT 20`,
		map[string]any{"row": text(created.Rows[0]["row_id"])}, executor.MutationOptions{})
	origins := []history.Origin{}
	if err := json.Unmarshal([]byte(text(shown.Rows[0]["origins"])), &origins); err != nil {
		t.Fatalf("origins column = %v (%v)", shown.Rows[0]["origins"], err)
	}
	if len(origins) != 1 || origins[0].RowID != source {
		t.Fatalf("origins from SHOW HISTORY = %+v", origins)
	}
	// A Row that was never reshaped has no lineage edge at all.
	leaf := text(h.run(`CREATE ROUTE UNDER :p NAME 'plain' KIND 'leaf' PURPOSE 'a position with nothing special about it'`,
		map[string]any{"p": root}, write("plain")).Rows[0]["route_id"])
	plain := h.insertTitle("plain", []string{leaf})
	shown = h.run(`SHOW HISTORY FROM work.notes FOR ROW :row LIMIT 20`, map[string]any{"row": plain}, executor.MutationOptions{})
	if value, present := shown.Rows[0]["origins"]; present && value != nil {
		t.Fatalf("a plain Row must have no origins: %v", value)
	}
}

func TestMergePointsOneHistoryAtEverySource(t *testing.T) {
	h := newHarness(t)
	leaves, _ := h.seedThreeLeaves()
	first := h.insertTitle("first half", []string{leaves.seed})
	second := h.insertTitle("second half", []string{leaves.first})

	merge := write("merge")
	merge.MaxAffectedRows = 3
	merge.SourceRevisions = map[string]uint64{first: 1, second: 1}
	merge.TargetRouteLeafIDs = [][]string{{leaves.second}}
	merged := h.run(`MERGE work.notes ROWS (:first, :second) INTO (title) VALUES ('both halves')`,
		map[string]any{"first": first, "second": second}, merge)

	rowID := text(merged.Rows[0]["row_id"])
	records := h.historyRecords(rowID)
	if len(records) != 1 {
		t.Fatalf("merged history = %+v", records)
	}
	if len(records[0].Origins) != 2 {
		t.Fatalf("origins = %+v", records[0].Origins)
	}
	seen := map[string]bool{}
	for _, origin := range records[0].Origins {
		seen[origin.RowID] = true
		if origin.Revision != 1 {
			t.Fatalf("origin revision = %d", origin.Revision)
		}
	}
	if !seen[first] || !seen[second] {
		t.Fatalf("origins must name both sources: %+v", records[0].Origins)
	}

	// Both sources keep their own single record and their revision.
	for _, source := range []string{first, second} {
		if revision := h.rawRowField(source, "revision"); revision != "1" {
			t.Fatalf("source %s revision = %s", source, revision)
		}
		if records := h.historyRecords(source); len(records) != 1 {
			t.Fatalf("source %s history = %+v", source, records)
		}
	}
}
