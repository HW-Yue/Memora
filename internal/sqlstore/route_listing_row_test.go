package sqlstore_test

import (
	"strconv"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/router"
)

// A leaf's listing carries the Row it holds.
//
// "One live Row hangs under exactly one leaf" is enforced by the write path
// (internal/sqlstore/invariant.go), so leaf → Row is one-to-one and the layer
// listing can say it. Before this, every walk had to follow `SHOW ROUTES` with
// one `OPEN ROUTE :leaf LIMIT 1` per leaf just to learn the row id — the widest
// measured walk spent 35 of its 50 statements that way.
//
// The two revisions are different objects and the columns say so: `revision` is
// the Route node's own version (it changes when the node is renamed, re-purposed
// or re-mounted), `row_revision` is the version of the Row hanging under it (it
// changes when the fact is edited). A caller that passes one where the other is
// expected gets a revision conflict on a Row nobody touched.

func TestLayerListingCarriesTheRowEachLeafHolds(t *testing.T) {
	h := newHarness(t)
	h.seedTree()
	rowID := h.insertAlongPath("Use SQLite", []router.PathSegment{
		segment("architecture", router.KindBranch, "架构决策"),
		segment("sqlite", router.KindLeaf, "为什么存储层是 SQLite"),
	})

	branches := h.rootChildren()
	if len(branches) != 1 {
		t.Fatalf("root children = %v", branches)
	}
	// A branch holds no Row, so both columns are null rather than an empty
	// string a caller could mistake for an id.
	if branches[0]["row_id"] != nil || branches[0]["row_revision"] != nil {
		t.Fatalf("a branch carries no Row: %v", branches[0])
	}

	listing := h.run(`SHOW ROUTES UNDER :p`,
		map[string]any{"p": text(branches[0]["route_id"])}, executor.MutationOptions{})
	columns := map[string]bool{}
	for _, column := range listing.Columns {
		columns[column.Name] = column.Nullable
	}
	for _, name := range []string{"row_id", "row_revision"} {
		nullable, present := columns[name]
		if !present {
			t.Fatalf("the listing must declare %s: %v", name, listing.Columns)
		}
		if !nullable {
			t.Fatalf("%s is empty on every non-leaf, so it must be nullable", name)
		}
	}
	if len(listing.Rows) != 1 {
		t.Fatalf("leaf layer = %v", listing.Rows)
	}
	leaf := listing.Rows[0]
	if text(leaf["row_id"]) != rowID {
		t.Fatalf("row_id = %v, want %s", leaf["row_id"], rowID)
	}

	// The same answer `OPEN ROUTE` gives, from the listing that was already read:
	// this is one source of truth reported twice, not a second resolution.
	opened := h.run(`OPEN ROUTE :leaf LIMIT 1`,
		map[string]any{"leaf": text(leaf["route_id"])}, executor.MutationOptions{}).Rows
	if len(opened) != 1 {
		t.Fatalf("open route = %v", opened)
	}
	if text(leaf["row_id"]) != text(opened[0]["row_id"]) ||
		text(leaf["row_revision"]) != text(opened[0]["revision"]) {
		t.Fatalf("the listing and OPEN ROUTE disagree: %v vs %v", leaf, opened[0])
	}
}

