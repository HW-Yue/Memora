package sqlstore_test

import (
	"strconv"
	"testing"

	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/sqlstore"
)

// A reshape cannot re-point every link that points at it without blocking on
// them, so it queues the endpoints instead. The queue is written by the writer,
// in its own transaction: a reader cannot write, and a crash must not leave
// links stale with an empty queue. Until an endpoint is repaired the link is
// legitimate rather than broken — which is what the invariant assertion now
// says, and why a linked Row can be split at all.

func (h *harness) seedLeaves(names ...string) []string {
	h.t.Helper()
	root := h.seedTree()
	ids := []string{}
	for _, name := range names {
		ids = append(ids, text(h.run(`CREATE ROUTE UNDER :p NAME :name KIND 'leaf' PURPOSE :purpose`,
			map[string]any{"p": root, "name": name, "purpose": name + " purpose"}, write(name)).Rows[0]["route_id"]))
	}
	return ids
}

func (h *harness) revisionOf(rowID string) uint64 {
	h.t.Helper()
	revision, err := strconv.ParseUint(h.rawRowField(rowID, "revision"), 10, 64)
	if err != nil {
		h.t.Fatal(err)
	}
	return revision
}

type repairRow struct {
	tableID            string
	rowID              string
	counterpartTableID string
	counterpartRowID   string
	reason             string
}

func (h *harness) repairs() []repairRow {
	h.t.Helper()
	rows, err := h.db.SQL().Query(`SELECT table_id, row_id, counterpart_table_id, counterpart_row_id, reason
		FROM mem_repairs ORDER BY row_id`)
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	repairs := []repairRow{}
	for rows.Next() {
		entry := repairRow{}
		if err := rows.Scan(&entry.tableID, &entry.rowID, &entry.counterpartTableID,
			&entry.counterpartRowID, &entry.reason); err != nil {
			h.t.Fatal(err)
		}
		repairs = append(repairs, entry)
	}
	return repairs
}

func TestReshapeQueuesTheLinksThatPointedAtTheSource(t *testing.T) {
	h := newHarness(t)
	leaves := h.seedLeaves("source", "watcher", "first", "second")
	source := h.insertTitle("both facts", []string{leaves[0]})
	watcher := h.insertTitle("watcher", []string{leaves[1]})
	h.setRowLinks(watcher, 1, []sqlstore.LinkRef{{RowID: source}})

	split := write("split a linked Row")
	split.ExpectedRevision = h.revisionOf(source)
	split.MaxAffectedRows = 3
	split.TargetRouteLeafIDs = [][]string{{leaves[2]}, {leaves[3]}}
	h.run(`SPLIT work.notes ROW :row INTO (title) VALUES ('first fact'), ('second fact')`,
		map[string]any{"row": source}, split)

	// The source is superseded, and the endpoint that pointed at it is queued.
	if state := h.rawRowField(source, "row_state"); state != "superseded" {
		t.Fatalf("source state = %s", state)
	}
	repairs := h.repairs()
	if len(repairs) != 1 {
		t.Fatalf("repairs = %+v", repairs)
	}
	if repairs[0].rowID != watcher || repairs[0].counterpartRowID != source ||
		repairs[0].reason != sqlstore.RepairStaleReference {
		t.Fatalf("repair = %+v", repairs[0])
	}
	// Queued, so the link is pending rather than broken.
	if report := h.doctor(); report.BrokenLinks != 0 {
		t.Fatalf("a queued link must not count as broken: %+v", report)
	}
	// The source keeps its own links: clearing them would remove the back edges
	// the repair needs.
	if links := h.links(source); len(links) != 1 || links[0].RowID != watcher {
		t.Fatalf("the superseded Row must keep its links: %+v", links)
	}
	if links := h.links(watcher); len(links) != 1 || links[0].RowID != source {
		t.Fatalf("the link is repaired later, not rewritten here: %+v", links)
	}
}

func TestReshapeWithoutInboundLinksQueuesNothing(t *testing.T) {
	h := newHarness(t)
	leaves := h.seedLeaves("source", "first", "second")
	source := h.insertTitle("both facts", []string{leaves[0]})

	split := write("split an unlinked Row")
	split.ExpectedRevision = 1
	split.MaxAffectedRows = 3
	split.TargetRouteLeafIDs = [][]string{{leaves[1]}, {leaves[2]}}
	h.run(`SPLIT work.notes ROW :row INTO (title) VALUES ('a'), ('b')`, map[string]any{"row": source}, split)

	if repairs := h.repairs(); len(repairs) != 0 {
		t.Fatalf("nothing pointed at the source, so nothing is queued: %+v", repairs)
	}
}

func TestRepeatedEndpointsQueueOnce(t *testing.T) {
	h := newHarness(t)
	leaves := h.seedLeaves("source", "watcher", "first", "second")
	source := h.insertTitle("both facts", []string{leaves[0]})
	watcher := h.insertTitle("watcher", []string{leaves[1]})
	h.setRowLinks(watcher, 1, []sqlstore.LinkRef{{RowID: source}})
	// A duplicated entry in the source's own list must not queue the endpoint
	// twice: the queue is keyed by the endpoint, not by the occurrence.
	duplicated := h.links(source)
	h.setLinks(source, append(duplicated, duplicated...))

	split := write("split a linked Row")
	split.ExpectedRevision = h.revisionOf(source)
	split.MaxAffectedRows = 3
	split.TargetRouteLeafIDs = [][]string{{leaves[2]}, {leaves[3]}}
	h.run(`SPLIT work.notes ROW :row INTO (title) VALUES ('a'), ('b')`, map[string]any{"row": source}, split)

	if repairs := h.repairs(); len(repairs) != 1 {
		t.Fatalf("an endpoint is queued once however often it appears: %+v", repairs)
	}
}

func TestSupersededTargetWithoutAQueuedRepairIsStillBroken(t *testing.T) {
	h := newHarness(t)
	leaves := h.seedLeaves("source", "watcher")
	source := h.insertTitle("source", []string{leaves[0]})
	watcher := h.insertTitle("watcher", []string{leaves[1]})
	h.setRowLinks(watcher, 1, []sqlstore.LinkRef{{RowID: source}})

	// Fabricate the state a crash between "superseded" and "queued" would leave:
	// the target is no longer live and nothing is queued for it.
	h.supersede(source)

	before := h.rawRowField(watcher, "values_json")
	mutation := write("any later write")
	mutation.ExpectedRevision = h.revisionOf(watcher)
	statement := `UPDATE work.notes SET title = 'later' WHERE row_id = :row`
	if code := h.fails(statement, map[string]any{"row": watcher}, mutation); code != result.CodeInternal {
		t.Fatalf("a stale link with nothing queued must be refused: %s", code)
	}
	if after := h.rawRowField(watcher, "values_json"); after != before {
		t.Fatalf("the refused write must leave the Row alone: %s", after)
	}
}
