package executor_test

import (
	"context"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
)

func TestMutationOptionsAtomicallyCarryAgentIndexTerms(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	session, rows, closeSession := batchFixture(t, ctx)
	defer closeSession()
	indexTerm := "architecture'; DELETE FROM work.notes"
	envelope := session.Execute(ctx, executor.BatchRequest{
		RequestID: "agent-terms",
		Source: `
			INSERT INTO work.notes (title) VALUES ('first');
			UPDATE work.notes SET title = 'second' WHERE row_id = :row_id
		`,
		Statements: []executor.StatementInput{
			{Mutation: executor.MutationOptions{
				ExpectedSchemaVersion: 1, MaxAffectedRows: 1,
				IndexTerms: []string{"initial"},
			}},
			{
				Parameters: executor.Parameters{Named: map[string]any{"row_id": "row_first"}},
				Mutation: executor.MutationOptions{
					ExpectedSchemaVersion: 1, ExpectedRevision: 1, MaxAffectedRows: 1,
					IndexTerms: []string{indexTerm},
				},
			},
		},
	})
	assertStatuses(t, envelope, result.StatusSucceeded, result.StatusSucceeded)
	persisted, err := rows.Get(ctx, "work", "notes", "row_first")
	if err != nil {
		t.Fatal(err)
	}
	matches, err := rows.LookupAgentTerm(ctx, persisted.DatabaseID, indexTerm, 10)
	if err != nil || len(matches) != 1 || matches[0].Revision != 2 {
		t.Fatalf("parameter-like index term = %#v, %v", matches, err)
	}
	if old, err := rows.LookupAgentTerm(ctx, persisted.DatabaseID, "initial", 10); err != nil || len(old) != 0 {
		t.Fatalf("old index term = %#v, %v", old, err)
	}
}

func TestAgentIndexTermsRollBackWithExplicitBatch(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	session, rows, closeSession := batchFixture(t, ctx)
	defer closeSession()
	envelope := session.Execute(ctx, executor.BatchRequest{
		RequestID: "agent-index-rollback",
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
				IndexTerms: []string{"must disappear"},
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
	databases, err := rows.List(ctx, "work", "notes", 10)
	if err != nil || len(databases) != 0 {
		t.Fatalf("rows after rollback = %#v, %v", databases, err)
	}
	if matches, err := rows.LookupAgentTerm(
		ctx, "db_database", "must disappear", 10,
	); err != nil || len(matches) != 0 {
		t.Fatalf("postings after rollback = %#v, %v", matches, err)
	}
}
