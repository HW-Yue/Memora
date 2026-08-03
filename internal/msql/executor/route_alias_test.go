package executor_test

import (
	"context"
	"reflect"
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
	}
	if envelope.Results[0].Revision == nil || *envelope.Results[0].Revision != 2 ||
		envelope.Results[1].Rows[0]["revision"] != uint64(2) {
		t.Fatalf("Route revision result = %#v", envelope.Results)
	}
}
