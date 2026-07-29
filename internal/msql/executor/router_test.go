package executor_test

import (
	"context"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
)

func TestMSQLRouterManagementTraversalAndLocatorOnlyOpen(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	session, rows, closeSession := batchFixture(t, ctx)
	defer closeSession()
	injectionLikePurpose := "notes'; DELETE ROUTE route_root --"
	setup := session.Execute(ctx, executor.BatchRequest{
		RequestID: "router-setup",
		Source: `
			CREATE ROUTE ROOT FOR DATABASE :database PURPOSE :purpose;
			CREATE ROUTE UNDER :root NAME :name KIND :kind PURPOSE :purpose;
			CREATE ROUTE UNDER :root NAME :name KIND :kind PURPOSE :purpose;
			INSERT INTO work.notes (title) VALUES ('routed')
		`,
		Statements: []executor.StatementInput{
			{
				Parameters: executor.Parameters{Named: map[string]any{
					"database": "work", "purpose": "Work Router",
				}},
				Mutation: executor.MutationOptions{MaxAffectedRows: 1},
			},
			{
				Parameters: executor.Parameters{Named: map[string]any{
					"root": "route_root", "name": "notes",
					"kind": "leaf", "purpose": injectionLikePurpose,
				}},
				Mutation: executor.MutationOptions{MaxAffectedRows: 1},
			},
			{
				Parameters: executor.Parameters{Named: map[string]any{
					"root": "route_root", "name": "projects",
					"kind": "branch", "purpose": "Project scopes",
				}},
				Mutation: executor.MutationOptions{MaxAffectedRows: 1},
			},
			{Mutation: executor.MutationOptions{
				ExpectedSchemaVersion: 1, MaxAffectedRows: 1,
				RouteLeafIDs: []string{"route_first"},
			}},
		},
	})
	assertStatuses(t, setup,
		result.StatusSucceeded, result.StatusSucceeded,
		result.StatusSucceeded, result.StatusSucceeded,
	)
	if len(setup.Results[0].Rows) != 1 ||
		setup.Results[0].Rows[0]["route_id"] != "route_root" {
		t.Fatalf("CREATE ROUTE root result = %#v", setup.Results[0])
	}
	root, err := rows.GetRouterNode(ctx, "route_root")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rows.ResolveRouterPath(ctx, root.DatabaseID, "/"); err != nil {
		t.Fatalf("ResolveRouterPath(%q, /) error = %v", root.DatabaseID, err)
	}

	firstPage := session.Execute(ctx, executor.BatchRequest{
		RequestID: "router-first-page",
		Source:    "SHOW ROUTES FROM DATABASE :database AT :path CURSOR :cursor LIMIT :limit",
		Statements: []executor.StatementInput{{Parameters: executor.Parameters{
			Named: map[string]any{
				"database": "work", "path": "/", "cursor": "", "limit": int64(1),
			},
		}}},
	})
	assertStatuses(t, firstPage, result.StatusSucceeded)
	firstResult := firstPage.Results[0]
	if len(firstResult.Rows) != 1 || firstResult.Rows[0]["route_id"] != "route_first" ||
		firstResult.Rows[0]["purpose"] != injectionLikePurpose ||
		!firstResult.Truncated || firstResult.NextCursor == "" ||
		firstPage.NextCursor != firstResult.NextCursor {
		t.Fatalf("first Router page = %#v", firstPage)
	}

	secondPage := session.Execute(ctx, executor.BatchRequest{
		RequestID: "router-second-page",
		Source:    "SHOW ROUTES FROM DATABASE :database AT :path CURSOR :cursor LIMIT :limit",
		Statements: []executor.StatementInput{{Parameters: executor.Parameters{
			Named: map[string]any{
				"database": "work", "path": "/",
				"cursor": firstResult.NextCursor, "limit": int64(1),
			},
		}}},
	})
	assertStatuses(t, secondPage, result.StatusSucceeded)
	if len(secondPage.Results[0].Rows) != 1 ||
		secondPage.Results[0].Rows[0]["route_id"] != "route_second" ||
		secondPage.Results[0].Truncated || secondPage.Results[0].NextCursor != "" {
		t.Fatalf("second Router page = %#v", secondPage)
	}

	opened := session.Execute(ctx, executor.BatchRequest{
		RequestID: "router-open",
		Source:    "OPEN ROUTE FROM DATABASE :database AT :path LIMIT :limit",
		Statements: []executor.StatementInput{{Parameters: executor.Parameters{
			Named: map[string]any{
				"database": "work", "path": "/notes", "limit": int64(10),
			},
		}}},
	})
	assertStatuses(t, opened, result.StatusSucceeded)
	locators := opened.Results[0].Rows
	if len(locators) != 1 || locators[0]["row_id"] != "row_first" ||
		locators[0]["revision"] != uint64(1) || len(locators[0]) != 4 {
		t.Fatalf("OPEN ROUTE locators = %#v", locators)
	}

	renamed := session.Execute(ctx, executor.BatchRequest{
		RequestID: "router-rename",
		Source:    "ALTER ROUTE :route RENAME TO :name",
		Statements: []executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{
				"route": "route_first", "name": "knowledge",
			}},
			Mutation: executor.MutationOptions{
				ExpectedRevision: 1, MaxAffectedRows: 1,
			},
		}},
	})
	assertStatuses(t, renamed, result.StatusSucceeded)
	if renamed.Results[0].Revision == nil || *renamed.Results[0].Revision != 2 {
		t.Fatalf("rename result = %#v", renamed.Results[0])
	}
	node, err := rows.GetRouterNode(ctx, "route_first")
	if err != nil || node.ID != "route_first" || node.Name != "knowledge" {
		t.Fatalf("renamed Router node = %#v, %v", node, err)
	}

	deleted := session.Execute(ctx, executor.BatchRequest{
		RequestID: "router-delete",
		Source:    "DELETE ROUTE :route",
		Statements: []executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"route": "route_second"}},
			Mutation: executor.MutationOptions{
				ExpectedRevision: 1, MaxAffectedRows: 1,
			},
		}},
	})
	assertStatuses(t, deleted, result.StatusSucceeded)
	if deleted.Results[0].Revision == nil || *deleted.Results[0].Revision != 2 {
		t.Fatalf("delete result = %#v", deleted.Results[0])
	}
	if _, err := rows.GetRouterNode(ctx, "route_second"); err == nil {
		t.Fatal("deleted Router node remains visible")
	}
}

