package executor_test

import (
	"context"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/row"
)

func TestMutationOptionsCarryCompleteRouterMembershipSnapshot(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	session, rows, closeSession := batchFixture(t, ctx)
	defer closeSession()
	leaf := createRouteLeaf(t, ctx, rows)

	envelope := session.Execute(ctx, executor.BatchRequest{
		RequestID: "router-memberships",
		Source: `
			INSERT INTO work.notes (title) VALUES ('routed');
			UPDATE work.notes SET title = 'unrouted' WHERE row_id = :row
		`,
		Statements: []executor.StatementInput{
			{Mutation: executor.MutationOptions{
				ExpectedSchemaVersion: 1, MaxAffectedRows: 1,
				RouteLeafIDs: []string{leaf.ID},
			}},
			{
				Parameters: executor.Parameters{Named: map[string]any{"row": "row_first"}},
				Mutation: executor.MutationOptions{
					ExpectedSchemaVersion: 1, ExpectedRevision: 1, MaxAffectedRows: 1,
					RouteLeafIDs: []string{},
				},
			},
		},
	})
	assertStatuses(t, envelope, result.StatusSucceeded, result.StatusSucceeded)

	persisted, err := rows.Get(ctx, "work", "notes", "row_first")
	if err != nil {
		t.Fatal(err)
	}
	memberships, err := rows.RouterMemberships(ctx, persisted)
	if err != nil || len(memberships) != 0 {
		t.Fatalf("explicit empty Router snapshot = %#v, %v", memberships, err)
	}
}

func TestRouterMembershipsRollBackWithExplicitBatch(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	session, rows, closeSession := batchFixture(t, ctx)
	defer closeSession()
	leaf := createRouteLeaf(t, ctx, rows)

	envelope := session.Execute(ctx, executor.BatchRequest{
		RequestID: "router-membership-rollback",
		Source: `
			BEGIN;
			INSERT INTO work.notes (title) VALUES ('rolled back');
			INSERT INTO work.notes (title, count) VALUES ('invalid', 'not an integer');
			COMMIT
		`,
		Statements: []executor.StatementInput{
			{},
			{Mutation: executor.MutationOptions{
				ExpectedSchemaVersion: 1, MaxAffectedRows: 1,
				RouteLeafIDs: []string{leaf.ID},
			}},
			{Mutation: executor.MutationOptions{
				ExpectedSchemaVersion: 1, MaxAffectedRows: 1,
			}},
			{},
		},
	})
	assertStatuses(t, envelope,
		result.StatusRolledBack, result.StatusRolledBack,
		result.StatusFailed, result.StatusSkipped,
	)

	persisted, err := rows.List(ctx, "work", "notes", 10)
	if err != nil || len(persisted) != 0 {
		t.Fatalf("rows after rollback = %#v, %v", persisted, err)
	}
	memberships, err := rows.RouterMemberships(ctx, row.Row{
		ID: "row_first", DatabaseID: "db_database", TableID: "tbl_table",
	})
	if err != nil || len(memberships) != 0 {
		t.Fatalf("Router memberships after rollback = %#v, %v", memberships, err)
	}
}

func createRouteLeaf(
	t *testing.T,
	ctx context.Context,
	rows *row.Service,
) router.Node {
	t.Helper()
	transaction, err := rows.BeginTransaction(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = transaction.Rollback() }()
	root, err := transaction.CreateRouterRoot(ctx, "db_database", "Work Router")
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := transaction.CreateRouterNode(ctx, root.ID, router.NodeDefinition{
		Name: "notes", Kind: router.KindLeaf, Purpose: "Routed notes",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	return leaf
}
