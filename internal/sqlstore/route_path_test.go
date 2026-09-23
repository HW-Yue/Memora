package sqlstore_test

import (
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/router"
)

// An INSERT may name a path instead of a leaf id. Every segment carries its own
// name, kind and purpose, so completing the path is the write's consequence
// rather than the engine guessing semantics; see
// docs/query/implicit-route-path-v1.md.

func segment(name string, kind router.Kind, purpose string) router.PathSegment {
	return router.PathSegment{Name: name, Kind: kind, Purpose: purpose}
}

// seedTree creates the Database, Table and root without any leaf, so a path's
// result is the whole tree.
func (h *harness) seedTree() string {
	h.t.Helper()
	h.run(`CREATE DATABASE work PURPOSE 'Work memory' SCOPE 'Projects'`, nil, executor.MutationOptions{})
	h.run(`CREATE TABLE work.notes PURPOSE 'Notes' ROW SEMANTICS 'One fact' (title TEXT NOT NULL PURPOSE 'Title' ROLE title)`, nil, executor.MutationOptions{})
	return text(h.run(`CREATE ROUTE ROOT FOR TABLE work.notes PURPOSE 'Everything'`, nil, write("root")).Rows[0]["route_id"])
}

func (h *harness) rootChildren() []result.Row {
	h.t.Helper()
	return h.run(`SHOW ROUTES FROM TABLE work.notes AT ROOT`, nil, executor.MutationOptions{}).Rows
}

func (h *harness) insertAlongPath(title string, path []router.PathSegment) string {
	h.t.Helper()
	mutation := write("insert along a path")
	mutation.RoutePath = path
	return text(h.run(`INSERT INTO work.notes (title) VALUES (:title)`, map[string]any{"title": title}, mutation).Rows[0]["row_id"])
}

func TestInsertCompletesAMissingPath(t *testing.T) {
	h := newHarness(t)
	h.seedTree()

	path := []router.PathSegment{
		segment("architecture", router.KindBranch, "架构决策"),
		segment("storage", router.KindBranch, "存储选型"),
		segment("sqlite", router.KindLeaf, "为什么选 SQLite"),
	}
	rowID := h.insertAlongPath("Use SQLite", path)

	if leaves := h.storedLeaves(rowID); len(leaves) != 1 {
		t.Fatalf("stored leaves = %v", leaves)
	}
	// The Row is reachable the way an Agent navigates: layer by layer.
	top := h.rootChildren()
	if len(top) != 1 || text(top[0]["name"]) != "architecture" {
		t.Fatalf("root children = %v", top)
	}
	under := h.run(`SHOW ROUTES UNDER :p`, map[string]any{"p": text(top[0]["route_id"])}, executor.MutationOptions{})
	if len(under.Rows) != 1 || text(under.Rows[0]["name"]) != "storage" {
		t.Fatalf("second level = %v", under.Rows)
	}
	last := h.run(`SHOW ROUTES UNDER :p`, map[string]any{"p": text(under.Rows[0]["route_id"])}, executor.MutationOptions{})
	if len(last.Rows) != 1 || text(last.Rows[0]["name"]) != "sqlite" || text(last.Rows[0]["kind"]) != "leaf" {
		t.Fatalf("leaf level = %v", last.Rows)
	}
	opened := h.run(`OPEN ROUTE :leaf LIMIT 1`, map[string]any{"leaf": text(last.Rows[0]["route_id"])}, executor.MutationOptions{})
	if len(opened.Rows) != 1 || text(opened.Rows[0]["row_id"]) != rowID {
		t.Fatalf("open route = %v", opened.Rows)
	}
}

// Reusing the existing part of a path is the point of naming segments rather
// than ids: the second write only adds its own leaf.
func TestInsertReusesTheExistingPrefix(t *testing.T) {
	h := newHarness(t)
	h.seedTree()
	prefix := []router.PathSegment{segment("architecture", router.KindBranch, "架构决策")}

	first := h.insertAlongPath("first", append(append([]router.PathSegment{}, prefix...), segment("sqlite", router.KindLeaf, "为什么存储层是 SQLite")))
	second := h.insertAlongPath("second", append(append([]router.PathSegment{}, prefix...), segment("wal", router.KindLeaf, "预写日志怎么保证提交后读得到")))

	top := h.rootChildren()
	if len(top) != 1 {
		t.Fatalf("the shared branch must not be duplicated: %v", top)
	}
	under := h.run(`SHOW ROUTES UNDER :p`, map[string]any{"p": text(top[0]["route_id"])}, executor.MutationOptions{})
	if len(under.Rows) != 2 {
		t.Fatalf("both leaves must hang under the one shared branch: %v", under.Rows)
	}
	for _, rowID := range []string{first, second} {
		if leaves := h.storedLeaves(rowID); len(leaves) != 1 {
			t.Fatalf("row %s leaves = %v", rowID, leaves)
		}
	}
}

