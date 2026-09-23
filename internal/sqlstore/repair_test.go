package sqlstore_test

import (
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/sqlstore"
)

// The queue is drained by one explicit, bounded statement. Entries are
// re-checked before they are applied: the queue records what was true when it
// was written, and a repair that no longer has anything to do is discarded
// rather than applied blindly. The repair writes both ends of every edge and
// does not queue anything itself, so a repair never schedules its own repair.

func (h *harness) repair(limit int) result.StatementResult {
	h.t.Helper()
	mutation := write("drain the repair queue")
	mutation.MaxAffectedRows = uint64(limit)
	return h.run(`REPAIR LINKS IN DATABASE work LIMIT :limit`, map[string]any{"limit": limit}, mutation)
}

func TestRepairRefreshesAStaleSummary(t *testing.T) {
	h := newHarness(t)
	one, two, _ := h.threeLeaves()
	first := h.insertTitle("first", []string{one})
	second := h.insertTitle("second", []string{two})
	h.setRowLinks(first, 1, []sqlstore.LinkRef{{RowID: second}})

	// An ordinary write to the linked Row leaves the summary pointing at it
	// describing the revision before the write.
	refine := write("refine the linked Row")
	refine.ExpectedRevision = h.revisionOf(second)
	h.run(`UPDATE work.notes SET title = 'second, revised' WHERE row_id = :row`, map[string]any{"row": second}, refine)

	repairs := h.repairs()
	if len(repairs) != 1 || repairs[0].rowID != first || repairs[0].reason != sqlstore.RepairStaleSummary {
		t.Fatalf("an in-place write must queue the summary of its inbound links: %+v", repairs)
	}

	receipt := h.repair(8)
	if text(receipt.Rows[0]["repaired"]) != "1" || text(receipt.Rows[0]["remaining"]) != "0" {
		t.Fatalf("receipt = %v", receipt.Rows[0])
	}
	if repairs := h.repairs(); len(repairs) != 0 {
		t.Fatalf("the drained entry must be gone: %+v", repairs)
	}

	// The refreshed summary describes the counterpart's current revision, on the
	// side that was stale, and the repair left nothing fresh-looking-but-wrong.
	links := h.links(first)
	if len(links) != 1 || links[0].Revision != h.revisionOf(second) {
		t.Fatalf("summary revision = %+v, counterpart is at %d", links, h.revisionOf(second))
	}
	back := h.links(second)
	if len(back) != 1 || back[0].Revision != h.revisionOf(first) {
		t.Fatalf("both sides are written together: %+v", back)
	}
	if report := h.doctor(); report.BrokenLinks != 0 {
		t.Fatalf("a repaired link must be consistent: %+v", report)
	}
}

func TestRepairRepointsLinksAtTheSuccessors(t *testing.T) {
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

	receipt := h.repair(8)
	if text(receipt.Rows[0]["repaired"]) != "1" {
		t.Fatalf("receipt = %v", receipt.Rows[0])
	}
	successors := []string{}
	for _, row := range h.run("SELECT row_id FROM `work`.`notes` LIMIT 5", nil, executor.MutationOptions{}).Rows {
		if text(row["row_id"]) != watcher {
			successors = append(successors, text(row["row_id"]))
		}
	}
	if len(successors) != 2 {
		t.Fatalf("successors = %v", successors)
	}

	// The link now points at both successors, and each of them points back.
	links := h.links(watcher)
	if len(links) != 2 {
		t.Fatalf("a split link must point at every successor: %+v", links)
	}
	seen := map[string]bool{}
	for _, link := range links {
		seen[link.RowID] = true
		if back := h.links(link.RowID); len(back) == 0 {
			t.Fatalf("successor %s must point back", link.RowID)
		}
	}
	for _, successor := range successors {
		if !seen[successor] {
			t.Fatalf("missing successor %s in %+v", successor, links)
		}
	}
	// The superseded Row's own back edge is gone: the repair consumed it.
	if links := h.links(source); len(links) != 0 {
		t.Fatalf("the superseded Row must not keep the repaired edge: %+v", links)
	}
	if repairs := h.repairs(); len(repairs) != 0 {
		t.Fatalf("the repair must not queue a follow-up of its own: %+v", repairs)
	}
	if report := h.doctor(); report.BrokenLinks != 0 {
		t.Fatalf("a repaired link must be consistent: %+v", report)
	}
}

