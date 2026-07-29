package executor_test

import (
	"context"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
)

func TestExecuteMatchReturnsOnlyFusedLocators(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	subject, rows, closeStore := queryFixture(t, ctx)
	defer closeStore()
	semantic, err := rows.Insert(ctx, "work", "notes", map[string]any{
		"title": "unrelated wording",
	}, row.WriteOptions{ExpectedSchemaVersion: 1, IndexTerms: []string{"architecture"}})
	if err != nil {
		t.Fatal(err)
	}
	literal := insertQueryRow(t, ctx, rows, map[string]any{"title": "architecture literal"})

	output, err := execute(ctx, subject,
		"MATCH work.notes QUERY :query TERMS :terms LIMIT :limit",
		executor.Parameters{Named: map[string]any{
			"query": "architecture", "terms": []any{"architecture"},
			"limit": int64(10),
		}}, executor.MutationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Rows) != 2 || output.Rows[0]["row_id"] != semantic.ID ||
		output.Rows[1]["row_id"] != literal.ID || output.Rows[0]["score"] != 0.8 {
		t.Fatalf("MATCH output = %#v", output)
	}
	if _, leaked := output.Rows[0]["title"]; leaked {
		t.Fatalf("MATCH leaked Row content: %#v", output.Rows[0])
	}
}

func TestMatchReadFailureDoesNotStopLaterBatchMatch(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	session, rows, closeSession := batchFixture(t, ctx)
	defer closeSession()
	insertQueryRow(t, ctx, rows, map[string]any{"title": "architecture"})
	envelope := session.Execute(ctx, executor.BatchRequest{
		RequestID: "match-read-failure",
		Source: `
			MATCH work.notes QUERY :query TERMS :terms LIMIT 10;
			MATCH work.notes QUERY :query TERMS :terms LIMIT 10
		`,
		Statements: []executor.StatementInput{
			{Parameters: executor.Parameters{Named: map[string]any{
				"query": "architecture", "terms": "invalid",
			}}},
			{Parameters: executor.Parameters{Named: map[string]any{
				"query": "architecture", "terms": []any{},
			}}},
		},
	})
	assertStatuses(t, envelope, result.StatusFailed, result.StatusSucceeded)
	if len(envelope.Results[1].Rows) != 1 {
		t.Fatalf("later MATCH rows = %#v", envelope.Results[1].Rows)
	}
}

func TestExecuteMatchValidatesArrayAndLimitParameters(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	subject, _, closeStore := queryFixture(t, ctx)
	defer closeStore()
	for _, parameters := range []executor.Parameters{
		{Named: map[string]any{"query": "x", "terms": "not-array", "limit": int64(10)}},
		{Named: map[string]any{"query": "x", "terms": []any{int64(1)}, "limit": int64(10)}},
		{Named: map[string]any{"query": "x", "terms": []any{"x"}, "limit": int64(0)}},
	} {
		if _, err := execute(ctx, subject,
			"MATCH work.notes QUERY :query TERMS :terms LIMIT :limit",
			parameters, executor.MutationOptions{}); err == nil {
			t.Fatalf("MATCH parameters %#v succeeded", parameters)
		}
	}
}
