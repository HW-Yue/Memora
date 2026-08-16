package nativerow

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/nativecatalog"
	"github.com/HW-Yue/Memora/internal/nativeconfig"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/router"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

// The thirteenth child under one branch must fail with the two executable
// remedies attached, never succeed and never silently paginate.
func TestCreateRouteRefusesThirteenthChildAndOffersBothRemedies(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	engine, file := newFanoutEngine(t)
	createFanoutTable(t, ctx, engine)
	for index := 1; index <= router.DefaultBranchFanout; index++ {
		createFanoutChild(t, ctx, engine, index)
	}
	_, err := runMSQL(ctx, engine,
		"CREATE ROUTE UNDER :parent NAME :name KIND 'branch' PURPOSE :purpose",
		executor.Parameters{Named: map[string]any{
			"parent": "route_root", "name": "child13", "purpose": "child 13 notes",
		}}, executor.MutationOptions{MaxAffectedRows: 1})
	overflow := assertBranchOverflow(t, err)
	if overflow.ParentRouteID != "route_root" ||
		overflow.LiveChildren != router.DefaultBranchFanout ||
		overflow.BranchFanout != router.DefaultBranchFanout {
		t.Fatalf("overflow = %#v", overflow)
	}
	details := overflow.ErrorDetails()
	if details["reason"] != "route_branch_fanout_exceeded" {
		t.Fatalf("details = %#v", details)
	}
	remedies, ok := details["remedies"].([]any)
	if !ok || len(remedies) != 2 {
		t.Fatalf("remedies = %#v", details["remedies"])
	}
	kinds := map[string]string{}
	for _, value := range remedies {
		remedy, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("remedy = %#v", value)
		}
		kind, _ := remedy["kind"].(string)
		statement, _ := remedy["statement"].(string)
		kinds[kind] = statement
	}
	if kinds["restructure_subtree"] == "" ||
		kinds["raise_branch_fanout"] != router.RaiseBranchFanoutStatement {
		t.Fatalf("remedy statements = %#v", kinds)
	}
	// The refused write leaves the tree untouched.
	children := executeMSQL(t, ctx, engine,
		"SHOW ROUTES FROM TABLE work.notes AT ROOT LIMIT 100", executor.Parameters{}, executor.MutationOptions{})
	if len(children.Rows) != router.DefaultBranchFanout {
		t.Fatalf("children after refused write = %d", len(children.Rows))
	}
	_ = file
}

