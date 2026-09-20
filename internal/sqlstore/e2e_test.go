package sqlstore_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	msqlservice "github.com/HW-Yue/Memora/internal/msql/service"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/security"
	"github.com/HW-Yue/Memora/internal/sqlstore"
)

type harness struct {
	t       *testing.T
	db      *sqlstore.DB
	session *msqlservice.Session
	seq     int
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	db, err := sqlstore.Open(filepath.Join(t.TempDir(), "memora.db"), sqlstore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	service := msqlservice.New(context.Background(), msqlservice.Config{
		Catalog: db, Rows: db.Rows(),
		Transactions: func(ctx context.Context) (executor.ExplicitTransaction, error) { return db.BeginTransaction(ctx) },
	})
	t.Cleanup(func() { _ = service.Close() })
	session, err := service.OpenSession("test")
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, db: db, session: session}
}

var authorization = security.Authorization{
	Version: security.AuthorizationVersion, Actor: "agent:test",
	AuthorizedDatabases: []string{"work"}, DefaultLevel: security.LevelStructural,
}

func (h *harness) run(source string, named map[string]any, mutation executor.MutationOptions) result.StatementResult {
	h.t.Helper()
	h.seq++
	envelope := h.session.ExecuteBatch(context.Background(), executor.BatchRequest{
		RequestID: "r" + string(rune('a'+h.seq%26)) + strings.Repeat("x", h.seq),
		Source:    source,
		Statements: []executor.StatementInput{{
			Parameters: executor.Parameters{Named: named}, Mutation: mutation, Authorization: authorization,
		}},
	})
	if envelope.Error != nil {
		h.t.Fatalf("%s: request error %s: %s", source, envelope.Error.Code, envelope.Error.Message)
	}
	statement := envelope.Results[0]
	if statement.Error != nil {
		h.t.Fatalf("%s: %s: %s", source, statement.Error.Code, statement.Error.Message)
	}
	return statement
}

func (h *harness) fails(source string, named map[string]any, mutation executor.MutationOptions) result.Code {
	h.t.Helper()
	h.seq++
	envelope := h.session.ExecuteBatch(context.Background(), executor.BatchRequest{
		RequestID: "f" + strings.Repeat("y", h.seq), Source: source,
		Statements: []executor.StatementInput{{Parameters: executor.Parameters{Named: named}, Mutation: mutation, Authorization: authorization}},
	})
	if envelope.Error != nil {
		return envelope.Error.Code
	}
	if envelope.Results[0].Error == nil {
		h.t.Fatalf("%s unexpectedly succeeded", source)
	}
	return envelope.Results[0].Error.Code
}

func text(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	default:
		encoded, _ := json.Marshal(typed)
		return strings.Trim(string(encoded), `"`)
	}
}

func write(reason string) executor.MutationOptions {
	return executor.MutationOptions{Actor: "agent:test", Source: "e2e", Reason: reason, SourceKind: "conversation_assertion", MaxAffectedRows: 1, ExpectedSchemaVersion: 1}
}

