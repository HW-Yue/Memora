package skillconflict_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/skillconflict"
	"github.com/HW-Yue/Memora/internal/skillwrite"
)

func TestBuildPresentsSourcesRevisionsAndStableDiffsWithoutMutation(t *testing.T) {
	t.Parallel()

	view, err := skillconflict.Build(conflictRequest())
	if err != nil {
		t.Fatal(err)
	}
	if view.Version != skillconflict.ViewVersion || view.ID != "conflict-routing" ||
		len(view.Existing) != 2 || len(view.Differences) != 2 {
		t.Fatalf("view = %#v", view)
	}
	if view.Existing[0].RowID != "row_old" || view.Existing[0].Revision != 4 ||
		view.Existing[0].Source.SourceEventID != "source-old" {
		t.Fatalf("first existing alternative = %#v", view.Existing[0])
	}
	first := view.Differences[0]
	if first.RowID != "row_old" || len(first.Fields) != 3 ||
		first.Fields[0].Field != "legacy" || first.Fields[1].Field != "nullable" ||
		first.Fields[2].Field != "title" {
		t.Fatalf("stable field differences = %#v", first)
	}
	if first.Fields[0].Proposed.Present || !first.Fields[0].Existing.Present ||
		!first.Fields[1].Proposed.Present || first.Fields[1].Existing.Present {
		t.Fatalf("missing/null distinction = %#v", first.Fields)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{`"plan"`, `"msql"`, "candidate", "disputed"} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Fatalf("conflict view contains %q: %s", forbidden, encoded)
		}
	}
}

func TestBuildRejectsRowsWithoutAVisibleDifference(t *testing.T) {
	t.Parallel()

	request := conflictRequest()
	request.Existing = request.Existing[:1]
	request.Existing[0].Values = cloneValues(request.Proposal.Values)
	if _, err := skillconflict.Build(request); err == nil {
		t.Fatal("Build accepted an identical Row as a semantic conflict")
	}
}

func TestResolveRequiresUserInstructionAndBindsEachActionToF31Decision(t *testing.T) {
	t.Parallel()

	view, err := skillconflict.Build(conflictRequest())
	if err != nil {
		t.Fatal(err)
	}
	withoutInstruction := resolution(skillconflict.ActionRewrite, revisePlan("row_old", 4))
	withoutInstruction.Instruction = ""
	if plan, err := skillconflict.Resolve(view, withoutInstruction); err == nil || plan.Version != "" {
		t.Fatalf("resolution without user instruction returned plan %#v, %v", plan, err)
	}

	tests := []struct {
		name       string
		action     skillconflict.Action
		keepRow    string
		removeRows []string
		plan       skillwrite.Plan
		decision   skillwrite.Decision
	}{
		{
			name: "retain existing", action: skillconflict.ActionRetain,
			keepRow: "row_old", plan: ignorePlan(), decision: skillwrite.DecisionIgnore,
		},
		{
			name: "rewrite existing", action: skillconflict.ActionRewrite,
			keepRow: "row_old", plan: revisePlan("row_old", 4), decision: skillwrite.DecisionRevise,
		},
		{
			name: "remove contradicted Row through merge", action: skillconflict.ActionRemove,
			keepRow: "row_old", removeRows: []string{"row_other"},
			plan: mergePlan("row_old", 4, "row_other", 2), decision: skillwrite.DecisionMerge,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			input := resolution(test.action, test.plan)
			input.KeepRowID = test.keepRow
			input.RemoveRowIDs = test.removeRows
			resolved, err := skillconflict.Resolve(view, input)
			if err != nil || resolved.Decision != test.decision || resolved.ID != test.plan.ID {
				t.Fatalf("resolved plan = %#v, %v", resolved, err)
			}
		})
	}
}

func TestResolveRejectsStaleUnshownAndAuthorizationExpandingPlans(t *testing.T) {
	t.Parallel()

	view, err := skillconflict.Build(conflictRequest())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		change func(*skillconflict.Resolution)
	}{
		{
			name: "stale revision",
			change: func(value *skillconflict.Resolution) {
				value.Plan.Steps[0].Input.Mutation.ExpectedRevision = 3
			},
		},
		{
			name: "unshown target",
			change: func(value *skillconflict.Resolution) {
				value.Plan.Steps[0].Target = "row_unshown"
			},
		},
		{
			name: "unshown SQL target",
			change: func(value *skillconflict.Resolution) {
				value.Plan.Steps[0].Input.Parameters.Named["row"] = "row_unshown"
			},
		},
		{
			name: "different SQL table",
			change: func(value *skillconflict.Resolution) {
				value.Plan.Steps[0].MSQL = "UPDATE work.other SET title = :title WHERE row_id = :row"
			},
		},
		{
			name: "expanded resolution authorization",
			change: func(value *skillconflict.Resolution) {
				value.AuthorizedDatabases = append(value.AuthorizedDatabases, "personal")
			},
		},
		{
			name: "expanded plan authorization",
			change: func(value *skillconflict.Resolution) {
				value.Plan.AuthorizedDatabases = append(value.Plan.AuthorizedDatabases, "personal")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			input := resolution(skillconflict.ActionRewrite, revisePlan("row_old", 4))
			test.change(&input)
			if _, err := skillconflict.Resolve(view, input); err == nil {
				t.Fatal("Resolve accepted a plan detached from the conflict view")
			}
		})
	}
}

