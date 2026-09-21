package sqlstore_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/security"
)

// pathOf builds an implicit route path: every name before the last is a branch,
// the last one is the leaf.
func pathOf(names ...string) []router.PathSegment {
	segments := make([]router.PathSegment, 0, len(names))
	for index, name := range names {
		kind := router.KindBranch
		if index == len(names)-1 {
			kind = router.KindLeaf
		}
		segments = append(segments, router.PathSegment{Name: name, Kind: kind, Purpose: name + " purpose"})
	}
	return segments
}

func (h *harness) failsAuthorized(auth security.Authorization, source string, named map[string]any, mutation executor.MutationOptions) result.Code {
	h.t.Helper()
	h.seq++
	envelope := h.session.ExecuteBatch(context.Background(), executor.BatchRequest{
		RequestID: "z" + strings.Repeat("q", h.seq), Source: source,
		Statements: []executor.StatementInput{{
			Parameters: executor.Parameters{Named: named}, Mutation: mutation, Authorization: auth,
		}},
	})
	if envelope.Error != nil {
		return envelope.Error.Code
	}
	if envelope.Results[0].Error == nil {
		h.t.Fatalf("%s unexpectedly succeeded", source)
	}
	return envelope.Results[0].Error.Code
}

// Two statements reach the archive and nothing else does: SHOW ARCHIVE lists
// metadata under a required scope and a bounded page, OPEN ARCHIVE hands over
// one record in full. It is the one read face query-model §7 exempts, so the
// tests here are as much about what it refuses as what it returns.

func (h *harness) showArchive(source string, named map[string]any) result.StatementResult {
	h.t.Helper()
	return h.run(source, named, executor.MutationOptions{})
}

func TestShowArchiveListsMetadataUnderABoundedScope(t *testing.T) {
	h := newHarness(t)
	h.seedTree()
	first := h.insertAlongPath("first", pathOf("architecture", "sqlite"))
	second := h.insertAlongPath("second", pathOf("architecture", "wal"))
	h.deleteRow(first, 1)
	h.deleteRow(second, 1)

	page := h.showArchive(`SHOW ARCHIVE FROM work.notes LIMIT 1`, nil)
	if len(page.Rows) != 1 {
		t.Fatalf("rows = %v", page.Rows)
	}
	if page.Page == nil || page.Page.NextCursor == "" || !page.Page.Truncated {
		t.Fatalf("a bounded listing must report its next cursor: %+v", page.Page)
	}
	// Metadata only: the record itself has to be opened.
	for _, forbidden := range []string{"row", "values", "values_json", "links"} {
		if _, present := page.Rows[0][forbidden]; present {
			t.Fatalf("SHOW ARCHIVE must not carry %q: %v", forbidden, page.Rows[0])
		}
	}
	if path := text(page.Rows[0]["path"]); path != "/root/architecture/sqlite" && path != "/root/architecture/wal" {
		t.Fatalf("path = %q", path)
	}

	next := h.showArchive(`SHOW ARCHIVE FROM work.notes CURSOR :cursor LIMIT 1`,
		map[string]any{"cursor": page.Page.NextCursor})
	if len(next.Rows) != 1 || text(next.Rows[0]["archive_id"]) == text(page.Rows[0]["archive_id"]) {
		t.Fatalf("the second page = %v", next.Rows)
	}
	if next.Page.Truncated {
		t.Fatal("two records with a limit of one each must not truncate the second page")
	}

	filtered := h.showArchive(`SHOW ARCHIVE FROM work.notes FOR ROW :row LIMIT 5`, map[string]any{"row": first})
	if len(filtered.Rows) != 1 || text(filtered.Rows[0]["row_id"]) != first {
		t.Fatalf("filtered = %v", filtered.Rows)
	}
}

func TestShowArchiveNeedsAScopeAndRefusesAForeignCursor(t *testing.T) {
	h := newHarness(t)
	h.seedTree()
	first := h.insertAlongPath("first", pathOf("architecture", "sqlite"))
	second := h.insertAlongPath("second", pathOf("architecture", "wal"))
	h.deleteRow(first, 1)
	h.deleteRow(second, 1)

	// The Table scope and the limit are both required.
	for _, source := range []string{
		`SHOW ARCHIVE FROM work.notes`,
		`SHOW ARCHIVE LIMIT 5`,
	} {
		if code := h.fails(source, nil, executor.MutationOptions{}); code == "" {
			t.Fatalf("%s must be refused", source)
		}
	}

	// A cursor is bound to the scope it was issued for: one listing everything
	// cannot be replayed against a single Row, or the other way round.
	wide := h.showArchive(`SHOW ARCHIVE FROM work.notes LIMIT 1`, nil)
	if wide.Page == nil || wide.Page.NextCursor == "" {
		t.Fatalf("two records with a limit of one must truncate: %+v", wide.Page)
	}
	narrow := `SHOW ARCHIVE FROM work.notes FOR ROW :row CURSOR :cursor LIMIT 1`
	code := h.fails(narrow, map[string]any{"row": first, "cursor": wide.Page.NextCursor}, executor.MutationOptions{})
	if code != result.CodeValidation {
		t.Fatalf("cross-scope cursor: %s", code)
	}

	// A tampered cursor is refused too, checksum and all.
	tampered := wide.Page.NextCursor[:len(wide.Page.NextCursor)-2] + "zz"
	code = h.fails(`SHOW ARCHIVE FROM work.notes CURSOR :cursor LIMIT 1`,
		map[string]any{"cursor": tampered}, executor.MutationOptions{})
	if code != result.CodeValidation {
		t.Fatalf("tampered cursor: %s", code)
	}
}

