package executor_test

import (
	"context"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
)

func TestExecuteHistoryAsOfShowAndRestore(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	subject, rows, closeStore := queryFixture(t, ctx)
	defer closeStore()
	inserted := insertQueryRow(t, ctx, rows, map[string]any{"title": "initial", "count": 1})
	updated, err := rows.Update(ctx, "work", "notes", inserted.ID, map[string]any{
		"title": "revised",
	}, row.WriteOptions{ExpectedSchemaVersion: 1, ExpectedRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rows.Delete(ctx, "work", "notes", inserted.ID, row.WriteOptions{
		ExpectedSchemaVersion: 1, ExpectedRevision: 2,
	}); err != nil {
		t.Fatal(err)
	}

	atRevision, err := execute(ctx, subject, `
		SELECT title, revision, commit_sequence
		FROM work.notes AS OF REVISION :revision
		WHERE row_id = :row_id LIMIT 1
	`, executor.Parameters{Named: map[string]any{
		"revision": int64(1), "row_id": inserted.ID,
	}}, executor.MutationOptions{})
	if err != nil {
		t.Fatalf("AS OF REVISION error = %v", err)
	}
	if len(atRevision.Rows) != 1 || atRevision.Rows[0]["title"] != "initial" ||
		atRevision.Rows[0]["revision"] != uint64(1) {
		t.Fatalf("AS OF REVISION output = %#v", atRevision)
	}

	atCommit, err := execute(ctx, subject, `
		SELECT title, revision FROM work.notes AS OF COMMIT_SEQUENCE :sequence
		WHERE row_id = :row_id LIMIT 1
	`, executor.Parameters{Named: map[string]any{
		"sequence": int64(updated.CommitSequence), "row_id": inserted.ID,
	}}, executor.MutationOptions{})
	if err != nil {
		t.Fatalf("AS OF COMMIT_SEQUENCE error = %v", err)
	}
	if len(atCommit.Rows) != 1 || atCommit.Rows[0]["title"] != "revised" ||
		atCommit.Rows[0]["revision"] != uint64(2) {
		t.Fatalf("AS OF COMMIT_SEQUENCE output = %#v", atCommit)
	}

	historyOutput, err := execute(ctx, subject, `
		SHOW HISTORY FROM work.notes FOR ROW :row_id LIMIT 2
	`, executor.Parameters{Named: map[string]any{"row_id": inserted.ID}}, executor.MutationOptions{})
	if err != nil {
		t.Fatalf("SHOW HISTORY error = %v", err)
	}
	if len(historyOutput.Rows) != 2 || !historyOutput.Truncated ||
		historyOutput.Rows[0]["revision"] != uint64(3) ||
		historyOutput.Rows[0]["operation"] != "DELETE" ||
		historyOutput.Rows[1]["revision"] != uint64(2) {
		t.Fatalf("SHOW HISTORY output = %#v", historyOutput)
	}
	if historyOutput.Page == nil || historyOutput.Page.Version != result.ListPageVersion ||
		historyOutput.Page.Snapshot == "" || historyOutput.NextCursor == "" {
		t.Fatalf("SHOW HISTORY page = %#v", historyOutput.Page)
	}
	secondHistory, err := execute(ctx, subject,
		"SHOW HISTORY FROM work.notes FOR ROW :row_id CURSOR :cursor LIMIT 2",
		executor.Parameters{Named: map[string]any{
			"row_id": inserted.ID, "cursor": historyOutput.NextCursor,
		}}, executor.MutationOptions{})
	if err != nil || len(secondHistory.Rows) != 1 || secondHistory.Truncated ||
		secondHistory.Rows[0]["revision"] != uint64(1) {
		t.Fatalf("SHOW HISTORY second page = %#v, %v", secondHistory, err)
	}

	restored, err := execute(ctx, subject, `
		RESTORE work.notes ROW :row_id TO REVISION :revision
	`, executor.Parameters{Named: map[string]any{
		"row_id": inserted.ID, "revision": int64(1),
	}}, executor.MutationOptions{
		ExpectedSchemaVersion: 1, ExpectedRevision: 3, MaxAffectedRows: 1,
		Actor: "agent:test", Source: "history:test", Reason: "restore original",
	})
	if err != nil {
		t.Fatalf("RESTORE error = %v", err)
	}
	if restored.AffectedRows != 1 || restored.Revision == nil || *restored.Revision != 4 ||
		restored.CommitSequence == nil || *restored.CommitSequence != 4 {
		t.Fatalf("RESTORE output = %#v", restored)
	}
	current, err := rows.Get(ctx, "work", "notes", inserted.ID)
	if err != nil || current.State != row.StateLive || current.Values["title"] != "initial" {
		t.Fatalf("restored current row = %#v, %v", current, err)
	}
	records, err := rows.History(ctx, "work", "notes", inserted.ID)
	if err != nil || len(records) != 4 || records[3].Operation != "COMPENSATE" ||
		records[3].Actor != "agent:test" || records[3].Reason != "restore original" {
		t.Fatalf("RESTORE history = %#v, %v", records, err)
	}
	_, err = execute(ctx, subject,
		"SHOW HISTORY FROM work.notes FOR ROW :row_id CURSOR :cursor LIMIT 2",
		executor.Parameters{Named: map[string]any{
			"row_id": inserted.ID, "cursor": historyOutput.NextCursor,
		}}, executor.MutationOptions{})
	assertExecutorCode(t, err, result.CodeRevisionConflict)
}

func TestHistoryStatementsRejectUnboundedOrInvalidTargets(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	subject, _, closeStore := queryFixture(t, ctx)
	defer closeStore()
	for _, source := range []string{
		"SELECT * FROM work.notes AS OF REVISION 1 LIMIT 10",
		"SHOW HISTORY FROM work.notes FOR ROW 'not-a-row-id' LIMIT 10",
		"SHOW HISTORY FROM work.notes FOR ROW 'row_missing' LIMIT 0",
	} {
		if _, err := execute(ctx, subject, source, executor.Parameters{}, executor.MutationOptions{}); err == nil {
			t.Fatalf("Execute(%q) succeeded", source)
		}
	}
}