func TestInsertRejectsPathsItCannotComplete(t *testing.T) {
	h := newHarness(t)
	h.seedTree()

	cases := []struct {
		name string
		path []router.PathSegment
	}{
		{name: "empty path", path: []router.PathSegment{}},
		{name: "blank name", path: []router.PathSegment{segment("  ", router.KindLeaf, "p")}},
		{name: "blank purpose", path: []router.PathSegment{segment("a", router.KindLeaf, "  ")}},
		{name: "slash in name", path: []router.PathSegment{segment("a/b", router.KindLeaf, "p")}},
		{name: "unknown kind", path: []router.PathSegment{segment("a", router.Kind("twig"), "p")}},
		{name: "last segment is a branch", path: []router.PathSegment{segment("a", router.KindBranch, "p")}},
	}
	for _, tc := range cases {
		mutation := write("reject")
		mutation.RoutePath = tc.path
		source := `INSERT INTO work.notes (title) VALUES ('rejected')`
		if code := h.fails(source, nil, mutation); code != result.CodeValidation {
			t.Fatalf("%s: code = %s", tc.name, code)
		}
	}

	// A leaf cannot be an interior segment: nothing hangs below a leaf.
	walk := []router.PathSegment{
		segment("architecture", router.KindBranch, "架构决策"),
		segment("mqtt", router.KindLeaf, "为什么设备侧走 MQTT"),
	}
	h.insertAlongPath("leaf in the middle", walk)
	deep := write("reject")
	deep.RoutePath = append(append([]router.PathSegment{}, walk...), segment("below", router.KindLeaf, "Below a leaf"))
	if code := h.fails(`INSERT INTO work.notes (title) VALUES ('rejected')`, nil, deep); code != result.CodeConstraint {
		t.Fatalf("interior leaf: code = %s", code)
	}

	// A leaf under the same name but a different purpose is a semantic clash,
	// not something to silently rewrite.
	clash := write("reject")
	clash.RoutePath = []router.PathSegment{segment("architecture", router.KindBranch, "架构决策"), segment("mqtt", router.KindLeaf, "别的用途")}
	if code := h.fails(`INSERT INTO work.notes (title) VALUES ('rejected')`, nil, clash); code != result.CodeConstraint {
		t.Fatalf("purpose clash: code = %s", code)
	}

	// An occupied leaf cannot take a second Row.
	occupied := write("reject")
	occupied.RoutePath = walk
	if code := h.fails(`INSERT INTO work.notes (title) VALUES ('rejected')`, nil, occupied); code != result.CodeConstraint {
		t.Fatalf("occupied leaf: code = %s", code)
	}

	// Only the one successful walk landed.
	if h.liveRows() != 1 {
		t.Fatalf("refused writes must not land: %d live rows", h.liveRows())
	}
}

func TestInsertRefusesAnImplicitPathWithoutARoot(t *testing.T) {
	h := newHarness(t)
	h.run(`CREATE DATABASE work PURPOSE 'p' SCOPE 's'`, nil, executor.MutationOptions{})
	h.run(`CREATE TABLE work.notes PURPOSE 'p' ROW SEMANTICS 'r' (title TEXT NOT NULL PURPOSE 'title' ROLE title)`, nil, executor.MutationOptions{})

	mutation := write("no root yet")
	mutation.RoutePath = []router.PathSegment{segment("architecture", router.KindLeaf, "架构决策")}
	source := `INSERT INTO work.notes (title) VALUES ('rejected')`
	if code := h.fails(source, nil, mutation); code != result.CodeValidation {
		t.Fatalf("code = %s", code)
	}
	if h.liveRows() != 0 {
		t.Fatalf("nothing may be written: %d live rows", h.liveRows())
	}
}

func TestInsertRefusesBothAMountAndAPath(t *testing.T) {
	h := newHarness(t)
	_, leaf := h.seedNotes()

	mutation := write("both")
	mutation.RouteLeafIDs = []string{leaf}
	mutation.RoutePath = []router.PathSegment{segment("architecture", router.KindLeaf, "架构决策")}
	source := `INSERT INTO work.notes (title) VALUES ('rejected')`
	if code := h.fails(source, nil, mutation); code != result.CodeValidation {
		t.Fatalf("code = %s", code)
	}
}
