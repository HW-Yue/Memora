package sqlstore_test

import (
	"context"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/skillwrite"
)

// planTool runs a Mutation Plan against the harness's session, the way the CLI
// does: the plan's preflight, mutation and verification all travel through the
// same transport.
func (h *harness) planTool() skillwrite.Tool {
	return skillwrite.ToolFunc(func(ctx context.Context, call skillwrite.Call) (result.Envelope, error) {
		return h.session.ExecuteBatch(ctx, call.Request), nil
	})
}

func intPointer(value int) *int { return &value }

// A Mutation Plan has to be able to express the mount form the Skill tells a
// host to prefer. It could not: the validator demanded a route_leaf_ids
// snapshot, so the only way to write on a new path was to skip the plan — and
// the plan is where the write is checked before any tool call. The whole plan
// below runs with the LevelWrite authorization the runner builds, which is also
// the claim that completing a position is part of the Row write rather than a
// structural change: the leaf leaves with the Row, and the branch it needed is
// pruned when the removal empties it.
func TestAMutationPlanMountsAnInsertOnANewPath(t *testing.T) {
	h := newHarness(t)
	h.run(`CREATE DATABASE work PURPOSE 'Work memory' SCOPE 'Projects'`, nil, executor.MutationOptions{})
	h.run(`CREATE TABLE work.notes PURPOSE 'Notes' ROW SEMANTICS 'One fact' (title TEXT NOT NULL PURPOSE 'Title' ROLE title, summary TEXT NOT NULL PURPOSE 'Body' ROLE summary)`, nil, executor.MutationOptions{})
	h.run(`CREATE ROUTE ROOT FOR TABLE work.notes PURPOSE 'Everything'`, nil, write("root"))

	plan := skillwrite.Plan{
		Version: skillwrite.PlanVersion, ID: "plan-1", Decision: skillwrite.DecisionInsert,
		Database: "work", Table: "notes", Actor: "agent:test", SourceEventID: "e2e",
		Reason: "record the decision", AuthorizedDatabases: []string{"work"},
		Preflight: []skillwrite.Check{{
			ID: "census", MSQL: "SELECT row_id FROM work.notes LIMIT 1", ExpectRows: intPointer(0),
		}},
		Steps: []skillwrite.Step{{
			ID: "insert", Kind: "INSERT", Target: "work.notes",
			MSQL: "INSERT INTO work.notes (title, summary) VALUES (:title, :summary)",
			Input: executor.StatementInput{
				Parameters: executor.Parameters{Named: map[string]any{
					"title": "Use SQLite", "summary": "SQLite carries the pages.",
				}},
				Mutation: executor.MutationOptions{
					ExpectedSchemaVersion: 1, MaxAffectedRows: 1,
					Actor: "agent:test", Source: "e2e", Reason: "record the decision",
					RoutePath: []router.PathSegment{
						{Name: "architecture", Kind: router.KindBranch, Purpose: "Architecture decisions"},
						{Name: "sqlite", Kind: router.KindLeaf, Purpose: "Why SQLite"},
					},
				},
			},
		}},
		Verify: []skillwrite.Check{{
			ID: "read-back", MSQL: "SELECT title FROM work.notes LIMIT 1", ExpectRows: intPointer(1),
		}},
	}
	report, err := skillwrite.New(h.planTool()).Run(context.Background(), plan)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if report.Receipt.Status != skillwrite.ReceiptCommitted || !report.Receipt.Verified {
		t.Fatalf("receipt = %+v", report.Receipt)
	}
	if len(report.Receipt.Changes) != 1 || report.Receipt.Changes[0].ObjectID == "" {
		t.Fatalf("changes = %+v", report.Receipt.Changes)
	}

	rowID := report.Receipt.Changes[0].ObjectID
	census := h.run(`SELECT row_id FROM work.notes LIMIT 10`, nil, executor.MutationOptions{})
	if len(census.Rows) != 1 || text(census.Rows[0]["row_id"]) != rowID {
		t.Fatalf("census = %v", census.Rows)
	}
	paths, ok := census.Rows[0]["route_paths"]
	if !ok {
		t.Fatalf("a SELECT Row must carry its route_paths: %v", census.Rows[0])
	}
	encoded := text(paths)
	if !strings.Contains(encoded, "architecture") || !strings.Contains(encoded, "sqlite") {
		t.Fatalf("route_paths = %q, want the path the plan named", encoded)
	}

	// The position the plan completed is a real navigable position, not just a
	// string in the Row's mount list.
	branches := h.run(`SHOW ROUTES FROM TABLE work.notes AT ROOT LIMIT 12`, nil, executor.MutationOptions{})
	if len(branches.Rows) != 1 || text(branches.Rows[0]["name"]) != "architecture" {
		t.Fatalf("root children = %v", branches.Rows)
	}
	leaves := h.run(`SHOW ROUTES UNDER :parent LIMIT 12`,
		map[string]any{"parent": text(branches.Rows[0]["route_id"])}, executor.MutationOptions{})
	if len(leaves.Rows) != 1 || text(leaves.Rows[0]["name"]) != "sqlite" {
		t.Fatalf("branch children = %v", leaves.Rows)
	}
	opened := h.run(`OPEN ROUTE :leaf LIMIT 1`,
		map[string]any{"leaf": text(leaves.Rows[0]["route_id"])}, executor.MutationOptions{})
	if len(opened.Rows) != 1 || text(opened.Rows[0]["row_id"]) != rowID {
		t.Fatalf("open route = %v", opened.Rows)
	}

	// The claim that this is a Row write, not a structural one, is the leaf's
	// fate: it goes with the Row, and the branch it needed goes with the leaf.
	deletion := write("delete")
	if report.Receipt.Changes[0].Revision == nil {
		t.Fatal("the receipt must report the revision it committed")
	}
	deletion.ExpectedRevision = *report.Receipt.Changes[0].Revision
	h.run(`DELETE FROM work.notes WHERE row_id = :row`, map[string]any{"row": rowID}, deletion)
	if after := h.run(`SHOW ROUTES FROM TABLE work.notes AT ROOT LIMIT 12`, nil, executor.MutationOptions{}); len(after.Rows) != 0 {
		t.Fatalf("the emptied branch must be pruned, still there: %v", after.Rows)
	}
}