func TestRepairIsBoundedByItsLimit(t *testing.T) {
	h := newHarness(t)
	one, two, three := h.threeLeaves()
	first := h.insertTitle("first", []string{one})
	second := h.insertTitle("second", []string{two})
	third := h.insertTitle("third", []string{three})
	h.setRowLinks(first, 1, []sqlstore.LinkRef{{RowID: second}})
	h.setRowLinks(third, 1, []sqlstore.LinkRef{{RowID: second}})

	// Two queued endpoints, one pass with room for one. Which one goes first is
	// the queue's business, so the test only counts.
	refine := write("refine the hub")
	refine.ExpectedRevision = h.revisionOf(second)
	h.run(`UPDATE work.notes SET title = 'hub, revised' WHERE row_id = :row`, map[string]any{"row": second}, refine)
	if repairs := h.repairs(); len(repairs) != 2 {
		t.Fatalf("two inbound links must queue two endpoints: %+v", repairs)
	}
	receipt := h.repair(1)
	if text(receipt.Rows[0]["repaired"]) != "1" || text(receipt.Rows[0]["remaining"]) != "1" {
		t.Fatalf("a bounded pass must leave the rest queued: %v", receipt.Rows[0])
	}
	receipt = h.repair(8)
	if text(receipt.Rows[0]["repaired"]) != "1" || text(receipt.Rows[0]["remaining"]) != "0" {
		t.Fatalf("the next pass drains the rest: %v", receipt.Rows[0])
	}
}

func TestRepairDiscardsAQueueEntryWhoseHolderIsGone(t *testing.T) {
	h := newHarness(t)
	one, two, _ := h.threeLeaves()
	hub := h.insertTitle("hub", []string{one})
	holder := h.insertTitle("holder", []string{two})
	h.setRowLinks(holder, 1, []sqlstore.LinkRef{{RowID: hub}})

	refine := write("refine the hub")
	refine.ExpectedRevision = h.revisionOf(hub)
	h.run(`UPDATE work.notes SET title = 'hub, revised' WHERE row_id = :row`, map[string]any{"row": hub}, refine)
	if repairs := h.repairs(); len(repairs) != 1 {
		t.Fatalf("the in-place write must queue one endpoint: %+v", repairs)
	}

	// The queued condition no longer holds once the holder itself is deleted.
	h.deleteRow(holder, h.revisionOf(holder))
	receipt := h.repair(8)
	if text(receipt.Rows[0]["discarded"]) != "1" || text(receipt.Rows[0]["repaired"]) != "0" {
		t.Fatalf("a stale queue entry must be discarded, not applied: %v", receipt.Rows[0])
	}
	if repairs := h.repairs(); len(repairs) != 0 {
		t.Fatalf("the queue must drain: %+v", repairs)
	}
}

// A counterpart the repair cannot read is not a counterpart that is gone. The
// only disappearance a repair may act on is the row not being there; a decode
// or I/O failure says nothing about whether the link is still legitimate, so
// the endpoint stays linked, the queue entry stays queued, and the receipt
// names what it could not finish instead of reporting it discarded.
func (h *harness) corruptStoredValues(rowID string) {
	h.t.Helper()
	if _, err := h.db.SQL().Exec(
		`UPDATE "data_`+h.notesTableID()+`" SET values_json = '{not json' WHERE row_id = ?`, rowID); err != nil {
		h.t.Fatal(err)
	}
}

