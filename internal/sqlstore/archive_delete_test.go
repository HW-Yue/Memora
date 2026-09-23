package sqlstore_test

import (
	"encoding/json"
	"testing"

	"github.com/HW-Yue/Memora/internal/change"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/sqlstore"
)

// A delete archives before it removes, and the archive is the only copy left
// afterwards: the Row, its leaf, its history and both ends of every link are
// gone. Rebuilding from the archive is the Agent's job, so what lands there has
// to be enough to do it.

// maxCascadeForTest mirrors the storage layer's link-cascade bound.
const maxCascadeForTest = 1000

func (h *harness) rawRowCount() int {
	h.t.Helper()
	var count int
	query := `SELECT COUNT(*) FROM "data_` + h.notesTableID() + `"`
	if err := h.db.SQL().QueryRow(query).Scan(&count); err != nil {
		h.t.Fatal(err)
	}
	return count
}

func (h *harness) archiveCount(rowID string) int {
	h.t.Helper()
	var count int
	if err := h.db.SQL().QueryRow(`SELECT COUNT(*) FROM mem_archive WHERE row_id = ?`, rowID).Scan(&count); err != nil {
		h.t.Fatal(err)
	}
	return count
}

func (h *harness) archived(rowID string) ([]sqlstore.ArchiveSegment, sqlstore.ArchivedRow) {
	h.t.Helper()
	var pathJSON, rowJSON string
	if err := h.db.SQL().QueryRow(`SELECT path_json, row_json FROM mem_archive WHERE row_id = ?`, rowID).Scan(&pathJSON, &rowJSON); err != nil {
		h.t.Fatal(err)
	}
	path := []sqlstore.ArchiveSegment{}
	if err := json.Unmarshal([]byte(pathJSON), &path); err != nil {
		h.t.Fatal(err)
	}
	content := sqlstore.ArchivedRow{}
	if err := json.Unmarshal([]byte(rowJSON), &content); err != nil {
		h.t.Fatal(err)
	}
	return path, content
}

// supersede marks a Row the way a SPLIT does, without driving a whole plan.
func (h *harness) supersede(rowID string) {
	h.t.Helper()
	update := `UPDATE "data_` + h.notesTableID() + `" SET row_state = 'superseded' WHERE row_id = ?`
	if _, err := h.db.SQL().Exec(update, rowID); err != nil {
		h.t.Fatal(err)
	}
}

// setLinks writes a Row's links straight into the data table: nothing can write
// them through MSQL yet, so a fixture is the only way to exercise the detach.
func (h *harness) setLinks(rowID string, links []sqlstore.Link) {
	h.t.Helper()
	encoded, err := json.Marshal(links)
	if err != nil {
		h.t.Fatal(err)
	}
	update := `UPDATE "data_` + h.notesTableID() + `" SET links = ? WHERE row_id = ?`
	if _, err := h.db.SQL().Exec(update, string(encoded), rowID); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) links(rowID string) []sqlstore.Link {
	h.t.Helper()
	var encoded string
	query := `SELECT links FROM "data_` + h.notesTableID() + `" WHERE row_id = ?`
	if err := h.db.SQL().QueryRow(query, rowID).Scan(&encoded); err != nil {
		h.t.Fatal(err)
	}
	links := []sqlstore.Link{}
	if err := json.Unmarshal([]byte(encoded), &links); err != nil {
		h.t.Fatal(err)
	}
	return links
}

func (h *harness) deleteRow(rowID string, revision uint64) result.StatementResult {
	h.t.Helper()
	mutation := write("archive and remove")
	mutation.ExpectedRevision = revision
	return h.run(`DELETE FROM work.notes WHERE row_id = :row`, map[string]any{"row": rowID}, mutation)
}

func TestDeleteArchivesThePathAndContent(t *testing.T) {
	h := newHarness(t)
	h.seedTree()
	rowID := h.insertAlongPath("Use SQLite", []router.PathSegment{
		segment("architecture", router.KindBranch, "架构决策"),
		segment("sqlite", router.KindLeaf, "为什么选 SQLite"),
	})

	h.deleteRow(rowID, 1)

	// The root is part of the path: rebuilding needs its purpose as much as the
	// segments below it.
	path, content := h.archived(rowID)
	if len(path) != 3 || path[1].Name != "architecture" || path[2].Name != "sqlite" {
		t.Fatalf("archived path = %+v", path)
	}
	if path[1].Kind != router.KindBranch || path[2].Kind != router.KindLeaf {
		t.Fatalf("archived kinds = %+v", path)
	}
	if path[2].Purpose != "为什么选 SQLite" || path[0].Purpose != "Everything" {
		t.Fatalf("archived segments must carry purpose and stable ids: %+v", path)
	}
	if path[0].RouteID == "" || path[2].RouteID == "" {
		t.Fatalf("archived segments must carry stable ids: %+v", path)
	}
	if content.RowID != rowID || content.Revision != 1 || content.RowState != "live" {
		t.Fatalf("archived content = %+v", content)
	}
	if len(content.RouteLeafIDs) != 1 || content.RouteLeafIDs[0] != path[2].RouteID {
		t.Fatalf("archived mount = %v", content.RouteLeafIDs)
	}
	values := map[string]any{}
	for key, value := range content.Values {
		values[key] = value
	}
	if len(values) != 1 {
		t.Fatalf("archived values = %v", values)
	}
	for _, value := range values {
		if value != "Use SQLite" {
			t.Fatalf("archived value = %v", value)
		}
	}
}

