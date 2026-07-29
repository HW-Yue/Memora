package executor_test

import (
	"context"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/reindex"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
)

func TestMSQLUpdateAtomicallyInvalidatesAndQueuesSemanticReindex(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	session, rows, closeSession := batchFixture(t, ctx)
	defer closeSession()
	inserted, err := rows.Insert(ctx, "work", "notes", map[string]any{
		"title": "Initial",
	}, row.WriteOptions{
		ExpectedSchemaVersion: 1, IndexTerms: []string{"initial"},
	})
	if err != nil {
		t.Fatal(err)
	}

	rolledBack := session.Execute(ctx, executor.BatchRequest{
		RequestID: "reindex-rollback",
		Source: `
			BEGIN;
			UPDATE work.notes SET title = 'temporary' WHERE row_id = :row;
			INSERT INTO work.notes (title, count) VALUES ('invalid', 'wrong');
			COMMIT
		`,
		Statements: []executor.StatementInput{
			{},
			{
				Parameters: executor.Parameters{Named: map[string]any{"row": inserted.ID}},
				Mutation: executor.MutationOptions{
					ExpectedSchemaVersion: 1, ExpectedRevision: 1, MaxAffectedRows: 1,
				},
			},
			{Mutation: executor.MutationOptions{
				ExpectedSchemaVersion: 1, MaxAffectedRows: 1,
			}},
			{},
		},
	})
	assertStatuses(t, rolledBack,
		result.StatusRolledBack, result.StatusRolledBack,
		result.StatusFailed, result.StatusSkipped,
	)
	if matches, err := rows.LookupAgentTerm(ctx, inserted.DatabaseID, "initial", 10); err != nil ||
		len(matches) != 1 || matches[0].Revision != 1 {
		t.Fatalf("Agent index after rollback = %#v, %v", matches, err)
	}
	if tasks, err := rows.ListReindexTasks(ctx); err != nil || len(tasks) != 0 {
		t.Fatalf("tasks after rollback = %#v, %v", tasks, err)
	}

	committed := session.Execute(ctx, executor.BatchRequest{
		RequestID: "reindex-commit",
		Source:    "UPDATE work.notes SET title = 'committed' WHERE row_id = :row",
		Statements: []executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"row": inserted.ID}},
			Mutation: executor.MutationOptions{
				ExpectedSchemaVersion: 1, ExpectedRevision: 1, MaxAffectedRows: 1,
			},
		}},
	})
	assertStatuses(t, committed, result.StatusSucceeded)
	tasks, err := rows.ListReindexTasks(ctx)
	if err != nil || len(tasks) != 1 || tasks[0].Revision != 2 ||
		tasks[0].State != reindex.StatePending {
		t.Fatalf("committed task = %#v, %v", tasks, err)
	}
	if matches, err := rows.LookupAgentTerm(ctx, inserted.DatabaseID, "initial", 10); err != nil ||
		len(matches) != 0 {
		t.Fatalf("stale Agent index = %#v, %v", matches, err)
	}
	if matches, err := rows.LookupMechanicalTerm(ctx, inserted.DatabaseID, "committed", 10); err != nil ||
		len(matches) != 1 || matches[0].Revision != 2 {
		t.Fatalf("mechanical fallback = %#v, %v", matches, err)
	}
}
