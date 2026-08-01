package row_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
	nativekvstore "github.com/HW-Yue/Memora/internal/store/nativekv"
)

func TestRowMutationsAppendPersistentSemanticHistory(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "database.db")
	firstStore, err := nativekvstore.Open(path)
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

	secondStore, err := nativekvstore.Open(path)
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
	databaseStore, err := nativekvstore.Open(filepath.Join(t.TempDir(), "database.db"))
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

func TestHistoryAsOfAndCompensationRestoreDeletedRow(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, err := nativekvstore.Open(filepath.Join(t.TempDir(), "database.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	dictionary := catalog.New(databaseStore, catalog.Options{
		IDs: &idSource{values: []string{"database", "table", "title", "body"}},
	})
	createSchema(t, ctx, dictionary)
	service := row.New(databaseStore, dictionary, row.Options{
		IDs: &idSource{values: []string{"note"}},
	})
	inserted, err := service.Insert(ctx, "work", "notes", map[string]any{"title": "initial"}, row.WriteOptions{
		ExpectedSchemaVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := service.Update(ctx, "work", "notes", inserted.ID, map[string]any{"title": "revised"}, row.WriteOptions{
		ExpectedSchemaVersion: 1, ExpectedRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Delete(ctx, "work", "notes", inserted.ID, row.WriteOptions{
		ExpectedSchemaVersion: 1, ExpectedRevision: 2,
	}); err != nil {
		t.Fatal(err)
	}

	atRevision, err := service.AsOfRevision(ctx, "work", "notes", inserted.ID, 1)
	if err != nil || atRevision.Values["title"] != "initial" || atRevision.Revision != 1 ||
		atRevision.CommitSequence != inserted.CommitSequence || atRevision.State != row.StateLive {
		t.Fatalf("AsOfRevision() = %#v, %v", atRevision, err)
	}
	atCommit, err := service.AsOfCommit(ctx, "work", "notes", inserted.ID, updated.CommitSequence)
	if err != nil || atCommit.Values["title"] != "revised" || atCommit.Revision != 2 {
		t.Fatalf("AsOfCommit() = %#v, %v", atCommit, err)
	}

	restored, err := service.Restore(ctx, "work", "notes", inserted.ID, 1, row.WriteOptions{
		ExpectedSchemaVersion: 1, ExpectedRevision: 3,
		Metadata: row.WriteMetadata{
			Actor: "agent:test", Source: "history:revision-1", Reason: "undo deletion",
		},
	})
	if err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	if restored.Revision != 4 || restored.CommitSequence != 4 || restored.State != row.StateLive ||
		restored.Values["title"] != "initial" {
		t.Fatalf("restored row = %#v", restored)
	}
	current, err := service.Get(ctx, "work", "notes", inserted.ID)
	if err != nil || current.Revision != 4 {
		t.Fatalf("current row = %#v, %v", current, err)
	}
	records, page, err := service.HistoryPage(ctx, "work", "notes", inserted.ID, "", 2)
	hasMore := page.NextCursor != ""
	if err != nil || !hasMore || len(records) != 2 ||
		records[0].Revision != 4 || records[0].Operation != history.OperationCompensate ||
		records[1].Revision != 3 {
		t.Fatalf("HistoryPage() = %#v, hasMore=%v, %v", records, hasMore, err)
	}
	all, err := service.History(ctx, "work", "notes", inserted.ID)
	if err != nil || len(all) != 4 || all[0].Values == nil ||
		all[0].Operation != history.OperationInsert || all[3].Reason != "undo deletion" {
		t.Fatalf("immutable history = %#v, %v", all, err)
	}

	_, err = service.Restore(ctx, "work", "notes", inserted.ID, 2, row.WriteOptions{
		ExpectedSchemaVersion: 1, ExpectedRevision: 3,
	})
	assertCode(t, err, result.CodeRevisionConflict)
}

func TestHistoryProjectionAndRestoreFollowStableColumnIdentity(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, err := nativekvstore.Open(filepath.Join(t.TempDir(), "database.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	dictionary := catalog.New(databaseStore, catalog.Options{
		IDs: &idSource{values: []string{"database", "table", "title", "body"}},
	})
	createSchema(t, ctx, dictionary)
	service := row.New(databaseStore, dictionary, row.Options{
		IDs: &idSource{values: []string{"note"}},
	})
	inserted, err := service.Insert(ctx, "work", "notes", map[string]any{"title": "stable ID"}, row.WriteOptions{
		ExpectedSchemaVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dictionary.RenameColumn(ctx, "work", "notes", "title", "Heading"); err != nil {
		t.Fatal(err)
	}

	historical, err := service.AsOfRevision(ctx, "work", "notes", inserted.ID, 1)
	if err != nil || historical.Values["Heading"] != "stable ID" {
		t.Fatalf("renamed history projection = %#v, %v", historical, err)
	}
	if _, exists := historical.Values["title"]; exists {
		t.Fatalf("history exposed deprecated name: %#v", historical.Values)
	}
	restored, err := service.Restore(ctx, "work", "notes", inserted.ID, 1, row.WriteOptions{
		ExpectedSchemaVersion: 2, ExpectedRevision: 1,
	})
	if err != nil || restored.SchemaVersion != 2 || restored.Revision != 2 ||
		restored.Values["Heading"] != "stable ID" {
		t.Fatalf("restored renamed row = %#v, %v", restored, err)
	}
}
