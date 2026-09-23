package sqlstore_test

import (
	"context"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/router"
)

// Every Route must carry a `purpose`, and what is required is a description,
// not a non-empty string: a purpose that only repeats the name says nothing the
// name did not already say, and the retrieval path that reads those two fields
// is left choosing between bare labels. The rule has two halves on the write
// side — a new Route is refused, an existing one is only reported — because a
// library that already holds such Routes must stay writable while its purposes
// are being filled in. See docs/planning/route-purpose-contract.md and
// docs/decisions.md「语义树的标签质量是可测量的检索损伤」.

// failureMessage runs a statement that must fail and returns the message a
// caller would read: a refusal whose text does not say what to do instead is
// answered by trying the same thing again.
func (h *harness) failureMessage(source string, named map[string]any, mutation executor.MutationOptions) string {
	h.t.Helper()
	h.seq++
	envelope := h.session.ExecuteBatch(context.Background(), executor.BatchRequest{
		RequestID: "m" + strings.Repeat("z", h.seq), Source: source,
		Statements: []executor.StatementInput{{
			Parameters: executor.Parameters{Named: named}, Mutation: mutation, Authorization: authorization,
		}},
	})
	if envelope.Error != nil {
		return envelope.Error.Message
	}
	if envelope.Results[0].Error == nil {
		h.t.Fatalf("%s unexpectedly succeeded", source)
	}
	return envelope.Results[0].Error.Message
}

func createChild(h *harness, parent, name, kind, purpose string) result.StatementResult {
	h.t.Helper()
	return h.run(`CREATE ROUTE UNDER :parent NAME :name KIND :kind PURPOSE :purpose`,
		map[string]any{"parent": parent, "name": name, "kind": kind, "purpose": purpose}, write("child"))
}

func TestCreateRouteRefusesAPurposeThatRepeatsTheName(t *testing.T) {
	h := newHarness(t)
	root := h.seedTree()

	// The four ways the same absence is written: the name again, the name with
	// padding, the name in another case, and the name in full-width letters.
	for _, purpose := range []string{"storage", "  storage  ", "STORAGE", "ｓｔｏｒａｇｅ"} {
		code := h.fails(`CREATE ROUTE UNDER :parent NAME :name KIND :kind PURPOSE :purpose`,
			map[string]any{"parent": root, "name": "storage", "kind": "branch", "purpose": purpose},
			write("child"))
		if code != result.CodeValidation {
			t.Fatalf("purpose %q was accepted or refused wrongly: %s", purpose, code)
		}
	}
	// The refusal has to say what to write instead, or the next attempt is a
	// space added to the same word.
	message := h.failureMessage(`CREATE ROUTE UNDER :parent NAME :name KIND :kind PURPOSE :purpose`,
		map[string]any{"parent": root, "name": "storage", "kind": "branch", "purpose": "storage"},
		write("child"))
	for _, expected := range []string{"purpose", "storage"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("the refusal must name %q: %s", expected, message)
		}
	}

	// A real description of the same Route is accepted: the rule is about
	// content, and nothing else about CREATE ROUTE changed.
	createChild(h, root, "storage", "branch", "为什么存储层是 SQLite，以及页、WAL 归谁负责")
}

func TestRoutePathRefusesANewSegmentWhosePurposeRepeatsItsName(t *testing.T) {
	h := newHarness(t)
	h.seedTree()

	mutation := write("insert along a path")
	mutation.RoutePath = []router.PathSegment{
		segment("architecture", router.KindBranch, "架构决策"),
		segment("sqlite", router.KindLeaf, "SQLite"),
	}
	if code := h.fails(`INSERT INTO work.notes (title) VALUES (:title)`,
		map[string]any{"title": "Use SQLite"}, mutation); code != result.CodeValidation {
		t.Fatalf("a path segment that names itself twice must be refused, got %s", code)
	}
	// Nothing was created on the way to the refusal.
	if children := h.rootChildren(); len(children) != 0 {
		t.Fatalf("a refused path must leave no branch behind: %v", children)
	}
}

