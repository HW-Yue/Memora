package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
)

// The archive rule — "an object is visible iff neither it nor any ancestor is
// archived" — is only as good as its weakest read surface, and the rename rule
// — "a Catalog change never hides existing data" — is only as good as its
// weakest one too. Both are exhaustive claims, so both are tested as a matrix
// over every read surface rather than as a handful of hand-picked cases.
//
// readSurface is one MSQL read, parameterised by the names it addresses so the
// same list can be replayed after a rename.
type readSurface struct {
	name   string
	source func(database, table, column string) string
	params map[string]any
	// hiddenByDatabaseArchive is false only for reads that legitimately keep
	// working when the Database is archived (there are none today; the field
	// exists so an intentional exception has to be written down).
	skipRename bool
}

func catalogReadSurfaces() []readSurface {
	return []readSurface{
		{name: "SHOW TABLES", source: func(d, _, _ string) string { return "SHOW TABLES FROM " + d }},
		{name: "SHOW COLUMNS", source: func(d, t, _ string) string { return "SHOW COLUMNS FROM " + d + "." + t }},
		{name: "DESCRIBE DATABASE", source: func(d, _, _ string) string { return "DESCRIBE DATABASE " + d }},
		{name: "DESCRIBE TABLE", source: func(d, t, _ string) string { return "DESCRIBE TABLE " + d + "." + t }},
		{name: "SELECT", source: func(d, t, c string) string {
			return "SELECT " + c + ", row_id, revision FROM " + d + "." + t + " LIMIT 10"
		}},
		{name: "SELECT WHERE", source: func(d, t, c string) string {
			return "SELECT " + c + " FROM " + d + "." + t + " WHERE " + c + " = :needle LIMIT 10"
		}, params: map[string]any{"needle": "crash recovery"}},
		{name: "SHOW HISTORY", source: func(d, t, _ string) string {
			return "SHOW HISTORY FROM " + d + "." + t + " FOR ROW :row LIMIT 10"
		}, params: map[string]any{"row": ""}},
		{name: "SHOW ROUTES AT ROOT", source: func(d, t, _ string) string {
			return "SHOW ROUTES FROM TABLE " + d + "." + t + " AT ROOT LIMIT 12"
		}},
	}
}

// instanceReadSurfaces address no Database by name, so a rename cannot break
// them; what matters is whether an archived object still shows up in them.
func instanceReadSurfaces() []readSurface {
	return []readSurface{
		{name: "SHOW DATABASES", source: func(string, string, string) string { return "SHOW DATABASES" }, skipRename: true},
		{name: "SHOW CATALOG ATLAS", source: func(string, string, string) string {
			return "SHOW CATALOG ATLAS LIMIT 64 BYTES 8192 COMPACT"
		}, skipRename: true},
		{name: "SHOW LEXICAL LOCATIONS", source: func(string, string, string) string {
			return "SHOW LEXICAL LOCATIONS FROM ALL TABLES USING :needle LIMIT 10 BYTES 8192"
		}, params: map[string]any{"needle": "crash recovery"}, skipRename: true},
		{name: "SHOW ROUTE CANDIDATES", source: func(string, string, string) string {
			return "SHOW ROUTE CANDIDATES FROM ALL TABLES USING LEXICAL :needle LIMIT 8 BYTES 4096"
		}, params: map[string]any{"needle": "crash recovery"}, skipRename: true},
	}
}

type matrixFixture struct {
	dataDir string
	rowID   string
}

