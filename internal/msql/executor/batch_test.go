package executor_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
	sqlitestore "github.com/HW-Yue/Memora/internal/store/sqlite"
)

func TestBatchAutocommitFailureDoesNotStopIndependentStatements(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	session, _, closeSession := batchFixture(t, ctx)
	defer closeSession()
	envelope := session.Execute(ctx, executor.BatchRequest{
		RequestID: "autocommit",
		Source: `
			INSERT INTO work.notes (title) VALUES ('first');
			INSERT INTO work.notes (title, count) VALUES ('invalid', 'not an integer');
			SELECT title FROM work.notes LIMIT 10
		`,
		Statements: []executor.StatementInput{
			{Mutation: executor.MutationOptions{ExpectedSchemaVersion: 1, MaxAffectedRows: 1}},
			{Mutation: executor.MutationOptions{ExpectedSchemaVersion: 1, MaxAffectedRows: 1}},
			{},
		},
	})

	assertStatuses(t, envelope, result.StatusSucceeded, result.StatusFailed, result.StatusSucceeded)
	if envelope.Results[1].Error == nil || envelope.Results[1].Error.Code != result.CodeConstraint {
		t.Fatalf("failed result = %#v", envelope.Results[1])
	}
	if len(envelope.Results[2].Rows) != 1 || envelope.Results[2].Rows[0]["title"] != "first" {
		t.Fatalf("final SELECT rows = %#v", envelope.Results[2].Rows)
	}
}

func TestBatchWriteFailureRollsBackTransactionAndContinuesAfterBoundary(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	session, _, closeSession := batchFixture(t, ctx)
	defer closeSession()
	envelope := session.Execute(ctx, executor.BatchRequest{
		RequestID: "transaction-failure",
		Source: `
			BEGIN;
			INSERT INTO work.notes (title) VALUES ('rolled back');
			INSERT INTO work.notes (title, count) VALUES ('invalid', 'not an integer');
			SELECT title FROM work.notes LIMIT 10;
			COMMIT;
			INSERT INTO work.notes (title) VALUES ('after');
			SELECT title FROM work.notes LIMIT 10
		`,
		Statements: []executor.StatementInput{
			{},
			{Mutation: executor.MutationOptions{ExpectedSchemaVersion: 1, MaxAffectedRows: 1}},
			{Mutation: executor.MutationOptions{ExpectedSchemaVersion: 1, MaxAffectedRows: 1}},
			{},
			{},
			{Mutation: executor.MutationOptions{ExpectedSchemaVersion: 1, MaxAffectedRows: 1}},
			{},
		},
	})

	assertStatuses(t, envelope,
		result.StatusRolledBack,
		result.StatusRolledBack,
		result.StatusFailed,
		result.StatusSkipped,
		result.StatusSkipped,
		result.StatusSucceeded,
		result.StatusSucceeded,
	)
	if len(envelope.Results[6].Rows) != 1 || envelope.Results[6].Rows[0]["title"] != "after" {
		t.Fatalf("post-transaction rows = %#v", envelope.Results[6].Rows)
	}
}

func TestBatchReadFailureIsStructuredAndLaterReadContinues(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	session, rows, closeSession := batchFixture(t, ctx)
	defer closeSession()
	insertQueryRow(t, ctx, rows, map[string]any{"title": "present"})

	envelope := session.Execute(ctx, executor.BatchRequest{
		RequestID: "read-failure",
		Source: `
			SELECT * FROM work.missing LIMIT 10;
			SELECT title FROM work.notes LIMIT 10
		`,
	})

	assertStatuses(t, envelope, result.StatusFailed, result.StatusSucceeded)
	if envelope.Results[0].Error == nil || envelope.Results[0].Error.Code != result.CodeNotFound ||
		envelope.Results[0].Error.StatementIndex == nil || *envelope.Results[0].Error.StatementIndex != 0 {
		t.Fatalf("structured read error = %#v", envelope.Results[0].Error)
	}
	if len(envelope.Results[1].Rows) != 1 {
		t.Fatalf("later read rows = %#v", envelope.Results[1].Rows)
	}
}

func TestBatchReadFailureDoesNotAbortExplicitTransaction(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	session, _, closeSession := batchFixture(t, ctx)
	defer closeSession()
	envelope := session.Execute(ctx, executor.BatchRequest{
		RequestID: "transaction-read-failure",
		Source: `
			BEGIN;
			SELECT * FROM work.missing LIMIT 10;
			INSERT INTO work.notes (title) VALUES ('committed');
			COMMIT;
			SELECT title FROM work.notes LIMIT 10
		`,
		Statements: []executor.StatementInput{
			{},
			{},
			{Mutation: executor.MutationOptions{ExpectedSchemaVersion: 1, MaxAffectedRows: 1}},
			{},
			{},
		},
	})

	assertStatuses(t, envelope,
		result.StatusSucceeded,
		result.StatusFailed,
		result.StatusSucceeded,
		result.StatusSucceeded,
		result.StatusSucceeded,
	)
	if envelope.Results[1].Error == nil || envelope.Results[1].Error.Code != result.CodeNotFound {
		t.Fatalf("transaction read error = %#v", envelope.Results[1])
	}
	if len(envelope.Results[4].Rows) != 1 || envelope.Results[4].Rows[0]["title"] != "committed" {
		t.Fatalf("committed rows = %#v", envelope.Results[4].Rows)
	}
}

