package sqlstore_test

import (
	"encoding/json"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/sqlstore"
)

// A link is stored at both ends and written by one snapshot option. The option
// is the Row's complete membership: nil leaves it alone, an empty list clears
// it. The other Row's own links are edited in place, so this write never wipes
// what that Row holds to anyone else.

func (h *harness) insertLinked(title string, leaf string, links []sqlstore.LinkRef) string {
	h.t.Helper()
	mutation := write("insert with links")
	mutation.RouteLeafIDs = []string{leaf}
	mutation.Links = links
	return text(h.run(`INSERT INTO work.notes (title) VALUES (:title)`, map[string]any{"title": title}, mutation).Rows[0]["row_id"])
}

func (h *harness) setRowLinks(rowID string, revision uint64, links []sqlstore.LinkRef) {
	h.t.Helper()
	mutation := write("relink")
	mutation.ExpectedRevision = revision
	mutation.Links = links
	h.run(`UPDATE work.notes SET title = title WHERE row_id = :row`, map[string]any{"row": rowID}, mutation)
}

func (h *harness) threeLeaves() (string, string, string) {
	h.t.Helper()
	root := h.seedTree()
	ids := []string{}
	for _, name := range []string{"one", "two", "three"} {
		ids = append(ids, text(h.run(`CREATE ROUTE UNDER :p NAME :name KIND 'leaf' PURPOSE :purpose`,
			map[string]any{"p": root, "name": name, "purpose": name + " purpose"}, write(name)).Rows[0]["route_id"]))
	}
	return ids[0], ids[1], ids[2]
}

func TestLinksAreWrittenAtBothEnds(t *testing.T) {
	h := newHarness(t)
	one, two, three := h.threeLeaves()
	first := h.insertTitle("first", []string{one})
	second := h.insertTitle("second", []string{two})

	h.setRowLinks(first, 1, []sqlstore.LinkRef{{RowID: second}})

	fromFirst := h.links(first)
	if len(fromFirst) != 1 || fromFirst[0].RowID != second {
		t.Fatalf("first links = %+v", fromFirst)
	}
	// The revision recorded is the counterpart's *final* one: writing the second
	// end is an in-place modification, so recording the pre-write revision would
	// make the summary read as stale the moment it was written.
	if fromFirst[0].Summary == "" || fromFirst[0].Revision != 2 {
		t.Fatalf("a link entry carries the summary and the revision it came from: %+v", fromFirst[0])
	}
	if fromFirst[0].TableID == "" {
		t.Fatal("a link entry has to persist where the other Row lives: uniqueness is not addressability")
	}
	fromSecond := h.links(second)
	if len(fromSecond) != 1 || fromSecond[0].RowID != first {
		t.Fatalf("second links = %+v", fromSecond)
	}
	// Adding the other end is an in-place modification of that Row.
	if revision := h.rawRowField(second, "revision"); revision != "2" {
		t.Fatalf("counterpart revision = %s", revision)
	}
	if h.historyCount(second) != 2 {
		t.Fatalf("counterpart history = %d", h.historyCount(second))
	}

	// A second link on the same Row leaves the first one alone.
	th := h.insertTitle("third", []string{three})
	h.setRowLinks(first, 2, []sqlstore.LinkRef{{RowID: second}, {RowID: th}})
	if links := h.links(first); len(links) != 2 {
		t.Fatalf("first links = %+v", links)
	}
	if links := h.links(second); len(links) != 1 {
		t.Fatalf("the untouched end must keep its link: %+v", links)
	}

	// Removing one end removes both, and the other end keeps its own links.
	h.setRowLinks(first, 3, []sqlstore.LinkRef{{RowID: th}})
	if links := h.links(first); len(links) != 1 || links[0].RowID != th {
		t.Fatalf("first links after removal = %+v", links)
	}
	if links := h.links(second); len(links) != 0 {
		t.Fatalf("the removed end must be detached: %+v", links)
	}
}