// newMatrixFixture builds one Database with two Tables, a Route tree and one
// Row whose text is searchable, so every surface in the matrix has something
// real to return.
func newMatrixFixture(t *testing.T) matrixFixture {
	t.Helper()
	dataDir := archiveInstance(t)
	rows := executeTraceMSQL(t, dataDir, "SELECT row_id FROM work.notes LIMIT 10", nil)
	rowID, _ := rows.Results[0].Rows[0]["row_id"].(string)
	executeTraceMSQL(t, dataDir,
		"UPDATE work.notes SET title = :title WHERE row_id = :row",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"title": "crash recovery", "row": rowID}},
			Mutation: executor.MutationOptions{
				ExpectedSchemaVersion: 1, ExpectedRevision: 1, MaxAffectedRows: 1,
				Actor: "agent:test", Source: "event_2", Reason: "make the Row searchable",
			},
		}},
	)
	rootResult := executeTraceMSQL(t, dataDir,
		"CREATE ROUTE ROOT FOR TABLE work.notes PURPOSE :purpose",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"purpose": "Notes root"}},
			Mutation:   executor.MutationOptions{MaxAffectedRows: 1},
		}},
	)
	rootID, _ := rootResult.Results[0].Rows[0]["route_id"].(string)
	executeTraceMSQL(t, dataDir,
		"CREATE ROUTE UNDER :parent NAME :name KIND :kind PURPOSE :purpose",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{
				"parent": rootID, "name": "decisions", "kind": "leaf", "purpose": "Decision Row",
			}},
			Mutation: executor.MutationOptions{MaxAffectedRows: 1},
		}},
	)
	return matrixFixture{dataDir: dataDir, rowID: rowID}
}

// mentions reports whether any returned row names the object, so an
// instance-wide surface can be checked for leaks by content rather than by a
// row count that legitimately includes live siblings.
func (fixture matrixFixture) mentions(t *testing.T, surface readSurface, needle string) bool {
	t.Helper()
	rows := fixture.rows(t, surface, "work", "notes", "title")
	encoded, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Contains(string(encoded), needle)
}

func (fixture matrixFixture) rows(t *testing.T, surface readSurface, database, table, column string) []result.Row {
	t.Helper()
	envelope := fixture.execute(t, surface, database, table, column)
	values := []result.Row{}
	for _, statement := range envelope.Results {
		values = append(values, statement.Rows...)
	}
	return values
}

func (fixture matrixFixture) execute(t *testing.T, surface readSurface, database, table, column string) result.Envelope {
	t.Helper()
	params := map[string]any{}
	for key, value := range surface.params {
		params[key] = value
	}
	if _, ok := params["row"]; ok {
		params["row"] = fixture.rowID
	}
	var statements []executor.StatementInput
	if len(params) > 0 {
		statements = []executor.StatementInput{{Parameters: executor.Parameters{Named: params}}}
	}
	envelope, err := Execute(context.Background(), fixture.dataDir, surface.source(database, table, column), statements)
	if err != nil {
		t.Fatalf("%s: %v", surface.name, err)
	}
	return envelope
}

func (fixture matrixFixture) read(t *testing.T, surface readSurface, database, table, column string) (bool, int, string) {
	t.Helper()
	envelope := fixture.execute(t, surface, database, table, column)
	message := ""
	if envelope.Error != nil {
		message = envelope.Error.Message
	}
	rows := 0
	for _, statement := range envelope.Results {
		if statement.Error != nil {
			message += " " + statement.Error.Message
		}
		rows += len(statement.Rows)
	}
	return envelope.OK, rows, message
}

// TestEveryReadSurfaceSurvivesARename is the rename half of the matrix. A
// Catalog change bumps schema versions without rewriting Rows, and the bug it
// caught (F227 groundwork) made every Row unreadable afterwards, so each
// surface is replayed under the new names and must still return its data.
func TestEveryReadSurfaceSurvivesARename(t *testing.T) {
	fixture := newMatrixFixture(t)

	executeTraceMSQL(t, fixture.dataDir, "ALTER TABLE work.notes RENAME COLUMN title TO heading", nil)
	executeTraceMSQL(t, fixture.dataDir, "ALTER TABLE work.notes RENAME TO memos", nil)
	executeTraceMSQL(t, fixture.dataDir, "ALTER DATABASE work RENAME TO projects", nil)

	for _, surface := range append(catalogReadSurfaces(), instanceReadSurfaces()...) {
		if surface.skipRename {
			continue
		}
		t.Run(surface.name, func(t *testing.T) {
			ok, rows, message := fixture.read(t, surface, "projects", "memos", "heading")
			if !ok {
				t.Fatalf("%s must still work after a rename: %s", surface.name, message)
			}
			if rows == 0 {
				t.Fatalf("%s returned nothing after a rename", surface.name)
			}
		})
	}
}

