package nativemutation

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/nativecatalog"
	"github.com/HW-Yue/Memora/internal/nativeconfig"
	"github.com/HW-Yue/Memora/internal/nativerouter"
	"github.com/HW-Yue/Memora/internal/nativerow"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/router"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

// route_policy is its own configuration key with its own revision chain, and
// changing it must not disturb query_budgets.
func TestRoutePolicyMSQLIsDiscoverableVersionedAndRestorable(t *testing.T) {
	t.Parallel()
	session, cleanup := newRoutePolicySession(t)
	defer cleanup()
	response := session.Execute(context.Background(), executor.BatchRequest{
		RequestID: "route-policy",
		Source: "SHOW CONFIGURATION ROUTE_POLICY; " +
			"ALTER CONFIGURATION ROUTE_POLICY SET BRANCH_FANOUT 16; " +
			"SHOW CONFIGURATION ROUTE_POLICY; " +
			"RESTORE CONFIGURATION ROUTE_POLICY TO REVISION 1; " +
			"SHOW CONFIGURATION ROUTE_POLICY HISTORY LIMIT 10; " +
			"SHOW CONFIGURATION",
		Statements: []executor.StatementInput{
			{},
			{Mutation: executor.MutationOptions{
				ExpectedRevision: 1, Actor: "agent:test", Reason: "domain needs a wider fan-out",
			}},
			{},
			{Mutation: executor.MutationOptions{
				ExpectedRevision: 2, Actor: "agent:test", Reason: "restore the default grouping",
			}},
			{},
			{},
		},
	})
	if !response.OK || len(response.Results) != 6 {
		t.Fatalf("Route policy response = %#v", response)
	}
	for _, statement := range response.Results {
		if statement.Status != result.StatusSucceeded {
			t.Fatalf("Route policy statement = %#v", statement)
		}
	}
	if response.Results[0].Rows[0]["config_key"] != nativeconfig.RoutePolicyKey ||
		response.Results[0].Rows[0]["branch_fanout"] != router.DefaultBranchFanout ||
		response.Results[0].Rows[0]["revision"] != uint64(1) {
		t.Fatalf("initial Route policy = %#v", response.Results[0].Rows)
	}
	if response.Results[2].Rows[0]["branch_fanout"] != 16 ||
		response.Results[2].Rows[0]["revision"] != uint64(2) {
		t.Fatalf("raised Route policy = %#v", response.Results[2].Rows)
	}
	if len(response.Results[4].Rows) != 3 ||
		response.Results[4].Rows[0]["branch_fanout"] != router.DefaultBranchFanout ||
		response.Results[4].Rows[0]["restored_revision"] != uint64(1) {
		t.Fatalf("Route policy history = %#v", response.Results[4].Rows)
	}
	if response.Results[5].Rows[0]["config_key"] != nativeconfig.QueryBudgetsKey ||
		response.Results[5].Rows[0]["revision"] != uint64(1) ||
		response.Results[5].Rows[0]["route_children"] != 12 {
		t.Fatalf("query budgets must keep their own chain = %#v", response.Results[5].Rows)
	}
}

