package executor_test

import (
	"context"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
)

func TestExecuteParameterizedRelationshipLifecycle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	subject, rows, closeStore := queryFixture(t, ctx)
	defer closeStore()
	source := insertQueryRow(t, ctx, rows, map[string]any{"title": "source"})
	target := insertQueryRow(t, ctx, rows, map[string]any{"title": "target"})
	relationType := "depends_on'; DELETE FROM work.notes"

	created, err := execute(ctx, subject, `
		RELATE work.notes ROW :source
		TO work.notes ROW :target
		TYPE :type DESCRIPTION :description
	`, executor.Parameters{Named: map[string]any{
		"source": source.ID, "target": target.ID,
		"type": relationType, "description": "parameterized relationship",
	}}, executor.MutationOptions{MaxAffectedRows: 1})
	if err != nil {
		t.Fatal(err)
	}
	if created.AffectedRows != 1 || created.Revision == nil || *created.Revision != 1 ||
		created.CommitSequence == nil {
		t.Fatalf("RELATE output = %#v", created)
	}

	shown, err := execute(ctx, subject, `
		SHOW RELATIONS FROM work.notes FOR ROW :row
		DIRECTION OUTGOING LIMIT :limit
	`, executor.Parameters{Named: map[string]any{
		"row": source.ID, "limit": int64(10),
	}}, executor.MutationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(shown.Rows) != 1 || shown.Rows[0]["relation_type"] != relationType ||
		shown.Rows[0]["source_row_id"] != source.ID ||
		shown.Rows[0]["target_row_id"] != target.ID {
		t.Fatalf("SHOW RELATIONS output = %#v", shown)
	}
	relationID, _ := shown.Rows[0]["relation_id"].(string)
	incoming, err := execute(ctx, subject, `
		SHOW RELATIONS FROM work.notes FOR ROW :row
		DIRECTION INCOMING LIMIT 10
	`, executor.Parameters{Named: map[string]any{"row": target.ID}}, executor.MutationOptions{})
	if err != nil || len(incoming.Rows) != 1 ||
		incoming.Rows[0]["relation_id"] != relationID {
		t.Fatalf("incoming SHOW RELATIONS output = %#v, %v", incoming, err)
	}

	deleted, err := execute(ctx, subject, "UNRELATE :relation_id", executor.Parameters{
		Named: map[string]any{"relation_id": relationID},
	}, executor.MutationOptions{ExpectedRevision: 1, MaxAffectedRows: 1})
	if err != nil {
		t.Fatal(err)
	}
	if deleted.AffectedRows != 1 || deleted.Revision == nil || *deleted.Revision != 2 {
		t.Fatalf("UNRELATE output = %#v", deleted)
	}
	shown, err = execute(ctx, subject, `
		SHOW RELATIONS FROM work.notes FOR ROW :row
		DIRECTION OUTGOING LIMIT 10
	`, executor.Parameters{Named: map[string]any{"row": source.ID}}, executor.MutationOptions{})
	if err != nil || len(shown.Rows) != 0 {
		t.Fatalf("relations after UNRELATE = %#v, %v", shown, err)
	}
}

func TestRelationshipWriteFailureRollsBackExplicitBatch(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	session, rows, closeSession := batchFixture(t, ctx)
	defer closeSession()
	source := insertQueryRow(t, ctx, rows, map[string]any{"title": "source"})
	target := insertQueryRow(t, ctx, rows, map[string]any{"title": "target"})

	envelope := session.Execute(ctx, executor.BatchRequest{
		RequestID: "relation-rollback",
		Source: `
			BEGIN;
			RELATE work.notes ROW :source TO work.notes ROW :target TYPE :type;
			INSERT INTO work.notes (title, count) VALUES ('invalid', 'not an integer');
			COMMIT;
			SHOW RELATIONS FROM work.notes FOR ROW :source DIRECTION OUTGOING LIMIT 10
		`,
		Statements: []executor.StatementInput{
			{},
			{
				Parameters: executor.Parameters{Named: map[string]any{
					"source": source.ID, "target": target.ID, "type": "depends_on",
				}},
				Mutation: executor.MutationOptions{MaxAffectedRows: 1},
			},
			{Mutation: executor.MutationOptions{
				ExpectedSchemaVersion: 1, MaxAffectedRows: 1,
			}},
			{},
			{Parameters: executor.Parameters{Named: map[string]any{"source": source.ID}}},
		},
	})

	assertStatuses(t, envelope,
		result.StatusRolledBack,
		result.StatusRolledBack,
		result.StatusFailed,
		result.StatusSkipped,
		result.StatusSucceeded,
	)
	if len(envelope.Results[4].Rows) != 0 {
		t.Fatalf("rolled-back relations = %#v", envelope.Results[4].Rows)
	}
}