// TestEveryReadSurfaceHidesAnArchivedDatabase is the archive half. Every
// surface either fails naming UNARCHIVE or returns nothing; none may leak the
// archived Database or its Rows.
func TestEveryReadSurfaceHidesAnArchivedDatabase(t *testing.T) {
	fixture := newMatrixFixture(t)

	if ok, message := run(t, fixture.dataDir, "ARCHIVE DATABASE work REASON :reason",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"reason": "retired project"}},
		}},
	); !ok {
		t.Fatalf("ARCHIVE DATABASE failed: %s", message)
	}

	for _, surface := range catalogReadSurfaces() {
		t.Run(surface.name, func(t *testing.T) {
			ok, _, message := fixture.read(t, surface, "work", "notes", "title")
			if ok {
				t.Fatalf("%s must fail on an archived Database", surface.name)
			}
			if !strings.Contains(message, "UNARCHIVE") {
				t.Fatalf("%s should point at UNARCHIVE, got %q", surface.name, message)
			}
		})
	}
	for _, surface := range instanceReadSurfaces() {
		t.Run(surface.name, func(t *testing.T) {
			ok, rows, message := fixture.read(t, surface, "work", "notes", "title")
			if !ok {
				t.Fatalf("%s must keep working with an archived Database present: %s", surface.name, message)
			}
			if rows != 0 {
				t.Fatalf("%s leaked %d rows from an archived Database", surface.name, rows)
			}
		})
	}
}

// TestEveryReadSurfaceHidesAnArchivedTable is the same sweep one level down:
// the Database stays live, so instance-wide surfaces must still list it while
// dropping everything that belongs to the archived Table.
func TestEveryReadSurfaceHidesAnArchivedTable(t *testing.T) {
	fixture := newMatrixFixture(t)

	if ok, message := run(t, fixture.dataDir, "ARCHIVE TABLE work.notes REASON :reason",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"reason": "merged elsewhere"}},
		}},
	); !ok {
		t.Fatalf("ARCHIVE TABLE failed: %s", message)
	}

	for _, surface := range catalogReadSurfaces() {
		if surface.name == "SHOW TABLES" || surface.name == "DESCRIBE DATABASE" {
			// These address the still-live Database, so they must succeed and
			// simply not mention the archived Table.
			ok, _, message := fixture.read(t, surface, "work", "notes", "title")
			if !ok {
				t.Fatalf("%s must keep working when only a Table is archived: %s", surface.name, message)
			}
			continue
		}
		t.Run(surface.name, func(t *testing.T) {
			ok, _, message := fixture.read(t, surface, "work", "notes", "title")
			if ok {
				t.Fatalf("%s must fail on an archived Table", surface.name)
			}
			if !strings.Contains(message, "UNARCHIVE") {
				t.Fatalf("%s should point at UNARCHIVE, got %q", surface.name, message)
			}
		})
	}
	if _, rows, _ := fixture.read(t, readSurface{
		name: "SHOW TABLES", source: func(d, _, _ string) string { return "SHOW TABLES FROM " + d },
	}, "work", "notes", "title"); rows != 1 {
		t.Fatalf("SHOW TABLES should list only the live Table, got %d", rows)
	}
	// The Database is still live, so these surfaces must keep working and keep
	// listing the live siblings. What they must never do is name the archived
	// Table or return one of its Rows.
	for _, surface := range instanceReadSurfaces() {
		t.Run(surface.name, func(t *testing.T) {
			ok, _, message := fixture.read(t, surface, "work", "notes", "title")
			if !ok {
				t.Fatalf("%s must keep working with an archived Table present: %s", surface.name, message)
			}
			for _, needle := range []string{"notes", fixture.rowID} {
				if fixture.mentions(t, surface, needle) {
					t.Fatalf("%s leaked %q from an archived Table", surface.name, needle)
				}
			}
		})
	}
}
