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
	leafResult := executeTraceMSQL(t, dataDir,
		"CREATE ROUTE UNDER :parent NAME :name KIND :kind PURPOSE :purpose",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{
				"parent": rootID, "name": "source", "kind": "leaf", "purpose": "Source",
			}},
			Mutation: executor.MutationOptions{MaxAffectedRows: 1},
		}},
	)
	leafID, _ := leafResult.Results[0].Rows[0]["route_id"].(string)
	rowIDs := []string{}
	for _, title := range []string{"first", "second"} {
		inserted := executeTraceMSQL(t, dataDir,
			"INSERT INTO work.notes (title) VALUES (:title)",
			[]executor.StatementInput{{
				Parameters: executor.Parameters{Named: map[string]any{"title": title}},
				Mutation: executor.MutationOptions{
					ExpectedSchemaVersion: 1, MaxAffectedRows: 1, Actor: "agent:test", Source: "event_1",
					Reason: "route plan fixture", RouteLeafIDs: []string{leafID},
				},
			}},
		)
		rowID, _ := inserted.Results[0].Rows[0]["row_id"].(string)
		rowIDs = append(rowIDs, rowID)
	}
	proposal := routemutationplan.Proposal{
		Version: routemutationplan.ProposalVersion, ID: "proposal_native", Operation: routemutationplan.OperationSplit,
		Actor: "agent:test", SourceEventID: "event_1", Reason: "split source leaf",
		Sources: []routemutationplan.SourceRef{{RouteID: leafID, ExpectedRevision: 1}},
		Targets: []routemutationplan.TargetProposal{
			{Key: "first", Name: "first", Purpose: "First", RowIDs: []string{rowIDs[0]}},
			{Key: "second", Name: "second", Purpose: "Second", RowIDs: []string{rowIDs[1]}},
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
	opened := executeTraceMSQL(t, dataDir, "OPEN ROUTE :leaf LIMIT 10", []executor.StatementInput{{
		Parameters: executor.Parameters{Named: map[string]any{"leaf": leafID}},
	}})
	if len(opened.Results[0].Rows) != 2 {
		t.Fatalf("PLAN mutated source leaf = %#v", opened.Results[0])
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
		target := executeTraceMSQL(t, dataDir, "OPEN ROUTE :leaf LIMIT 10", []executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"leaf": create.RouteID}},
		}})
		if len(target.Results[0].Rows) != 1 {
			t.Fatalf("applied target %s = %#v", create.RouteID, target.Results[0])
		}
	}
}