func TestMSQLRouterMutationsRollBackWithExplicitBatch(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	session, rows, closeSession := batchFixture(t, ctx)
	defer closeSession()
	createRouterRoot(t, ctx, session)

	envelope := session.Execute(ctx, executor.BatchRequest{
		RequestID: "router-rollback",
		Source: `
			BEGIN;
			CREATE ROUTE UNDER :root NAME :name KIND :kind PURPOSE :purpose;
			CREATE ROUTE UNDER :root NAME :name KIND :kind PURPOSE :purpose;
			COMMIT
		`,
		Statements: []executor.StatementInput{
			{},
			{
				Parameters: executor.Parameters{Named: map[string]any{
					"root": "route_root", "name": "temporary",
					"kind": "branch", "purpose": "Must roll back",
				}},
				Mutation: executor.MutationOptions{MaxAffectedRows: 1},
			},
			{
				Parameters: executor.Parameters{Named: map[string]any{
					"root": "route_root", "name": "invalid",
					"kind": "wrong", "purpose": "Invalid kind",
				}},
				Mutation: executor.MutationOptions{MaxAffectedRows: 1},
			},
			{},
		},
	})
	assertStatuses(t, envelope,
		result.StatusRolledBack, result.StatusRolledBack,
		result.StatusFailed, result.StatusSkipped,
	)
	if envelope.Results[2].Error == nil ||
		envelope.Results[2].Error.Code != result.CodeValidation {
		t.Fatalf("Router validation error = %#v", envelope.Results[2])
	}
	if _, err := rows.GetRouterNode(ctx, "route_first"); err == nil {
		t.Fatal("rolled-back Router node remains visible")
	}
	children, _, err := rows.ListRouterChildren(ctx, "route_root", "", 10)
	if err != nil || len(children) != 0 {
		t.Fatalf("Router children after rollback = %#v, %v", children, err)
	}
}

func createRouterRoot(
	t *testing.T,
	ctx context.Context,
	session *executor.BatchSession,
) {
	t.Helper()
	envelope := session.Execute(ctx, executor.BatchRequest{
		RequestID: "create-router-root",
		Source:    "CREATE ROUTE ROOT FOR DATABASE :database PURPOSE :purpose",
		Statements: []executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{
				"database": "work", "purpose": "Work Router",
			}},
			Mutation: executor.MutationOptions{MaxAffectedRows: 1},
		}},
	})
	assertStatuses(t, envelope, result.StatusSucceeded)
}