func TestAgentJourneyOnSQLite(t *testing.T) {
	h := newHarness(t)

	h.run(`CREATE DATABASE work PURPOSE 'Work memory' SCOPE 'Projects and decisions'`, nil, executor.MutationOptions{})
	h.run(`CREATE TABLE work.notes PURPOSE 'Notes' ROW SEMANTICS 'One fact per row' (title TEXT NOT NULL PURPOSE 'Title' ROLE title, body TEXT PURPOSE 'Body' ROLE summary)`, nil, executor.MutationOptions{})

	tables := h.run(`SHOW TABLES FROM work LIMIT 32 COMPACT`, nil, executor.MutationOptions{})
	if len(tables.Rows) != 1 || text(tables.Rows[0]["name"]) != "notes" {
		t.Fatalf("companion tables must stay hidden, got %v", tables.Rows)
	}
	table := h.run(`DESCRIBE TABLE work.notes`, nil, executor.MutationOptions{})
	schemaVersion := table.Rows[0]["schema_version"]

	root := h.run(`CREATE ROUTE ROOT FOR TABLE work.notes PURPOSE :purpose`, map[string]any{"purpose": "Everything about work"}, write("root"))
	rootID := text(root.Rows[0]["route_id"])
	branch := h.run(`CREATE ROUTE UNDER :parent NAME :name KIND :kind PURPOSE :purpose`,
		map[string]any{"parent": rootID, "name": "architecture", "kind": "branch", "purpose": "Architecture decisions about storage engines"}, write("branch"))
	branchID := text(branch.Rows[0]["route_id"])
	leaf := h.run(`CREATE ROUTE UNDER :parent NAME :name KIND :kind PURPOSE :purpose`,
		map[string]any{"parent": branchID, "name": "sqlite", "kind": "leaf", "purpose": "Why the storage engine moved to sqlite"}, write("leaf"))
	leafID := text(leaf.Rows[0]["route_id"])
	if text(leaf.Rows[0]["path"]) != "/architecture/sqlite" {
		t.Fatalf("path = %v", leaf.Rows[0]["path"])
	}

	var expected uint64
	if err := json.Unmarshal([]byte(text(schemaVersion)), &expected); err != nil {
		t.Fatal(err)
	}
	mutation := write("record decision")
	mutation.ExpectedSchemaVersion = expected
	mutation.RouteLeafIDs = []string{leafID}
	inserted := h.run(`INSERT INTO work.notes (title, body) VALUES (:title, :body)`,
		map[string]any{"title": "Use SQLite", "body": "SQLite replaces the native engine"}, mutation)
	if inserted.AffectedRows != 1 {
		t.Fatalf("insert affected %d", inserted.AffectedRows)
	}

	// Walk the tree the way the Agent does.
	top := h.run(`SHOW ROUTES FROM TABLE work.notes AT ROOT LIMIT 12`, nil, executor.MutationOptions{})
	if len(top.Rows) != 1 || text(top.Rows[0]["route_id"]) != branchID {
		t.Fatalf("root children = %v", top.Rows)
	}
	under := h.run(`SHOW ROUTES UNDER :parent LIMIT 12`, map[string]any{"parent": branchID}, executor.MutationOptions{})
	if len(under.Rows) != 1 || text(under.Rows[0]["route_id"]) != leafID {
		t.Fatalf("branch children = %v", under.Rows)
	}
	opened := h.run(`OPEN ROUTE :leaf LIMIT 1`, map[string]any{"leaf": leafID}, executor.MutationOptions{})
	if len(opened.Rows) != 1 {
		t.Fatalf("open route = %v", opened.Rows)
	}
	rowID := text(opened.Rows[0]["row_id"])
	selected := h.run("SELECT * FROM `work`.`notes` WHERE row_id = :row LIMIT 1", map[string]any{"row": rowID}, executor.MutationOptions{})
	if len(selected.Rows) != 1 || text(selected.Rows[0]["title"]) != "Use SQLite" {
		t.Fatalf("select = %v", selected.Rows)
	}

	// Update in place writes history; AS OF reads it back.
	update := write("refine")
	update.ExpectedRevision = 1
	h.run(`UPDATE work.notes SET body = :body WHERE row_id = :row`, map[string]any{"body": "SQLite WAL", "row": rowID}, update)
	historyRows := h.run(`SHOW HISTORY FROM work.notes FOR ROW :row LIMIT 20`, map[string]any{"row": rowID}, executor.MutationOptions{})
	if len(historyRows.Rows) != 2 {
		t.Fatalf("history = %v", historyRows.Rows)
	}
	before := h.run(`SELECT * FROM work.notes AS OF REVISION 1 WHERE row_id = :row LIMIT 1`, map[string]any{"row": rowID}, executor.MutationOptions{})
	if text(before.Rows[0]["body"]) != "SQLite replaces the native engine" {
		t.Fatalf("as of = %v", before.Rows)
	}
	stale := write("stale")
	stale.ExpectedRevision = 1
	if code := h.fails(`UPDATE work.notes SET body = 'x' WHERE row_id = :row`, map[string]any{"row": rowID}, stale); code != result.CodeRevisionConflict {
		t.Fatalf("stale update code = %s", code)
	}

	// Changes timeline.
	changes := h.run(`SHOW CHANGES IN DATABASE work LIMIT 20`, nil, executor.MutationOptions{})
	if len(changes.Rows) == 0 {
		t.Fatal("no committed changes recorded")
	}

	// Explicit transaction: rollback leaves nothing behind.
	h.run(`BEGIN`, nil, executor.MutationOptions{})
	tx := write("rolled back")
	tx.ExpectedSchemaVersion = expected
	h.run(`INSERT INTO work.notes (title) VALUES ('temporary')`, nil, tx)
	h.run(`ROLLBACK`, nil, executor.MutationOptions{})
	report, err := h.db.Doctor(context.Background())
	if err != nil || report.Rows != 1 || report.Integrity != "ok" {
		t.Fatalf("doctor = %+v, %v", report, err)
	}
}