func TestLinkOptionDistinguishesAbsentFromEmpty(t *testing.T) {
	h := newHarness(t)
	one, two, _ := h.threeLeaves()
	first := h.insertTitle("first", []string{one})
	second := h.insertTitle("second", []string{two})
	h.setRowLinks(first, 1, []sqlstore.LinkRef{{RowID: second}})

	// Absent: the membership stays.
	keep := write("refine only")
	keep.ExpectedRevision = 2
	h.run(`UPDATE work.notes SET title = 'refined' WHERE row_id = :row`, map[string]any{"row": first}, keep)
	if links := h.links(first); len(links) != 1 {
		t.Fatalf("an UPDATE without links must keep them: %+v", links)
	}

	// Empty: the membership is cleared, both ends.
	h.setRowLinks(first, 3, []sqlstore.LinkRef{})
	if links := h.links(first); len(links) != 0 {
		t.Fatalf("an explicit empty list must clear the links: %+v", links)
	}
	if links := h.links(second); len(links) != 0 {
		t.Fatalf("clearing must detach the other end: %+v", links)
	}
}

func TestLinksRefuseTargetsTheyCannotAddress(t *testing.T) {
	h := newHarness(t)
	one, two, _ := h.threeLeaves()
	first := h.insertTitle("first", []string{one})
	second := h.insertTitle("second", []string{two})

	cases := []struct {
		name  string
		links []sqlstore.LinkRef
		code  result.Code
	}{
		{name: "self", links: []sqlstore.LinkRef{{RowID: first}}, code: result.CodeValidation},
		{name: "unknown id", links: []sqlstore.LinkRef{{RowID: "row_missing"}}, code: result.CodeNotFound},
		{name: "unknown table", links: []sqlstore.LinkRef{{RowID: second, Table: "elsewhere"}}, code: result.CodeNotFound},
		{name: "empty id", links: []sqlstore.LinkRef{{RowID: "  "}}, code: result.CodeValidation},
	}
	for _, tc := range cases {
		mutation := write("reject")
		mutation.ExpectedRevision = 1
		mutation.Links = tc.links
		source := `UPDATE work.notes SET title = 'rejected' WHERE row_id = :row`
		if code := h.fails(source, map[string]any{"row": first}, mutation); code != tc.code {
			t.Fatalf("%s: code = %s, want %s", tc.name, code, tc.code)
		}
	}
	if links := h.links(first); len(links) != 0 {
		t.Fatalf("a refused write must not link anything: %+v", links)
	}
	if links := h.links(second); len(links) != 0 {
		t.Fatalf("a refused write must not touch the counterpart: %+v", links)
	}
}

// A deleted Row takes both ends of its links with it, which the archive feature
// already does; this exercises it through the write surface instead of a fixture.
func TestDeleteDetachesLinksWrittenThroughTheSurface(t *testing.T) {
	h := newHarness(t)
	one, two, _ := h.threeLeaves()
	first := h.insertTitle("first", []string{one})
	second := h.insertTitle("second", []string{two})
	h.setRowLinks(first, 1, []sqlstore.LinkRef{{RowID: second}})

	h.deleteRow(first, 2)

	if links := h.links(second); len(links) != 0 {
		t.Fatalf("the surviving Row must not keep a dangling link: %+v", links)
	}
	_, content := h.archived(first)
	if len(content.Links) != 1 || content.Links[0].RowID != second {
		t.Fatalf("the archive keeps the relation as it was: %+v", content.Links)
	}
}

// linksFromValue reads the links column back, whatever shape the envelope left
// it in.
func linksFromValue(t *testing.T, value any) []sqlstore.Link {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	links := []sqlstore.Link{}
	if err := json.Unmarshal(encoded, &links); err != nil {
		t.Fatalf("links = %v (%v)", value, err)
	}
	return links
}

func TestLinksReadBackFromSelect(t *testing.T) {
	h := newHarness(t)
	one, two, _ := h.threeLeaves()
	first := h.insertTitle("first", []string{one})
	second := h.insertTitle("second", []string{two})
	h.setRowLinks(first, 1, []sqlstore.LinkRef{{RowID: second}})

	selected := h.run(`SELECT title, row_id, revision FROM work.notes WHERE row_id = :row LIMIT 1`,
		map[string]any{"row": first}, executor.MutationOptions{})
	encoded, present := selected.Rows[0]["links"]
	if !present {
		t.Fatalf("a Row's links must be readable: %v", selected.Rows[0])
	}
	links := linksFromValue(t, encoded)
	if len(links) != 1 || links[0].RowID != second {
		t.Fatalf("links = %+v", links)
	}
}