func TestRepairKeepsALinkWhoseCounterpartCannotBeRead(t *testing.T) {
	// A real Instance runs without the commit-time invariant scan, and that is
	// the configuration this behaviour belongs to: with the scan on, the
	// corrupt Row fails the REPAIR statement before it classifies anything.
	h := newHarnessWithOptions(t, sqlstore.Options{})
	one, two, _ := h.threeLeaves()
	first := h.insertTitle("first", []string{one})
	second := h.insertTitle("second", []string{two})
	h.setRowLinks(first, 1, []sqlstore.LinkRef{{RowID: second}})

	refine := write("refine the linked Row")
	refine.ExpectedRevision = h.revisionOf(second)
	h.run(`UPDATE work.notes SET title = 'second, revised' WHERE row_id = :row`, map[string]any{"row": second}, refine)
	if repairs := h.repairs(); len(repairs) != 1 || repairs[0].reason != sqlstore.RepairStaleSummary {
		t.Fatalf("the write must queue one summary repair: %+v", repairs)
	}

	// The counterpart's stored values stop decoding. The Row is still there.
	h.corruptStoredValues(second)

	receipt := h.repair(8)
	if text(receipt.Rows[0]["repaired"]) != "0" || text(receipt.Rows[0]["discarded"]) != "0" {
		t.Fatalf("an unreadable counterpart is neither repaired nor discarded: %v", receipt.Rows[0])
	}
	if text(receipt.Rows[0]["failed"]) != "1" || text(receipt.Rows[0]["remaining"]) != "1" {
		t.Fatalf("the entry is still queued and the receipt says so: %v", receipt.Rows[0])
	}
	if len(receipt.Warnings) != 1 || receipt.Warnings[0].Code != result.CodeInternal ||
		!strings.Contains(receipt.Warnings[0].Message, second) {
		t.Fatalf("the receipt must name the endpoint it could not read: %+v", receipt.Warnings)
	}
	if links := h.links(first); len(links) != 1 || links[0].RowID != second {
		t.Fatalf("the link must survive a counterpart it cannot read: %+v", links)
	}
	if repairs := h.repairs(); len(repairs) != 1 {
		t.Fatalf("an entry that was not applied stays queued: %+v", repairs)
	}
}

// The same rule down the successor chain: a successor that cannot be read is
// not a chain that ended. Repointing on the tail it managed to read would drop
// the endpoint it could not see, so the pass stops and keeps the entry.
func TestRepairKeepsALinkWhoseSuccessorCannotBeRead(t *testing.T) {
	h := newHarnessWithOptions(t, sqlstore.Options{})
	leaves := h.seedLeaves("source", "watcher", "first", "second")
	source := h.insertTitle("both facts", []string{leaves[0]})
	watcher := h.insertTitle("watcher", []string{leaves[1]})
	h.setRowLinks(watcher, 1, []sqlstore.LinkRef{{RowID: source}})

	split := write("split a linked Row")
	split.ExpectedRevision = h.revisionOf(source)
	split.MaxAffectedRows = 3
	split.TargetRouteLeafIDs = [][]string{{leaves[2]}, {leaves[3]}}
	parts := h.run(`SPLIT work.notes ROW :row INTO (title) VALUES ('first fact'), ('second fact')`,
		map[string]any{"row": source}, split)
	h.corruptStoredValues(text(parts.Rows[0]["row_id"]))

	receipt := h.repair(8)
	if text(receipt.Rows[0]["failed"]) != "1" || text(receipt.Rows[0]["remaining"]) != "1" ||
		text(receipt.Rows[0]["repaired"]) != "0" || text(receipt.Rows[0]["discarded"]) != "0" {
		t.Fatalf("a successor that cannot be read leaves the entry queued: %v", receipt.Rows[0])
	}
	if links := h.links(watcher); len(links) != 1 || links[0].RowID != source {
		t.Fatalf("the endpoint must still point at the Row it was queued for: %+v", links)
	}
	if repairs := h.repairs(); len(repairs) != 1 {
		t.Fatalf("an entry that was not applied stays queued: %+v", repairs)
	}
}