// Raising this Database's limit is the second remedy: it must actually unblock
// the write, and the new limit must be enforced in turn.
func TestRaisingBranchFanoutUnblocksTheWriteAndIsEnforcedInTurn(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	engine, file := newFanoutEngine(t)
	createFanoutTable(t, ctx, engine)
	for index := 1; index <= router.DefaultBranchFanout; index++ {
		createFanoutChild(t, ctx, engine, index)
	}
	configuration, err := nativeconfig.New(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := configuration.UpdatePolicy(
		nativeconfig.RoutePolicy{BranchFanout: 14}, 1, "agent:test", "domain needs a wider fan-out",
	); err != nil {
		t.Fatal(err)
	}
	for index := router.DefaultBranchFanout + 1; index <= 14; index++ {
		createFanoutChild(t, ctx, engine, index)
	}
	_, err = runMSQL(ctx, engine,
		"CREATE ROUTE UNDER :parent NAME :name KIND 'branch' PURPOSE :purpose",
		executor.Parameters{Named: map[string]any{
			"parent": "route_root", "name": "child15", "purpose": "child 15 notes",
		}}, executor.MutationOptions{MaxAffectedRows: 1})
	if overflow := assertBranchOverflow(t, err); overflow.BranchFanout != 14 || overflow.LiveChildren != 14 {
		t.Fatalf("overflow after raise = %#v", overflow)
	}
}

// Lowering the limit never invalidates an existing tree: reads keep working and
// the subtree stays maintainable, only growth is refused.
func TestLoweringBranchFanoutLeavesExistingChildrenReadableAndRemovable(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	engine, file := newFanoutEngine(t)
	createFanoutTable(t, ctx, engine)
	for index := 1; index <= 6; index++ {
		createFanoutChild(t, ctx, engine, index)
	}
	configuration, err := nativeconfig.New(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := configuration.UpdatePolicy(
		nativeconfig.RoutePolicy{BranchFanout: 3}, 1, "agent:test", "tighten grouping",
	); err != nil {
		t.Fatal(err)
	}
	children := executeMSQL(t, ctx, engine,
		"SHOW ROUTES FROM TABLE work.notes AT ROOT LIMIT 100", executor.Parameters{}, executor.MutationOptions{})
	if len(children.Rows) != 6 {
		t.Fatalf("children after lowering the limit = %d", len(children.Rows))
	}
	executeMSQL(t, ctx, engine, "DELETE ROUTE :route",
		executor.Parameters{Named: map[string]any{"route": "route_child1"}},
		executor.MutationOptions{MaxAffectedRows: 1, ExpectedRevision: 1})
	if _, err := runMSQL(ctx, engine,
		"CREATE ROUTE UNDER :parent NAME :name KIND 'branch' PURPOSE :purpose",
		executor.Parameters{Named: map[string]any{
			"parent": "route_root", "name": "child7", "purpose": "child 7 notes",
		}}, executor.MutationOptions{MaxAffectedRows: 1}); assertBranchOverflow(t, err).LiveChildren != 5 {
		t.Fatalf("overflow must count the five remaining live children: %v", err)
	}
}

func newFanoutEngine(t *testing.T) (*executor.Engine, *nativestore.File) {
	t.Helper()
	file, err := nativestore.Create(filepath.Join(t.TempDir(), "database.memora"), nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	now := time.Date(2026, 8, 16, 8, 0, 0, 0, time.UTC)
	names := []string{"database", "table", "title"}
	routeIDs := []string{"root"}
	for index := 1; index <= 20; index++ {
		routeIDs = append(routeIDs, fmt.Sprintf("child%d", index))
	}
	dictionary := nativecatalog.NewService(nativecatalog.New(file), nativecatalog.ServiceOptions{
		IDs: &testIDs{values: names}, Clock: testClock{value: now},
	})
	rows := NewService(New(file), dictionary, ServiceOptions{
		IDs: &testIDs{values: routeIDs}, Clock: testClock{value: now},
	})
	return executor.New(dictionary, rows), file
}

func createFanoutTable(t *testing.T, ctx context.Context, engine *executor.Engine) {
	t.Helper()
	executeMSQL(t, ctx, engine, "CREATE DATABASE work PURPOSE 'Work' SCOPE 'Projects'",
		executor.Parameters{}, executor.MutationOptions{})
	executeMSQL(t, ctx, engine,
		"CREATE TABLE work.notes PURPOSE 'Notes' ROW SEMANTICS 'One note' (title TEXT(40) NOT NULL PURPOSE 'Title')",
		executor.Parameters{}, executor.MutationOptions{})
	executeMSQL(t, ctx, engine, "CREATE ROUTE ROOT FOR TABLE work.notes PURPOSE 'Notes Router'",
		executor.Parameters{}, executor.MutationOptions{MaxAffectedRows: 1})
}

func createFanoutChild(t *testing.T, ctx context.Context, engine *executor.Engine, index int) {
	t.Helper()
	name := fmt.Sprintf("child%d", index)
	executeMSQL(t, ctx, engine,
		"CREATE ROUTE UNDER :parent NAME :name KIND 'branch' PURPOSE :purpose",
		executor.Parameters{Named: map[string]any{
			"parent": "route_root", "name": name, "purpose": name + " notes",
		}}, executor.MutationOptions{MaxAffectedRows: 1})
}

func assertBranchOverflow(t *testing.T, err error) *router.BranchOverflowError {
	t.Helper()
	var overflow *router.BranchOverflowError
	if !errors.As(err, &overflow) {
		t.Fatalf("error = %v, want Route branch overflow", err)
	}
	if overflow.StableCode() != string(result.CodeConstraint) {
		t.Fatalf("stable code = %s", overflow.StableCode())
	}
	return overflow
}
