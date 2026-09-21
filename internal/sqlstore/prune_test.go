package sqlstore_test

import (
	"context"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/security"
)

// A Route mutation is the one operation that can empty a branch today: lifting
// the last child out of it. The engine then clears the shell itself and keeps
// going while each removal empties its own parent — and it must not touch an
// empty leaf on the way, because that is where the next Row goes.
func TestRouteMutationMovePrunesEmptiedBranches(t *testing.T) {
	h := newHarness(t)
	h.run(`CREATE DATABASE work PURPOSE 'p' SCOPE 's'`, nil, executor.MutationOptions{})
	h.run(`CREATE TABLE work.notes PURPOSE 'p' ROW SEMANTICS 'r' (title TEXT NOT NULL PURPOSE 'title' ROLE title)`, nil, executor.MutationOptions{})
	root := text(h.run(`CREATE ROUTE ROOT FOR TABLE work.notes PURPOSE 'root'`, nil, write("root")).Rows[0]["route_id"])
	outer := text(h.run(`CREATE ROUTE UNDER :p NAME 'outer' KIND 'branch' PURPOSE 'Outer grouping'`,
		map[string]any{"p": root}, write("outer")).Rows[0]["route_id"])
	inner := text(h.run(`CREATE ROUTE UNDER :p NAME 'inner' KIND 'branch' PURPOSE 'Inner grouping'`,
		map[string]any{"p": outer}, write("inner")).Rows[0]["route_id"])
	leaf := text(h.run(`CREATE ROUTE UNDER :p NAME 'leaf' KIND 'leaf' PURPOSE 'Holds one Row'`,
		map[string]any{"p": inner}, write("leaf")).Rows[0]["route_id"])

	proposal := map[string]any{
		"version": "memora.route-mutation-proposal/v1", "proposal_id": "route-proposal-prune",
		"operation": "MOVE", "actor": "agent:test", "source_event_id": "test:prune",
		"reason":           "lift the leaf up two levels",
		"sources":          []any{map[string]any{"route_id": leaf, "expected_revision": 1}},
		"target_parent_id": root,
	}
	planned := h.run(`PLAN ROUTE MUTATION FOR TABLE work.notes USING :proposal`,
		map[string]any{"proposal": proposal}, executor.MutationOptions{})
	if status := text(planned.Rows[0]["status"]); status != "review_required" {
		t.Fatalf("plan status = %q", status)
	}

	apply := write("apply the reviewed plan")
	plan := planned.Rows[0]["route_mutation_plan"]
	h.runAuthorized(approvedFor(text(planned.Rows[0]["plan_hash"])),
		`APPLY ROUTE MUTATION PLAN :plan FOR TABLE work.notes`, map[string]any{"plan": plan}, apply)

	// The leaf is now the root's only child: both emptied branches are gone, and
	// the leaf stays even though it holds no Row yet.
	children := h.run(`SHOW ROUTES UNDER :p LIMIT 12`, map[string]any{"p": root}, executor.MutationOptions{})
	if len(children.Rows) != 1 || text(children.Rows[0]["route_id"]) != leaf {
		t.Fatalf("root children after the move = %v", children.Rows)
	}
	for name, node := range map[string]string{"outer": outer, "inner": inner} {
		if code := h.fails(`DESCRIBE ROUTE :r`, map[string]any{"r": node}, executor.MutationOptions{}); code != result.CodeNotFound {
			t.Fatalf("%s branch: code = %s, want it physically gone", name, code)
		}
	}
}

// approvedFor binds the generic approval to the exact plan hash the review saw.
func approvedFor(planHash string) security.Authorization {
	authorization := authorization
	authorization.Approval = &security.Approval{
		Version: security.ApprovalVersion, Action: security.ActionApplyRouteMutation,
		SubjectSHA256: strings.TrimPrefix(planHash, "sha256:"), Confirmed: true,
	}
	return authorization
}

// runAuthorized is run() with the authorization the request has to carry; only
// the plan apply needs an approval, so the shared harness stays unapproved.
func (h *harness) runAuthorized(auth security.Authorization, source string, named map[string]any, mutation executor.MutationOptions) result.StatementResult {
	h.t.Helper()
	h.seq++
	envelope := h.session.ExecuteBatch(context.Background(), executor.BatchRequest{
		RequestID: "a" + strings.Repeat("z", h.seq),
		Source:    source,
		Statements: []executor.StatementInput{{
			Parameters: executor.Parameters{Named: named}, Mutation: mutation, Authorization: auth,
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
