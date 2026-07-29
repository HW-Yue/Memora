package row_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/row"
	sqlitestore "github.com/HW-Yue/Memora/internal/store/sqlite"
)

func TestRowMutationsAppendPersistentSemanticHistory(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "database.db")
	firstStore, err := sqlitestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	dictionary := catalog.New(firstStore, catalog.Options{
		IDs: &idSource{values: []string{"database", "table", "title", "body"}},
	})
	createSchema(t, ctx, dictionary)
	service := row.New(firstStore, dictionary, row.Options{
		IDs: &idSource{values: []string{"note"}},
	})
	inserted, err := service.Insert(ctx, "work", "notes", map[string]any{
		"title": "initial",
	}, row.WriteOptions{
		ExpectedSchemaVersion: 1,
		Metadata: row.WriteMetadata{
			Actor: "agent:codex", Source: "conversation:event-1", Reason: "captured stable decision",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := service.Update(ctx, "work", "notes", inserted.ID, map[string]any{
		"title": "revised",
	}, row.WriteOptions{
		ExpectedSchemaVersion: 1, ExpectedRevision: 1,
		Metadata: row.WriteMetadata{
			Actor: "agent:codex", Source: "conversation:event-2", Reason: "corrected decision",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := service.Delete(ctx, "work", "notes", inserted.ID, row.WriteOptions{
		ExpectedSchemaVersion: 1, ExpectedRevision: 2,
		Metadata: row.WriteMetadata{
			Actor: "agent:codex", Source: "conversation:event-3", Reason: "decision no longer applies",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if inserted.CommitSequence != 1 || updated.CommitSequence != 2 || deleted.CommitSequence != 3 {
		t.Fatalf("row commit sequences = %d, %d, %d", inserted.CommitSequence, updated.CommitSequence, deleted.CommitSequence)
	}
	records, err := service.History(ctx, "work", "notes", inserted.ID)
	if err != nil {
		t.Fatalf("History() error = %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("history = %#v", records)
	}
	for index, operation := range []history.Operation{history.OperationInsert, history.OperationUpdate, history.OperationDelete} {
		record := records[index]
		if record.Revision != uint64(index+1) || record.CommitSequence != uint64(index+1) ||
			record.Operation != operation || record.Actor != "agent:codex" || record.Source == "" || record.Reason == "" {
			t.Fatalf("history[%d] = %#v", index, record)
		}
	}
	if records[2].State != string(row.StateDeleted) || records[2].Values == nil {
		t.Fatalf("delete history = %#v", records[2])
	}
	if err := firstStore.Close(); err != nil {
		t.Fatal(err)
	}

	secondStore, err := sqlitestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.Close()
	reopened := row.New(secondStore, catalog.New(secondStore, catalog.Options{}), row.Options{})
	persisted, err := reopened.History(ctx, "work", "notes", inserted.ID)
	if err != nil || len(persisted) != 3 || persisted[2].CommitSequence != 3 {
		t.Fatalf("reopened history = %#v, %v", persisted, err)
	}
}

func TestTransactionHistorySharesCommitSequenceAndRollsBackAtomically(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, err := sqlitestore.Open(filepath.Join(t.TempDir(), "database.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	dictionary := catalog.New(databaseStore, catalog.Options{
		IDs: &idSource{values: []string{"database", "table", "title", "body"}},
	})
	createSchema(t, ctx, dictionary)
	service := row.New(databaseStore, dictionary, row.Options{
		IDs: &idSource{values: []string{"rolled-first", "rolled-second", "first", "second"}},
	})
	options := row.WriteOptions{
		ExpectedSchemaVersion: 1,
		Metadata: row.WriteMetadata{
			Actor: "agent:test", Source: "batch:test", Reason: "verify atomic history",
		},
	}

	rolledBack, err := service.BeginTransaction(ctx)
	if err != nil {
		t.Fatal(err)
	}
	firstRolled, err := rolledBack.Insert(ctx, "work", "notes", map[string]any{"title": "rolled first"}, options)
	if err != nil {
		t.Fatal(err)
	}
	secondRolled, err := rolledBack.Insert(ctx, "work", "notes", map[string]any{"title": "rolled second"}, options)
	if err != nil {
		t.Fatal(err)
	}
	if firstRolled.CommitSequence != secondRolled.CommitSequence {
		t.Fatalf("rolled-back commit sequences = %d, %d", firstRolled.CommitSequence, secondRolled.CommitSequence)
	}
	if err := rolledBack.Rollback(); err != nil {
		t.Fatal(err)
	}
	for _, rowID := range []string{firstRolled.ID, secondRolled.ID} {
		records, historyErr := service.History(ctx, "work", "notes", rowID)
		if historyErr != nil || len(records) != 0 {
			t.Fatalf("rolled-back history for %s = %#v, %v", rowID, records, historyErr)
		}
	}

	committed, err := service.BeginTransaction(ctx)
	if err != nil {
		t.Fatal(err)
	}
	first, err := committed.Insert(ctx, "work", "notes", map[string]any{"title": "first"}, options)
	if err != nil {
		t.Fatal(err)
	}
	second, err := committed.Insert(ctx, "work", "notes", map[string]any{"title": "second"}, options)
	if err != nil {
		t.Fatal(err)
	}
	if err := committed.Commit(); err != nil {
		t.Fatal(err)
	}
	if first.CommitSequence != 1 || second.CommitSequence != 1 {
		t.Fatalf("committed sequences = %d, %d, want 1", first.CommitSequence, second.CommitSequence)
	}
	for _, rowID := range []string{first.ID, second.ID} {
		records, historyErr := service.History(ctx, "work", "notes", rowID)
		if historyErr != nil || len(records) != 1 || records[0].CommitSequence != 1 {
			t.Fatalf("committed history for %s = %#v, %v", rowID, records, historyErr)
		}
	}
}