func (h *harness) historyCount(rowID string) int {
	h.t.Helper()
	var count int
	query := `SELECT COUNT(*) FROM "history_` + h.notesTableID() + `" WHERE row_id = ?`
	if err := h.db.SQL().QueryRow(query, rowID).Scan(&count); err != nil {
		h.t.Fatal(err)
	}
	return count
}

func TestDeleteRemovesHistoryAndEveryReadFace(t *testing.T) {
	h := newHarness(t)
	_, leaf := h.seedNotes()
	rowID := h.insertTitle("doomed", []string{leaf})

	refine := write("refine once so history has two records")
	refine.ExpectedRevision = 1
	h.run(`UPDATE work.notes SET title = 'refined' WHERE row_id = :row`, map[string]any{"row": rowID}, refine)
	if h.historyCount(rowID) != 2 {
		t.Fatalf("history before the delete = %d", h.historyCount(rowID))
	}

	h.deleteRow(rowID, 2)

	if h.historyCount(rowID) != 0 {
		t.Fatalf("the deleted Row's history must go with it: %d records", h.historyCount(rowID))
	}
	asOf := `SELECT * FROM work.notes AS OF REVISION 1 WHERE row_id = :row LIMIT 1`
	if code := h.fails(asOf, map[string]any{"row": rowID}, executor.MutationOptions{}); code != result.CodeNotFound {
		t.Fatalf("AS OF must not reach a deleted Row: %s", code)
	}
	if code := h.fails(`OPEN ROUTE :leaf LIMIT 1`, map[string]any{"leaf": leaf}, executor.MutationOptions{}); code != result.CodeNotFound {
		t.Fatalf("the leaf is gone, so opening it cannot succeed: %s", code)
	}
}

func TestDeleteDetachesBothEndsOfALink(t *testing.T) {
	h := newHarness(t)
	root, first := h.seedNotes()
	second := text(h.run(`CREATE ROUTE UNDER :p NAME 'other' KIND 'leaf' PURPOSE 'the second position a link can point at'`,
		map[string]any{"p": root}, write("other")).Rows[0]["route_id"])
	doomed := h.insertTitle("doomed", []string{first})
	keeper := h.insertTitle("keeper", []string{second})

	h.setLinks(doomed, []sqlstore.Link{{RowID: keeper, Summary: "keeper", Revision: 1}})
	h.setLinks(keeper, []sqlstore.Link{{RowID: doomed, Summary: "doomed", Revision: 1}})

	h.deleteRow(doomed, 1)

	if links := h.links(keeper); len(links) != 0 {
		t.Fatalf("the other end of the link must be detached: %v", links)
	}
	selected := h.run("SELECT revision FROM `work`.`notes` WHERE row_id = :row LIMIT 1", map[string]any{"row": keeper}, executor.MutationOptions{})
	if revision := text(selected.Rows[0]["revision"]); revision != "2" {
		t.Fatalf("detaching is an in-place modification, so the revision advances: %s", revision)
	}
	if h.historyCount(keeper) != 2 {
		t.Fatalf("the detach must be recorded in history: %d records", h.historyCount(keeper))
	}
	// The change entry names the Rows this delete modified, which is where the
	// cascade count is reported until the result envelope carries it.
	var body string
	if err := h.db.SQL().QueryRow(`SELECT body FROM mem_changes ORDER BY sequence DESC LIMIT 1`).Scan(&body); err != nil {
		t.Fatal(err)
	}
	envelope := change.Envelope{}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatal(err)
	}
	named := false
	for _, entry := range envelope.Entries {
		for _, related := range entry.RelatedObjectIDs {
			if related == keeper {
				named = true
			}
		}
	}
	if !named {
		t.Fatalf("the audit trail must name the Row whose link changed: %+v", envelope.Entries)
	}
	// The archive keeps the pre-delete relations, or a rebuild cannot reconnect.
	_, content := h.archived(doomed)
	if len(content.Links) != 1 || content.Links[0].RowID != keeper {
		t.Fatalf("archived links = %+v", content.Links)
	}
}

func TestDeleteRefusesAnUnboundedLinkCascade(t *testing.T) {
	h := newHarness(t)
	_, leaf := h.seedNotes()
	rowID := h.insertTitle("widely linked", []string{leaf})

	links := make([]sqlstore.Link, maxCascadeForTest+1)
	for index := range links {
		links[index] = sqlstore.Link{RowID: "row_missing", Summary: "s", Revision: 1}
	}
	h.setLinks(rowID, links)

	mutation := write("over the bound")
	mutation.ExpectedRevision = 1
	source := `DELETE FROM work.notes WHERE row_id = :row`
	if code := h.fails(source, map[string]any{"row": rowID}, mutation); code != result.CodeConstraint {
		t.Fatalf("code = %s", code)
	}
	if h.rawRowCount() != 1 || h.archiveCount(rowID) != 0 {
		t.Fatalf("a refused delete must change nothing: %d rows, %d archive records", h.rawRowCount(), h.archiveCount(rowID))
	}
}

func TestDeletingTheLastRowLeavesNoTree(t *testing.T) {
	h := newHarness(t)
	h.seedTree()
	rowID := h.insertAlongPath("only fact", []router.PathSegment{
		segment("architecture", router.KindLeaf, "架构决策"),
	})

	h.deleteRow(rowID, 1)

	if children := h.rootChildren(); len(children) != 0 {
		t.Fatalf("the emptied tree must not survive as shells: %v", children)
	}
	var root string
	if err := h.db.SQL().QueryRow(`SELECT COALESCE(router_root_id, '') FROM mem_tables WHERE id = ?`, h.notesTableID()).Scan(&root); err != nil {
		t.Fatal(err)
	}
	if root != "" {
		t.Fatalf("the root is gone, so the Table has no router: %q", root)
	}
}