// The Row's revision is not the node's revision. They are separate counters on
// separate objects, and a listing that reported the node's would hand every
// caller a stale handle for the optimistic-concurrency check on the Row.
func TestTheLeafReportsTheRowsRevisionNotItsOwn(t *testing.T) {
	h := newHarness(t)
	h.seedTree()
	rowID := h.insertAlongPath("Use SQLite", []router.PathSegment{
		segment("sqlite", router.KindLeaf, "为什么存储层是 SQLite"),
	})

	leaf := h.rootChildren()[0]
	routeID := text(leaf["route_id"])
	if text(leaf["row_revision"]) != "1" {
		t.Fatalf("a freshly inserted Row is at revision 1: %v", leaf)
	}

	// Move the node's own revision without touching the Row.
	amend := write("a longer description, which is the node's business")
	revision, err := strconv.ParseUint(text(leaf["revision"]), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	amend.ExpectedRevision = revision
	h.run(`ALTER ROUTE :route SET SYNOPSIS :synopsis`,
		map[string]any{"route": routeID, "synopsis": "为什么存储层是 SQLite，长一点的说法"}, amend)
	// Move the Row's revision without touching the node.
	edit := write("edit the fact, which is the Row's business")
	edit.ExpectedRevision = 1
	h.run(`UPDATE work.notes SET title = :title WHERE row_id = :row`,
		map[string]any{"title": "Still SQLite", "row": rowID}, edit)

	before := text(leaf["revision"])
	leaf = h.rootChildren()[0]
	nodeRevision, rowRevision := text(leaf["revision"]), text(leaf["row_revision"])
	if nodeRevision == before {
		t.Fatalf("the node's own revision must have moved: %v", leaf)
	}
	if nodeRevision == rowRevision {
		t.Fatalf("the two revisions must move independently: %v", leaf)
	}
	if rowRevision != "2" {
		t.Fatalf("row_revision = %s, want the edited Row's 2", rowRevision)
	}
	opened := h.run(`OPEN ROUTE :leaf LIMIT 1`,
		map[string]any{"leaf": routeID}, executor.MutationOptions{}).Rows
	if text(opened[0]["revision"]) != rowRevision {
		t.Fatalf("row_revision = %s, OPEN ROUTE says %v", rowRevision, opened[0]["revision"])
	}
}

// A leaf with nothing mounted reports no Row — the same rule `OPEN ROUTE`
// applies, so the listing never hands back an id that reads back as not_found.
// A leaf can exist empty: it is created before the Row that will hang under it,
// and `OPEN ROUTE` answers with zero locators rather than an error.
func TestAnEmptyLeafCarriesNoRow(t *testing.T) {
	h := newHarness(t)
	root := h.seedTree()
	leaf := h.run(`CREATE ROUTE UNDER :parent NAME :name KIND :kind PURPOSE :purpose`,
		map[string]any{"parent": root, "name": "sqlite", "kind": "leaf",
			"purpose": "为什么存储层是 SQLite"}, write("a leaf before its Row"))
	routeID := text(leaf.Rows[0]["route_id"])

	opened := h.run(`OPEN ROUTE :leaf LIMIT 1`, map[string]any{"leaf": routeID}, executor.MutationOptions{}).Rows
	if len(opened) != 0 {
		t.Fatalf("an empty leaf locates nothing: %v", opened)
	}
	listed := h.rootChildren()
	if len(listed) != 1 || text(listed[0]["route_id"]) != routeID {
		t.Fatalf("the empty leaf is still a node: %v", listed)
	}
	if listed[0]["row_id"] != nil || listed[0]["row_revision"] != nil {
		t.Fatalf("an empty leaf carries no Row: %v", listed[0])
	}
}

// Damaged data does not become an answer. A leaf whose body names a Row that is
// not live is exactly what doctor's `mismatched_mounts` counts, and `OPEN ROUTE`
// answers it with zero locators rather than a locator nobody can read. The
// listing mirrors that decision instead of taking the body's word for it —
// otherwise the cheap path would be the one that lies.
func TestALeafPointingAtARowThatIsNotLiveReportsNoRow(t *testing.T) {
	h := newHarness(t)
	h.seedTree()
	h.insertAlongPath("Use SQLite", []router.PathSegment{
		segment("sqlite", router.KindLeaf, "为什么存储层是 SQLite"),
	})
	routeID := text(h.rootChildren()[0]["route_id"])
	h.setLeafHolder(routeID, "row_that_was_never_written")

	opened := h.run(`OPEN ROUTE :leaf LIMIT 1`, map[string]any{"leaf": routeID}, executor.MutationOptions{}).Rows
	if len(opened) != 0 {
		t.Fatalf("a dangling mount locates nothing: %v", opened)
	}
	listed := h.rootChildren()[0]
	if listed["row_id"] != nil || listed["row_revision"] != nil {
		t.Fatalf("a dangling mount is not a position: %v", listed)
	}
}
