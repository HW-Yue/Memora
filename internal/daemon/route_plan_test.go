package daemon

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/routemutationplan"
	"github.com/HW-Yue/Memora/internal/security"
)

func TestNativeDaemonPlansAndAppliesApprovedRouteSplit(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "instance")
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan State, 1)
	done := make(chan error, 1)
	go func() { done <- Run(ctx, dataDir, ready) }()
	<-ready
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()

	executeTraceMSQL(t, dataDir, "CREATE DATABASE work PURPOSE 'Work' SCOPE 'Private'", nil)
	executeTraceMSQL(t, dataDir,
		"CREATE TABLE work.notes PURPOSE 'Notes' ROW SEMANTICS 'One note' (title TEXT(100) NOT NULL PURPOSE 'Title')", nil,
	)
	rootResult := executeTraceMSQL(t, dataDir,
		"CREATE ROUTE ROOT FOR TABLE work.notes PURPOSE :purpose",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"purpose": "Notes root"}},
			Mutation:   executor.MutationOptions{MaxAffectedRows: 1},
		}},
	)
	rootID, _ := rootResult.Results[0].Rows[0]["route_id"].(string)
	branchResult := executeTraceMSQL(t, dataDir,
		"CREATE ROUTE UNDER :parent NAME :name KIND :kind PURPOSE :purpose",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{
				"parent": rootID, "name": "source", "kind": "branch", "purpose": "Source",
			}},
			Mutation: executor.MutationOptions{MaxAffectedRows: 1},
		}},
	)
	branchID, _ := branchResult.Results[0].Rows[0]["route_id"].(string)
	leafIDs := []string{}
	for _, title := range []string{"first", "second"} {
		leafResult := executeTraceMSQL(t, dataDir,
			"CREATE ROUTE UNDER :parent NAME :name KIND :kind PURPOSE :purpose",
			[]executor.StatementInput{{
				Parameters: executor.Parameters{Named: map[string]any{
					"parent": branchID, "name": title, "kind": "leaf", "purpose": title + " Row",
				}},
				Mutation: executor.MutationOptions{MaxAffectedRows: 1},
			}},
		)
		leafID, _ := leafResult.Results[0].Rows[0]["route_id"].(string)
		leafIDs = append(leafIDs, leafID)
		executeTraceMSQL(t, dataDir,
			"INSERT INTO work.notes (title) VALUES (:title)",
			[]executor.StatementInput{{
				Parameters: executor.Parameters{Named: map[string]any{"title": title}},
				Mutation: executor.MutationOptions{
					ExpectedSchemaVersion: 1, MaxAffectedRows: 1, Actor: "agent:test", Source: "event_1",
					Reason: "route plan fixture", RouteLeafIDs: []string{leafID},
				},
			}},
		)
	}
	proposal := routemutationplan.Proposal{
		Version: routemutationplan.ProposalVersion, ID: "proposal_native", Operation: routemutationplan.OperationSplit,
		Actor: "agent:test", SourceEventID: "event_1", Reason: "split source branch",
		Sources: []routemutationplan.SourceRef{{RouteID: branchID, ExpectedRevision: 1}},
		Targets: []routemutationplan.TargetProposal{
			{Key: "first", Name: "first-group", Purpose: "First", ChildRouteIDs: []string{leafIDs[0]}},
			{Key: "second", Name: "second-group", Purpose: "Second", ChildRouteIDs: []string{leafIDs[1]}},
		},
	}
	planned := executeTraceMSQL(t, dataDir,
		"PLAN ROUTE MUTATION FOR TABLE work.notes USING :proposal",
		[]executor.StatementInput{{Parameters: executor.Parameters{Named: map[string]any{"proposal": proposal}}}},
	)
	if len(planned.Results[0].Rows) != 1 || planned.Results[0].Rows[0]["status"] != "review_required" ||
		planned.Results[0].AffectedRows != 0 {
		t.Fatalf("native Route plan = %#v", planned.Results[0])
	}
	children := executeTraceMSQL(t, dataDir, "SHOW ROUTES UNDER :branch LIMIT 12", []executor.StatementInput{{
		Parameters: executor.Parameters{Named: map[string]any{"branch": branchID}},
	}})
	if len(children.Results[0].Rows) != 2 {
		t.Fatalf("PLAN mutated source branch = %#v", children.Results[0])
	}
	encodedPlan, err := json.Marshal(planned.Results[0].Rows[0]["route_mutation_plan"])
	if err != nil {
		t.Fatal(err)
	}
	var plan routemutationplan.Plan
	if err := json.Unmarshal(encodedPlan, &plan); err != nil {
		t.Fatal(err)
	}
	applied := executeTraceMSQL(t, dataDir,
		"APPLY ROUTE MUTATION PLAN :plan FOR TABLE work.notes",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"plan": plan}},
			Authorization: security.Authorization{
				Version: security.AuthorizationVersion, Actor: "user:test", AuthorizedDatabases: []string{"work"}, DefaultLevel: security.LevelStructural,
				Approval: &security.Approval{Version: security.ApprovalVersion,
					Action:        security.ActionApplyRouteMutation,
					SubjectSHA256: strings.TrimPrefix(plan.Hash, "sha256:"), Confirmed: true},
			},
		}},
	)
	if applied.Results[0].AffectedRows != 1 || applied.Results[0].Rows[0]["verified"] != true {
		t.Fatalf("native Route apply = %#v", applied.Results[0])
	}
	for _, create := range plan.Creates {
		target := executeTraceMSQL(t, dataDir, "SHOW ROUTES UNDER :branch LIMIT 12", []executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"branch": create.RouteID}},
		}})
		if len(target.Results[0].Rows) != 1 {
			t.Fatalf("applied target %s = %#v", create.RouteID, target.Results[0])
		}
	}
}