func TestOpenArchiveReturnsTheWholeRecord(t *testing.T) {
	h := newHarness(t)
	h.seedTree()
	rowID := h.insertAlongPath("Use SQLite", pathOf("architecture", "sqlite"))
	h.deleteRow(rowID, 1)
	listed := h.showArchive(`SHOW ARCHIVE FROM work.notes LIMIT 5`, nil)
	archiveID := text(listed.Rows[0]["archive_id"])

	opened := h.run(`OPEN ARCHIVE :id`, map[string]any{"id": archiveID}, executor.MutationOptions{})
	if len(opened.Rows) != 1 {
		t.Fatalf("opened = %v", opened.Rows)
	}
	row := opened.Rows[0]
	if text(row["row_id"]) != rowID {
		t.Fatalf("row_id = %v", row["row_id"])
	}
	path := []map[string]any{}
	if err := json.Unmarshal([]byte(text(row["path"])), &path); err != nil {
		t.Fatalf("path is not JSON: %v (%v)", row["path"], err)
	}
	if len(path) != 3 || path[1]["name"] != "architecture" || path[2]["purpose"] != "sqlite purpose" {
		t.Fatalf("path = %v", path)
	}
	content := map[string]any{}
	if err := json.Unmarshal([]byte(text(row["row"])), &content); err != nil {
		t.Fatalf("row is not JSON: %v", err)
	}
	if text(content["row_id"]) != rowID || text(content["revision"]) != "1" {
		t.Fatalf("content = %v", content)
	}
	if _, present := content["values"]; !present {
		t.Fatalf("the record must carry the values: %v", content)
	}

	if code := h.fails(`OPEN ARCHIVE :id`, map[string]any{"id": "archive_missing"}, executor.MutationOptions{}); code != result.CodeNotFound {
		t.Fatalf("unknown archive: %s", code)
	}
}

func TestArchiveIsNotAQueryTarget(t *testing.T) {
	h := newHarness(t)
	h.seedTree()

	// The archive is an internal table: it is not in the catalog, and SELECT
	// cannot reach it, so SHOW ARCHIVE and OPEN ARCHIVE stay the only doors.
	tables := h.run(`SHOW TABLES FROM work LIMIT 32 COMPACT`, nil, executor.MutationOptions{})
	for _, row := range tables.Rows {
		if text(row["name"]) == "mem_archive" {
			t.Fatal("the archive must not appear as a Table")
		}
	}
	// Not a Table, so the Catalog cannot resolve it: any refusal is the point,
	// and h.fails fails the test outright if the statement succeeds.
	h.fails(`SELECT * FROM mem_archive LIMIT 1`, nil, executor.MutationOptions{})
}

func TestOpenArchiveFollowsTheRecordsDatabase(t *testing.T) {
	h := newHarness(t)
	h.seedTree()
	rowID := h.insertAlongPath("work fact", pathOf("architecture", "sqlite"))
	h.deleteRow(rowID, 1)
	listed := h.showArchive(`SHOW ARCHIVE FROM work.notes LIMIT 5`, nil)
	archiveID := text(listed.Rows[0]["archive_id"])

	// The harness scope covers "work" only, so a record from another Database
	// cannot be opened even though its ID is known.
	wide := authorization
	wide.AuthorizedDatabases = []string{"work", "other"}
	h.runAuthorized(wide, `CREATE DATABASE other PURPOSE 'p' SCOPE 's'`, nil, executor.MutationOptions{})
	h.runAuthorized(wide, `CREATE TABLE other.notes PURPOSE 'p' ROW SEMANTICS 'r' (title TEXT NOT NULL PURPOSE 't' ROLE title)`, nil, executor.MutationOptions{})
	h.runAuthorized(wide, `CREATE ROUTE ROOT FOR TABLE other.notes PURPOSE 'Everything'`, nil, write("root"))
	hidden := write("insert elsewhere")
	hidden.RoutePath = pathOf("architecture", "sqlite")
	hiddenRow := text(h.runAuthorized(wide, `INSERT INTO other.notes (title) VALUES ('other fact')`, nil, hidden).Rows[0]["row_id"])
	mutation := write("delete elsewhere")
	mutation.ExpectedRevision = 1
	h.runAuthorized(wide, `DELETE FROM other.notes WHERE row_id = :row`, map[string]any{"row": hiddenRow}, mutation)

	otherListed := h.runAuthorized(wide, `SHOW ARCHIVE FROM other.notes LIMIT 5`, nil, executor.MutationOptions{})
	otherID := text(otherListed.Rows[0]["archive_id"])
	if otherID == archiveID {
		t.Fatal("the two databases must archive separately")
	}

	// Authorization follows the record, not the caller's convenient scope.
	narrow := authorization
	narrow.AuthorizedDatabases = []string{"other"}
	if code := h.failsAuthorized(narrow, `OPEN ARCHIVE :id`, map[string]any{"id": archiveID}, executor.MutationOptions{}); code != result.CodePermissionDenied {
		t.Fatalf("opening work's record from an other-only scope: %s", code)
	}
	h.runAuthorized(narrow, `OPEN ARCHIVE :id`, map[string]any{"id": otherID}, executor.MutationOptions{})
}
