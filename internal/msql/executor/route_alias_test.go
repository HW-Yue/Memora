package executor_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
)

func TestRouteAliasesRoundTripThroughVersionedMSQL(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	session, rows, closeSession := batchFixture(t, ctx)
	defer closeSession()
	leaf := createRouteLeaf(t, ctx, rows)

	envelope := session.Execute(ctx, executor.BatchRequest{
		RequestID: "route-alias-round-trip",
		Source: `
			ALTER ROUTE :route SET ALIASES :aliases;
			DESCRIBE ROUTE :route
		`,
		Statements: []executor.StatementInput{
			{
				Parameters: executor.Parameters{Named: map[string]any{
					"route": leaf.ID, "aliases": []any{"agent loop", "运行时"},
				}},
				Mutation: executor.MutationOptions{ExpectedRevision: 1, MaxAffectedRows: 1},
			},
			{Parameters: executor.Parameters{Named: map[string]any{"route": leaf.ID}}},
		},
	})
	assertStatuses(t, envelope, result.StatusSucceeded, result.StatusSucceeded)
	for index, statement := range envelope.Results {
		if len(statement.Rows) != 1 || !reflect.DeepEqual(statement.Rows[0]["aliases"], []string{"agent loop", "运行时"}) {
			t.Fatalf("statement %d alias result = %#v", index, statement)
		}
		foundAliasColumn := false
		for _, column := range statement.Columns {
			if column.Name == "aliases" && column.Type == "TEXT_LIST" && !column.Nullable {
				foundAliasColumn = true
			}
		}
		if !foundAliasColumn {
			t.Fatalf("statement %d alias column = %#v", index, statement.Columns)
		}
	}
	if envelope.Results[0].Revision == nil || *envelope.Results[0].Revision != 2 ||
		envelope.Results[1].Rows[0]["revision"] != uint64(2) {
		t.Fatalf("Route revision result = %#v", envelope.Results)
	}
}

func TestRouteAliasMSQLRollsBackWithExplicitTransaction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	session, rows, closeSession := batchFixture(t, ctx)
	defer closeSession()
	leaf := createRouteLeaf(t, ctx, rows)

	envelope := session.Execute(ctx, executor.BatchRequest{
		RequestID: "route-alias-rollback",
		Source: `
			BEGIN;
			ALTER ROUTE :route SET ALIASES :aliases;
			ROLLBACK
		`,
		Statements: []executor.StatementInput{
			{},
			{
				Parameters: executor.Parameters{Named: map[string]any{
					"route": leaf.ID, "aliases": []any{"rolled back"},
				}},
				Mutation: executor.MutationOptions{ExpectedRevision: 1, MaxAffectedRows: 1},
			},
			{},
		},
	})
	assertStatuses(t, envelope, result.StatusRolledBack, result.StatusRolledBack, result.StatusSucceeded)
	current, err := rows.GetRouterNode(ctx, leaf.ID)
	if err != nil || current.Revision != 1 || len(current.Aliases) != 0 {
		t.Fatalf("Route after alias rollback = %#v, %v", current, err)
	}
}

func TestRouteAliasMSQLRejectsWrongShapeAndSemanticBounds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tests := []struct {
		name    string
		aliases any
	}{
		{name: "scalar", aliases: "agent loop"},
		{name: "mixed", aliases: []any{"agent loop", int64(1)}},
		{name: "blank", aliases: []any{" "}},
		{name: "same-name", aliases: []any{"NOTES"}},
		{name: "duplicate", aliases: []any{"Agent", "agent"}},
		{name: "too-many", aliases: []any{"a", "b", "c", "d", "e", "f", "g", "h", "i"}},
		{name: "too-long", aliases: []any{strings.Repeat("a", 65)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session, rows, closeSession := batchFixture(t, ctx)
			defer closeSession()
			leaf := createRouteLeaf(t, ctx, rows)
			envelope := session.Execute(ctx, executor.BatchRequest{
				RequestID: "route-alias-invalid-" + test.name,
				Source:    "ALTER ROUTE :route SET ALIASES :aliases",
				Statements: []executor.StatementInput{{
					Parameters: executor.Parameters{Named: map[string]any{
						"route": leaf.ID, "aliases": test.aliases,
					}},
					Mutation: executor.MutationOptions{ExpectedRevision: 1, MaxAffectedRows: 1},
				}},
			})
			if len(envelope.Results) != 1 || envelope.Results[0].Status != result.StatusFailed ||
				envelope.Results[0].Error == nil || envelope.Results[0].Error.Code != result.CodeValidation {
				t.Fatalf("invalid aliases result = %#v", envelope)
			}
			current, err := rows.GetRouterNode(ctx, leaf.ID)
			if err != nil || current.Revision != 1 || len(current.Aliases) != 0 {
				t.Fatalf("invalid aliases mutated Route = %#v, %v", current, err)
			}
		})
	}
}

func TestRouteAliasMSQLClearsAliasesAndRejectsStaleRevision(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	session, rows, closeSession := batchFixture(t, ctx)
	defer closeSession()
	leaf := createRouteLeaf(t, ctx, rows)

	request := func(id string, aliases any, revision uint64) result.Envelope {
		return session.Execute(ctx, executor.BatchRequest{
			RequestID: id, Source: "ALTER ROUTE :route SET ALIASES :aliases",
			Statements: []executor.StatementInput{{
				Parameters: executor.Parameters{Named: map[string]any{"route": leaf.ID, "aliases": aliases}},
				Mutation:   executor.MutationOptions{ExpectedRevision: revision, MaxAffectedRows: 1},
			}},
		})
	}
	assertStatuses(t, request("route-alias-set", []string{"agent loop"}, 1), result.StatusSucceeded)
	stale := request("route-alias-stale", []string{"stale"}, 1)
	if stale.Results[0].Error == nil || stale.Results[0].Error.Code != result.CodeRevisionConflict {
		t.Fatalf("stale alias update = %#v", stale)
	}
	cleared := request("route-alias-clear", []any{}, 2)
	assertStatuses(t, cleared, result.StatusSucceeded)
	if got := cleared.Results[0].Rows[0]["aliases"]; !reflect.DeepEqual(got, []string{}) || got == nil {
		t.Fatalf("cleared aliases = %#v", got)
	}
}
