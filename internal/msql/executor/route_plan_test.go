package executor_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/msql/parser"
	"github.com/HW-Yue/Memora/internal/routemutationplan"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/schemachangeplan"
	"github.com/HW-Yue/Memora/internal/security"
	nativekvstore "github.com/HW-Yue/Memora/internal/store/nativekv"
)

func TestPlanRouteMutationMSQLReturnsReviewPlanWithoutWriting(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := nativekvstore.Open(filepath.Join(t.TempDir(), "route-plan.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	dictionary := catalog.New(store, catalog.Options{IDs: &idSource{values: []string{"database", "table", "title"}}})
	database, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{Name: "work", Purpose: "Work", Scope: "Private"})
	if err != nil {
		t.Fatal(err)
	}
	table, err := dictionary.CreateTable(ctx, "work", catalog.TableDefinition{
		Name: "notes", Purpose: "Notes", RowSemantics: "One note",
		Columns: []catalog.ColumnDefinition{{Name: "title", Type: "TEXT(100)", Purpose: "Title"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	base := row.New(store, dictionary, row.Options{})
	rows := &planningRows{Service: base, nodes: []router.Node{
		{Version: router.Version, ID: "route_root", DatabaseID: database.ID, TableID: table.ID, Kind: router.KindRoot, Name: "root", Path: "/", Purpose: "Root", Revision: 1},
		{Version: router.Version, ID: "route_source", DatabaseID: database.ID, TableID: table.ID, ParentID: "route_root", Kind: router.KindLeaf, Name: "source", Path: "/source", Purpose: "Source", Revision: 2},
	}, locators: map[string][]router.Locator{"route_source": {
		{DatabaseID: database.ID, TableID: table.ID, RowID: "row_a", Revision: 1},
		{DatabaseID: database.ID, TableID: table.ID, RowID: "row_b", Revision: 1},
	}}}
	engine := executor.New(dictionary, rows)
	document, err := parser.Parse("PLAN ROUTE MUTATION FOR TABLE work.notes USING :proposal")
	if err != nil {
		t.Fatal(err)
	}
	proposal := routemutationplan.Proposal{
		Version: routemutationplan.ProposalVersion, ID: "proposal_1", Operation: routemutationplan.OperationSplit,
		Actor: "agent:test", SourceEventID: "event_1", Reason: "split",
		Sources: []routemutationplan.SourceRef{{RouteID: "route_source", ExpectedRevision: 2}},
		Targets: []routemutationplan.TargetProposal{
			{Key: "a", Name: "a", Purpose: "A", RowIDs: []string{"row_a"}},
			{Key: "b", Name: "b", Purpose: "B", RowIDs: []string{"row_b"}},
		},
	}
	output, err := engine.Execute(ctx, document.Statement,
		executor.Parameters{Named: map[string]any{"proposal": proposal}}, executor.MutationOptions{})
	if err != nil || len(output.Rows) != 1 || output.Rows[0]["status"] != "review_required" ||
		output.Rows[0]["plan_id"] == "" || output.AffectedRows != 0 {
		t.Fatalf("PLAN ROUTE output = %#v, %v", output, err)
	}
	if len(rows.nodes) != 2 || len(rows.locators["route_source"]) != 2 {
		t.Fatalf("planning mutated source = %#v %#v", rows.nodes, rows.locators)
	}
}

func TestApplyRouteMutationMSQLRequiresHashBoundApproval(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := nativekvstore.Open(filepath.Join(t.TempDir(), "route-apply.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	dictionary := catalog.New(store, catalog.Options{IDs: &idSource{values: []string{"database", "table", "title"}}})
	database, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{Name: "work", Purpose: "Work", Scope: "Private"})
	if err != nil {
		t.Fatal(err)
	}
	table, err := dictionary.CreateTable(ctx, "work", catalog.TableDefinition{
		Name: "notes", Purpose: "Notes", RowSemantics: "One note",
		Columns: []catalog.ColumnDefinition{{Name: "title", Type: "TEXT(100)", Purpose: "Title"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rows := &planningRows{Service: row.New(store, dictionary, row.Options{}), nodes: []router.Node{
		{Version: router.Version, ID: "route_root", DatabaseID: database.ID, TableID: table.ID, Kind: router.KindRoot, Name: "root", Path: "/", Purpose: "Root", Revision: 1},
		{Version: router.Version, ID: "route_source", DatabaseID: database.ID, TableID: table.ID, ParentID: "route_root", Kind: router.KindLeaf, Name: "source", Path: "/source", Purpose: "Source", Revision: 1},
	}, locators: map[string][]router.Locator{"route_source": {
		{DatabaseID: database.ID, TableID: table.ID, RowID: "row_a", Revision: 1},
		{DatabaseID: database.ID, TableID: table.ID, RowID: "row_b", Revision: 1},
	}}}
	plan, err := routemutationplan.Build(ctx, rows, routemutationplan.Scope{
		DatabaseID: database.ID, Database: "work", TableID: table.ID, Table: "notes",
	}, routemutationplan.Proposal{
		Version: routemutationplan.ProposalVersion, ID: "proposal_1", Operation: routemutationplan.OperationSplit,
		Actor: "agent:test", SourceEventID: "event_1", Reason: "split",
		Sources: []routemutationplan.SourceRef{{RouteID: "route_source", ExpectedRevision: 1}},
		Targets: []routemutationplan.TargetProposal{
			{Key: "a", Name: "a", Purpose: "A", RowIDs: []string{"row_a"}},
			{Key: "b", Name: "b", Purpose: "B", RowIDs: []string{"row_b"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	document, err := parser.Parse("APPLY ROUTE MUTATION PLAN :plan FOR TABLE work.notes")
	if err != nil {
		t.Fatal(err)
	}
	engine := executor.New(dictionary, rows)
	if _, err := engine.Execute(ctx, document.Statement, executor.Parameters{Named: map[string]any{"plan": plan}}, executor.MutationOptions{}); err == nil {
		t.Fatal("APPLY without approval unexpectedly succeeded")
	}
	ctx = security.WithAuthorization(ctx, security.Authorization{
		Version: security.AuthorizationVersion, Actor: "user:test", AuthorizedDatabases: []string{"work"},
		Approval: &security.Approval{Version: security.ApprovalVersion, Action: security.ActionApplyRouteMutation,
			SubjectSHA256: strings.TrimPrefix(plan.Hash, "sha256:"), Confirmed: true},
	})
	output, err := engine.Execute(ctx, document.Statement, executor.Parameters{Named: map[string]any{"plan": plan}}, executor.MutationOptions{})
	if err != nil || output.AffectedRows != 1 || !rows.applied || output.Rows[0]["verified"] != true {
		t.Fatalf("APPLY output = %#v, %v", output, err)
	}
}

type planningRows struct {
	*row.Service
	nodes         []router.Node
	locators      map[string][]router.Locator
	applied       bool
	schemaApplied bool
}

func (rows *planningRows) ApplySchemaChangePlan(_ context.Context, _, _ string, plan schemachangeplan.Plan) (schemachangeplan.Receipt, error) {
	rows.schemaApplied = true
	return schemachangeplan.Receipt{Version: schemachangeplan.ReceiptVersion, PlanID: plan.PlanID,
		PlanHash: plan.Hash, Status: "committed", ChangeSequence: 10,
		DatabaseRevision: plan.DatabaseRevision + 1, TableRevision: plan.TableRevision + 1,
		AppliedActions: len(plan.Actions), Verified: true}, nil
}

func (rows *planningRows) ListRouterNodes(context.Context) ([]router.Node, error) {
	return append([]router.Node(nil), rows.nodes...), nil
}

func (rows *planningRows) ListRouterLeafPage(_ context.Context, leafID, _ string, _ int) ([]router.Locator, router.ReadPage, error) {
	return append([]router.Locator(nil), rows.locators[leafID]...), router.ReadPage{}, nil
}

func (rows *planningRows) ApplyRouteMutationPlan(_ context.Context, database, table string, plan routemutationplan.Plan) (routemutationplan.Receipt, error) {
	rows.applied = true
	return routemutationplan.Receipt{Version: routemutationplan.ReceiptVersion, PlanID: plan.PlanID,
		PlanHash: plan.Hash, Operation: plan.Operation, Status: "committed", ChangeSequence: 9,
		CreatedNodes: len(plan.Creates), DeletedNodes: len(plan.Deletes), Verified: true}, nil
}