// The other half of the rule. A Route that already repeats its name is reported
// and left alone: refusing it would lock the writer out of exactly the library
// that needs repairing, which is the opposite of the point.
func TestRenamingARouteIntoItsOwnPurposeWarnsInsteadOfRefusing(t *testing.T) {
	h := newHarness(t)
	root := h.seedTree()
	branch := text(createChild(h, root, "storage", "branch", "内部结构").Rows[0]["route_id"])

	rename := write("rename")
	rename.ExpectedRevision = 1
	renamed := h.run(`ALTER ROUTE :route RENAME TO :name`,
		map[string]any{"route": branch, "name": "内部结构"}, rename)
	if text(renamed.Rows[0]["name"]) != "内部结构" {
		t.Fatalf("the rename must still happen: %v", renamed.Rows)
	}
	found := false
	for _, warning := range renamed.Warnings {
		if warning.Code != "route_purpose_repeats_name" {
			continue
		}
		found = true
		if !strings.Contains(warning.Message, "内部结构") {
			t.Fatalf("the notice must name the Route: %s", warning.Message)
		}
		if formatted := text(warning.Details["route_id"]); formatted != branch {
			t.Fatalf("the notice must carry the Route it means: %v", warning.Details)
		}
	}
	if !found {
		t.Fatalf("a Route whose purpose now repeats its name must be reported, got %+v", renamed.Warnings)
	}

	// And the library stays writable through it: a path that resolves to that
	// Route keeps working, because nothing new is being created there.
	mutation := write("insert along a path")
	mutation.RoutePath = []router.PathSegment{
		segment("内部结构", router.KindBranch, "内部结构"),
		segment("wal", router.KindLeaf, "预写日志怎么保证提交后读得到"),
	}
	h.run(`INSERT INTO work.notes (title) VALUES (:title)`, map[string]any{"title": "WAL"}, mutation)
	under := h.run(`SHOW ROUTES UNDER :p`, map[string]any{"p": branch}, executor.MutationOptions{})
	if len(under.Rows) != 1 || text(under.Rows[0]["name"]) != "wal" {
		t.Fatalf("the existing Route must still accept a child: %v", under.Rows)
	}
}

// The third half of the rule: what already exists has to be countable. The
// write path stops new ones, the walk says when a layer was chosen blind, and
// doctor is where the backlog is read off — a number that goes down as the
// purposes get written, and the paths to write them at.
func TestDoctorReportsRoutesWhosePurposeRepeatsTheirName(t *testing.T) {
	h := newHarness(t)
	root := h.seedTree()
	described := text(createChild(h, root, "storage", "branch", "内部结构：页、WAL 与恢复归谁负责").Rows[0]["route_id"])
	repeating := text(createChild(h, root, "rekey", "leaf", "换 embedding 模型之后怎么把整库向量换过去").Rows[0]["route_id"])

	if report := h.doctor(); report.RoutesWithoutPurpose != 0 {
		t.Fatalf("a described tree has no backlog: %+v", report)
	}

	rename := write("rename")
	rename.ExpectedRevision = 1
	h.run(`ALTER ROUTE :route RENAME TO :name`,
		map[string]any{"route": repeating, "name": "换 embedding 模型之后怎么把整库向量换过去"}, rename)

	report := h.doctor()
	if report.RoutesWithoutPurpose != 1 {
		t.Fatalf("routes without a purpose = %d, want 1: %+v", report.RoutesWithoutPurpose, report)
	}
	// A number alone cannot be acted on: the report says where, Database and
	// Table included, because a path is only unique inside one Table.
	if len(report.RoutesWithoutPurposePaths) != 1 ||
		!strings.Contains(report.RoutesWithoutPurposePaths[0], "work.notes") ||
		!strings.Contains(report.RoutesWithoutPurposePaths[0], "换 embedding 模型之后怎么把整库向量换过去") {
		t.Fatalf("the report must name the Route to repair: %v", report.RoutesWithoutPurposePaths)
	}
	// A missing description is a gap in the tree's labels, not a broken
	// Instance: reporting it as corruption would make every real fault harder
	// to see, and `memora doctor` exits non-zero on unhealthy.
	if report.Status != "healthy" {
		t.Fatalf("a bare label is not corruption: %+v", report)
	}
	if described == "" {
		t.Fatal("the described Route was not created")
	}
}
