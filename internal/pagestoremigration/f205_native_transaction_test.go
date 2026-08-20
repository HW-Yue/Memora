package pagestoremigration

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/nativecatalog"
	"github.com/HW-Yue/Memora/internal/nativemutation"
	"github.com/HW-Yue/Memora/internal/nativerouter"
	"github.com/HW-Yue/Memora/internal/nativerow"
	"github.com/HW-Yue/Memora/internal/row"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

func TestF205MultiRowPublicationFaultReopensAsOneOutcome(t *testing.T) {
	ctx := context.Background()
	directory, file, authority := newAuthorityFixture(t)
	dictionary := nativecatalog.NewService(
		nativecatalog.New(file), nativecatalog.ServiceOptions{Authority: authority},
	)
	if _, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{
		Name: "work", Purpose: "Work", Scope: "F205 fault fixture",
	}); err != nil {
		t.Fatal(err)
	}
	table, err := dictionary.CreateTable(ctx, "work", catalog.TableDefinition{
		Name: "notes", Purpose: "Notes", RowSemantics: "One claim",
		Columns: []catalog.ColumnDefinition{{Name: "title", Type: "TEXT(100)", Purpose: "Title"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rows := nativerow.NewService(nativerow.New(file), dictionary, nativerow.ServiceOptions{Authority: authority})
	first, err := rows.Insert(ctx, "work", "notes", map[string]any{"title": "first"}, row.WriteOptions{ExpectedSchemaVersion: table.SchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	second, err := rows.Insert(ctx, "work", "notes", map[string]any{"title": "second"}, row.WriteOptions{ExpectedSchemaVersion: table.SchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	firstBody, err := rows.CurrentBody(ctx, table, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	secondBody, err := rows.CurrentBody(ctx, table, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	sequence, err := rows.NextCommitSequence(ctx)
	if err != nil {
		t.Fatal(err)
	}
	firstBody.Revision++
	firstBody.CommitSequence = sequence
	firstBody.Values[table.Columns[0].ID] = "first revised"
	firstBody.UpdatedAt = time.Now().UTC()
	secondBody.Revision++
	secondBody.CommitSequence = sequence
	secondBody.Values[table.Columns[0].ID] = "second revised"
	secondBody.UpdatedAt = firstBody.UpdatedAt
	authority.checkpoint = func(phase authorityPhase) error {
		if phase == phaseRowCurrentPublished {
			return errors.New("injected F205 multi-row publication fault")
		}
		return nil
	}
	coordinator := nativemutation.New(file, nativerow.New(file), nativerouter.New(file), authority)
	err = coordinator.Commit(nativemutation.Plan{Changes: []nativemutation.RowChange{
		{Row: firstBody, Operation: history.OperationUpdate, Metadata: row.WriteMetadata{Actor: "f205", Source: "test", Reason: "fault"}, RecordedAt: firstBody.UpdatedAt},
		{Row: secondBody, Operation: history.OperationUpdate, Metadata: row.WriteMetadata{Actor: "f205", Source: "test", Reason: "fault"}, RecordedAt: secondBody.UpdatedAt},
	}})
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("multi-row fault error = %v", err)
	}
	authority.checkpoint = nil
	// F226: reads stay available; the affected Database fails closed for writes.
	if _, err := authority.Capture(ctx); err != nil {
		t.Fatalf("Capture() after fault = %v, want success", err)
	}
	assertDatabaseWritesPoisoned(t, ctx, authority, table.DatabaseID)
	if err := authority.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	reopenedFile, err := nativestore.Open(filepath.Join(directory, "database.memora"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedFile.Close()
	reopened, err := OpenAuthority(ctx, reopenedFile, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopenedDictionary := nativecatalog.NewService(nativecatalog.New(reopenedFile), nativecatalog.ServiceOptions{Authority: reopened})
	reopenedRows := nativerow.NewService(nativerow.New(reopenedFile), reopenedDictionary, nativerow.ServiceOptions{Authority: reopened})
	for id, expected := range map[string]string{first.ID: "first revised", second.ID: "second revised"} {
		value, getErr := reopenedRows.Get(ctx, "work", "notes", id)
		if getErr != nil || value.Revision != 2 || value.Values["title"] != expected {
			t.Fatalf("reopened Row %q = %#v, %v", id, value, getErr)
		}
	}
}