func TestBatchTransactionPersistsAcrossRequestsAndSessionCloseRollsBack(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	session, rows, closeStore := batchFixture(t, ctx)
	defer closeStore()
	first := session.Execute(ctx, executor.BatchRequest{
		RequestID: "begin",
		Source:    "BEGIN; INSERT INTO work.notes (title) VALUES ('pending')",
		Statements: []executor.StatementInput{
			{},
			{Mutation: executor.MutationOptions{ExpectedSchemaVersion: 1, MaxAffectedRows: 1}},
		},
	})
	assertStatuses(t, first, result.StatusSucceeded, result.StatusSucceeded)
	if !session.Active() {
		t.Fatal("session transaction is not active")
	}
	second := session.Execute(ctx, executor.BatchRequest{
		RequestID: "read-own-write",
		Source:    "SELECT title FROM work.notes LIMIT 10",
	})
	assertStatuses(t, second, result.StatusSucceeded)
	if len(second.Results[0].Rows) != 1 || second.Results[0].Rows[0]["title"] != "pending" {
		t.Fatalf("transaction read rows = %#v", second.Results[0].Rows)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if session.Active() {
		t.Fatal("closed session transaction is active")
	}
	persisted, err := rows.List(ctx, "work", "notes", 10)
	if err != nil || len(persisted) != 0 {
		t.Fatalf("rows after session close = %#v, %v", persisted, err)
	}
}

func TestBatchExplicitRollbackReportsExecutedStatements(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	session, rows, closeSession := batchFixture(t, ctx)
	defer closeSession()
	envelope := session.Execute(ctx, executor.BatchRequest{
		RequestID: "explicit-rollback",
		Source:    "BEGIN; INSERT INTO work.notes (title) VALUES ('discarded'); ROLLBACK",
		Statements: []executor.StatementInput{
			{},
			{Mutation: executor.MutationOptions{ExpectedSchemaVersion: 1, MaxAffectedRows: 1}},
			{},
		},
	})

	assertStatuses(t, envelope, result.StatusRolledBack, result.StatusRolledBack, result.StatusSucceeded)
	persisted, err := rows.List(ctx, "work", "notes", 10)
	if err != nil || len(persisted) != 0 {
		t.Fatalf("rows after explicit rollback = %#v, %v", persisted, err)
	}
}

func batchFixture(t *testing.T, ctx context.Context) (*executor.BatchSession, *row.Service, func()) {
	t.Helper()
	databaseStore, err := sqlitestore.Open(filepath.Join(t.TempDir(), "database.db"))
	if err != nil {
		t.Fatal(err)
	}
	dictionary := catalog.New(databaseStore, catalog.Options{
		IDs: &idSource{values: []string{"database", "table", "title", "count", "done", "at"}},
	})
	if _, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{
		Name: "work", Purpose: "Work", Scope: "Projects",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := dictionary.CreateTable(ctx, "work", catalog.TableDefinition{
		Name: "notes", Purpose: "Notes", RowSemantics: "One note",
		Columns: []catalog.ColumnDefinition{
			{Name: "title", Type: "TEXT(100)", Purpose: "Title"},
			{Name: "count", Type: "INTEGER", Nullable: true, Purpose: "Count"},
			{Name: "done", Type: "BOOLEAN", Nullable: true, Purpose: "Done"},
			{Name: "at", Type: "TIMESTAMP", Nullable: true, Purpose: "Time"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	rows := row.New(databaseStore, dictionary, row.Options{
		IDs: &idSource{values: []string{"first", "second", "third", "fourth"}},
	})
	session := executor.NewBatchSession(ctx, dictionary, rows)
	return session, rows, func() {
		_ = session.Close()
		if err := databaseStore.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	}
}

func assertStatuses(t *testing.T, envelope result.Envelope, statuses ...result.Status) {
	t.Helper()
	if _, err := json.Marshal(envelope); err != nil {
		t.Fatalf("Marshal(envelope) error = %v: %#v", err, envelope)
	}
	if len(envelope.Results) != len(statuses) {
		t.Fatalf("result count = %d, want %d: %#v", len(envelope.Results), len(statuses), envelope)
	}
	for index, status := range statuses {
		if envelope.Results[index].Status != status {
			t.Fatalf("result %d status = %q, want %q: %#v", index, envelope.Results[index].Status, status, envelope.Results[index])
		}
	}
}