func TestRouteNodeKeepsChildIDsAndDeprecatesInsteadOfDeleting(t *testing.T) {
	h := newHarness(t)
	h.run(`CREATE DATABASE work PURPOSE 'p' SCOPE 's'`, nil, executor.MutationOptions{})
	h.run(`CREATE TABLE work.notes PURPOSE 'p' ROW SEMANTICS 'r' (title TEXT NOT NULL PURPOSE 'title' ROLE title)`, nil, executor.MutationOptions{})
	root := text(h.run(`CREATE ROUTE ROOT FOR TABLE work.notes PURPOSE 'root'`, nil, write("root")).Rows[0]["route_id"])
	a := text(h.run(`CREATE ROUTE UNDER :p NAME 'a' KIND 'leaf' PURPOSE 'a'`, map[string]any{"p": root}, write("a")).Rows[0]["route_id"])
	h.run(`CREATE ROUTE UNDER :p NAME 'b' KIND 'leaf' PURPOSE 'b'`, map[string]any{"p": root}, write("b"))

	var body string
	if err := h.db.SQL().QueryRow(`SELECT body FROM mem_route_index i JOIN mem_tables t ON t.id = i.table_id WHERE i.route_id = ?`, root).Scan(&body); err != nil {
		t.Fatal(err)
	}
	var tableID string
	if err := h.db.SQL().QueryRow(`SELECT table_id FROM mem_route_index WHERE route_id = ?`, root).Scan(&tableID); err != nil {
		t.Fatal(err)
	}
	if err := h.db.SQL().QueryRow(`SELECT body FROM "routes_`+tableID+`" WHERE route_id = ?`, root).Scan(&body); err != nil {
		t.Fatal(err)
	}
	var stored struct {
		ChildIDs []string `json:"child_ids"`
	}
	if err := json.Unmarshal([]byte(body), &stored); err != nil || len(stored.ChildIDs) != 2 {
		t.Fatalf("root child_ids = %v (%v)", stored.ChildIDs, err)
	}

	retire := write("retire")
	retire.ExpectedRevision = 1
	h.run(`DELETE ROUTE :r`, map[string]any{"r": a}, retire)
	var deprecated int
	if err := h.db.SQL().QueryRow(`SELECT deprecated FROM "routes_`+tableID+`" WHERE route_id = ?`, a).Scan(&deprecated); err != nil || deprecated != 1 {
		t.Fatalf("deleted route must remain as deprecated, got %d (%v)", deprecated, err)
	}
	children := h.run(`SHOW ROUTES UNDER :p LIMIT 12`, map[string]any{"p": root}, executor.MutationOptions{})
	if len(children.Rows) != 1 || text(children.Rows[0]["name"]) != "b" {
		t.Fatalf("children after retire = %v", children.Rows)
	}
}

func TestSplitSupersedesTheSourceWithSuccessors(t *testing.T) {
	h := newHarness(t)
	h.run(`CREATE DATABASE work PURPOSE 'p' SCOPE 's'`, nil, executor.MutationOptions{})
	h.run(`CREATE TABLE work.notes PURPOSE 'p' ROW SEMANTICS 'r' (title TEXT NOT NULL PURPOSE 'title' ROLE title)`, nil, executor.MutationOptions{})
	row := text(h.run(`INSERT INTO work.notes (title) VALUES ('both facts')`, nil, write("seed")).Rows[0]["row_id"])
	split := write("split")
	split.ExpectedRevision = 1
	split.MaxAffectedRows = 3
	split.TargetRouteLeafIDs = [][]string{{}, {}}
	h.run(`SPLIT work.notes ROW :row INTO (title) VALUES ('fact one'), ('fact two')`, map[string]any{"row": row}, split)
	successors, err := h.db.Successors(context.Background(), "work", "notes", row)
	if err != nil || len(successors) != 2 {
		t.Fatalf("successors = %v, %v", successors, err)
	}
	if rows := h.run("SELECT * FROM `work`.`notes` WHERE row_id = :row LIMIT 1", map[string]any{"row": row}, executor.MutationOptions{}).Rows; len(rows) != 0 {
		t.Fatalf("superseded row must not read as live, got %v", rows)
	}
}