// The refused write must arrive as data: the Agent picks between the two
// remedies from the error envelope, without a human reading the message.
func TestBranchOverflowFailureCarriesBothRemediesInTheEnvelope(t *testing.T) {
	t.Parallel()
	session, cleanup := newRoutePolicySession(t)
	defer cleanup()
	ctx := context.Background()
	setup := session.Execute(ctx, executor.BatchRequest{
		RequestID: "route-policy-setup",
		Source: "CREATE DATABASE work PURPOSE 'Work' SCOPE 'Projects'; " +
			"CREATE TABLE work.notes PURPOSE 'Notes' ROW SEMANTICS 'One note' " +
			"(title TEXT(40) NOT NULL PURPOSE 'Title'); " +
			"CREATE ROUTE ROOT FOR TABLE work.notes PURPOSE 'Notes Router'",
		Statements: []executor.StatementInput{
			{}, {}, {Mutation: executor.MutationOptions{MaxAffectedRows: 1}},
		},
	})
	if !setup.OK {
		t.Fatalf("setup = %#v", setup)
	}
	root := setup.Results[2].Rows[0]["route_id"]
	for index := 1; index <= router.DefaultBranchFanout; index++ {
		created := session.Execute(ctx, executor.BatchRequest{
			RequestID: "route-policy-child",
			Source:    "CREATE ROUTE UNDER :parent NAME :name KIND 'branch' PURPOSE :purpose",
			Statements: []executor.StatementInput{{
				Parameters: executor.Parameters{Named: map[string]any{
					"parent": root, "name": childName(index), "purpose": childName(index) + " notes",
				}},
				Mutation: executor.MutationOptions{MaxAffectedRows: 1},
			}},
		})
		if !created.OK {
			t.Fatalf("child %d = %#v", index, created)
		}
	}
	refused := session.Execute(ctx, executor.BatchRequest{
		RequestID: "route-policy-overflow",
		Source:    "CREATE ROUTE UNDER :parent NAME :name KIND 'branch' PURPOSE :purpose",
		Statements: []executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{
				"parent": root, "name": "overflow", "purpose": "overflow notes",
			}},
			Mutation: executor.MutationOptions{MaxAffectedRows: 1},
		}},
	})
	if refused.OK || len(refused.Results) != 1 || refused.Results[0].Error == nil {
		t.Fatalf("overflow response = %#v", refused)
	}
	failure := refused.Results[0].Error
	if failure.Code != result.CodeConstraint {
		t.Fatalf("overflow code = %s", failure.Code)
	}
	if failure.Details["reason"] != "route_branch_fanout_exceeded" ||
		failure.Details["parent_route_id"] != root ||
		failure.Details["live_children"] != router.DefaultBranchFanout ||
		failure.Details["branch_fanout"] != router.DefaultBranchFanout {
		t.Fatalf("overflow details = %#v", failure.Details)
	}
	remedies, ok := failure.Details["remedies"].([]any)
	if !ok || len(remedies) != 2 {
		t.Fatalf("remedies = %#v", failure.Details["remedies"])
	}
	statements := map[string]string{}
	for _, value := range remedies {
		remedy, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("remedy = %#v", value)
		}
		kind, _ := remedy["kind"].(string)
		statement, _ := remedy["statement"].(string)
		statements[kind] = statement
	}
	if statements["raise_branch_fanout"] != router.RaiseBranchFanoutStatement ||
		statements["restructure_subtree"] == "" {
		t.Fatalf("remedy statements = %#v", statements)
	}
	// Remedy two, executed exactly as the envelope describes it.
	raised := session.Execute(ctx, executor.BatchRequest{
		RequestID: "route-policy-raise",
		Source:    "ALTER CONFIGURATION ROUTE_POLICY SET BRANCH_FANOUT :fanout",
		Statements: []executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"fanout": 13}},
			Mutation: executor.MutationOptions{
				ExpectedRevision: 1, Actor: "agent:test", Reason: "this domain needs one more group",
			},
		}},
	})
	if !raised.OK {
		t.Fatalf("raise = %#v", raised.Results[0])
	}
	accepted := session.Execute(ctx, executor.BatchRequest{
		RequestID: "route-policy-retry",
		Source:    "CREATE ROUTE UNDER :parent NAME :name KIND 'branch' PURPOSE :purpose",
		Statements: []executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{
				"parent": root, "name": "overflow", "purpose": "overflow notes",
			}},
			Mutation: executor.MutationOptions{MaxAffectedRows: 1},
		}},
	})
	if !accepted.OK {
		t.Fatalf("retry after raising the limit = %#v", accepted)
	}
}

func childName(index int) string {
	return "child" + string(rune('a'+index-1))
}

func newRoutePolicySession(t *testing.T) (*executor.BatchSession, func()) {
	t.Helper()
	file, err := nativestore.Create(filepath.Join(t.TempDir(), "database.memora"), nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	dictionary := nativecatalog.NewService(nativecatalog.New(file), nativecatalog.ServiceOptions{})
	rowRepository := nativerow.New(file)
	routeRepository := nativerouter.New(file)
	configuration, err := nativeconfig.New(file)
	if err != nil {
		t.Fatal(err)
	}
	rows := NewService(
		nativerow.NewService(rowRepository, dictionary, nativerow.ServiceOptions{}),
		dictionary, rowRepository, routeRepository, New(file, rowRepository, routeRepository),
		configuration,
	)
	session := executor.NewBatchSession(context.Background(), dictionary, rows)
	return session, func() {
		_ = session.Close()
		_ = file.Close()
	}
}