func conflictRequest() skillconflict.Request {
	return skillconflict.Request{
		ID: "conflict-routing", Database: "work", Table: "notes",
		AuthorizedDatabases: []string{"work"},
		Proposal: skillconflict.Proposal{
			Values: map[string]any{
				"title": "Route results are locators only", "nullable": nil,
			},
			Source: skillconflict.Provenance{
				Actor: "agent:host", SourceEventID: "proposal-new", Reason: "new verified conclusion",
			},
		},
		Existing: []skillconflict.RowSnapshot{
			{
				RowID: "row_old", Revision: 4,
				Values: map[string]any{"title": "Route results can answer directly", "legacy": nil},
				Source: skillconflict.Provenance{
					Actor: "agent:old", SourceEventID: "source-old", Reason: "old assumption",
				},
			},
			{
				RowID: "row_other", Revision: 2,
				Values: map[string]any{"title": "Route output is final evidence"},
				Source: skillconflict.Provenance{
					Actor: "agent:other", SourceEventID: "source-other", Reason: "imported note",
				},
			},
		},
	}
}

func resolution(action skillconflict.Action, plan skillwrite.Plan) skillconflict.Resolution {
	return skillconflict.Resolution{
		Version: skillconflict.ResolutionVersion, ConflictID: "conflict-routing", Action: action,
		Instruction: "Use the user-approved routing truth", Actor: "agent:host",
		SourceEventID: "user-decision-1", AuthorizedDatabases: []string{"work"},
		KeepRowID: "row_old", Plan: plan,
	}
}

func basePlan(id string, decision skillwrite.Decision) skillwrite.Plan {
	one := 1
	return skillwrite.Plan{
		Version: skillwrite.PlanVersion, ID: id, Decision: decision,
		Database: "work", Table: "notes", Actor: "agent:host",
		SourceEventID: "user-decision-1", Reason: "Use the user-approved routing truth",
		AuthorizedDatabases: []string{"work"},
		Preflight: []skillwrite.Check{{
			ID: "read-conflict", MSQL: "SELECT row_id, revision FROM work.notes WHERE row_id = :row LIMIT 1",
			Input:      executor.StatementInput{Parameters: executor.Parameters{Named: map[string]any{"row": "row_old"}}},
			ExpectRows: &one,
		}},
	}
}

func ignorePlan() skillwrite.Plan {
	return basePlan("resolve-retain", skillwrite.DecisionIgnore)
}

func revisePlan(rowID string, revision uint64) skillwrite.Plan {
	plan := basePlan("resolve-rewrite", skillwrite.DecisionRevise)
	plan.Steps = []skillwrite.Step{{
		ID: "rewrite", Kind: "UPDATE", Target: rowID,
		MSQL: "UPDATE work.notes SET title = :title WHERE row_id = :row",
		Input: executor.StatementInput{
			Parameters: executor.Parameters{Named: map[string]any{
				"title": "Route results are locators only", "row": rowID,
			}},
			Mutation: mutationOptions(revision),
		},
	}}
	plan.Verify = verifyCheck()
	return plan
}

func mergePlan(survivor string, survivorRevision uint64, removed string, removedRevision uint64) skillwrite.Plan {
	plan := basePlan("resolve-remove", skillwrite.DecisionMerge)
	plan.Steps = []skillwrite.Step{
		{
			ID: "retain", Kind: "UPDATE", Target: survivor,
			MSQL: "UPDATE work.notes SET title = :title WHERE row_id = :row",
			Input: executor.StatementInput{
				Parameters: executor.Parameters{Named: map[string]any{
					"title": "Route results are locators only", "row": survivor,
				}},
				Mutation: mutationOptions(survivorRevision),
			},
		},
		{
			ID: "remove", Kind: "DELETE", Target: removed,
			MSQL: "DELETE FROM work.notes WHERE row_id = :removed",
			Input: executor.StatementInput{
				Parameters: executor.Parameters{Named: map[string]any{"removed": removed}},
				Mutation: executor.MutationOptions{
					ExpectedSchemaVersion: 1, ExpectedRevision: removedRevision, MaxAffectedRows: 1,
					Actor: "agent:host", Source: "user-decision-1", Reason: "Use the user-approved routing truth",
				},
			},
		},
	}
	plan.Verify = verifyCheck()
	return plan
}

func mutationOptions(revision uint64) executor.MutationOptions {
	return executor.MutationOptions{
		ExpectedSchemaVersion: 1, ExpectedRevision: revision, MaxAffectedRows: 1,
		Actor: "agent:host", Source: "user-decision-1", Reason: "Use the user-approved routing truth",
		RouteLeafIDs: []string{},
	}
}

func verifyCheck() []skillwrite.Check {
	one := 1
	return []skillwrite.Check{{
		ID: "verify-resolution", MSQL: "SELECT row_id, revision FROM work.notes WHERE row_id = :row LIMIT 1",
		Input:      executor.StatementInput{Parameters: executor.Parameters{Named: map[string]any{"row": "row_old"}}},
		ExpectRows: &one,
	}}
}

func cloneValues(source map[string]any) map[string]any {
	cloned := make(map[string]any, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}
