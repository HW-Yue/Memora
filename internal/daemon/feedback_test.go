package daemon

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/feedback"
	"github.com/HW-Yue/Memora/internal/ipc"
	"github.com/HW-Yue/Memora/internal/row"
	nativekvstore "github.com/HW-Yue/Memora/internal/store/nativekv"
)

func TestFeedbackHandlerRecordsWithoutMutationAndConfirmsUndo(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	database, err := nativekvstore.Open(filepath.Join(t.TempDir(), "feedback-daemon.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	dictionary := catalog.New(database, catalog.Options{})
	if _, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{Name: "work", Purpose: "Work", Scope: "Reviewed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := dictionary.CreateTable(ctx, "work", catalog.TableDefinition{
		Name: "notes", Purpose: "Notes", RowSemantics: "One note",
		Columns: []catalog.ColumnDefinition{{Name: "title", Type: "TEXT(100)", Purpose: "Title"}},
	}); err != nil {
		t.Fatal(err)
	}
	rows := row.New(database, dictionary, row.Options{})
	first, err := rows.Insert(ctx, "work", "notes", map[string]any{"title": "first"}, row.WriteOptions{
		ExpectedSchemaVersion: 1, Metadata: row.WriteMetadata{Actor: "agent:test", Source: "seed-1", Reason: "seed"},
		RouteLeafIDs: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	current, err := rows.Update(ctx, "work", "notes", first.ID, map[string]any{"title": "wrong"}, row.WriteOptions{
		ExpectedSchemaVersion: 1, ExpectedRevision: 1,
		Metadata:     row.WriteMetadata{Actor: "agent:test", Source: "seed-2", Reason: "wrong"},
		RouteLeafIDs: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := newDatabaseHandler(ctx, dictionary, rows, database)
	defer handler.Close()
	session := ipc.Session{ID: "feedback-session"}
	event := feedback.Event{
		Version: feedback.EventVersion, EventID: "feedback-daemon", Kind: feedback.KindWrong,
		Actor: "agent:test", Reason: "incorrect title",
		Target: feedback.Target{Database: "work", Table: "notes", RowID: current.ID, Revision: current.Revision},
	}
	eventPayload, _ := json.Marshal(event)
	encoded, err := handler.Handle(ctx, session, ipc.Request{
		Version: ipc.Version, RequestID: "record-feedback", Method: "feedback.record", Payload: eventPayload,
	})
	if err != nil {
		t.Fatal(err)
	}
	var eventReceipt feedback.Receipt
	if err := json.Unmarshal(encoded, &eventReceipt); err != nil || eventReceipt.Status != "recorded" {
		t.Fatalf("feedback receipt = %#v, %v", eventReceipt, err)
	}
	unchanged, _ := rows.Get(ctx, "work", "notes", current.ID)
	if unchanged.Revision != 2 {
		t.Fatalf("recording feedback changed revision to %d", unchanged.Revision)
	}

	confirmation := feedback.Confirmation{
		Version: feedback.ConfirmationVersion, ConfirmationID: "confirm-daemon",
		FeedbackEventID: event.EventID, SourceEventID: "user-confirm-daemon", Actor: "agent:test",
		Instruction: "restore first title", Action: feedback.ActionUndo, ExpectedRevision: 2,
		AuthorizedDatabases: []string{"work"},
		Undo:                &feedback.Undo{TargetRevision: 1, ExpectedSchemaVersion: 1, RouteLeafIDs: []string{}},
	}
	confirmationPayload, _ := json.Marshal(confirmation)
	encoded, err = handler.Handle(ctx, session, ipc.Request{
		Version: ipc.Version, RequestID: "confirm-feedback", Method: "feedback.confirm", Payload: confirmationPayload,
	})
	if err != nil {
		t.Fatal(err)
	}
	var confirmationReceipt feedback.ConfirmationReceipt
	if err := json.Unmarshal(encoded, &confirmationReceipt); err != nil || !confirmationReceipt.Verified {
		t.Fatalf("confirmation receipt = %#v, %v", confirmationReceipt, err)
	}
	restored, _ := rows.Get(ctx, "work", "notes", current.ID)
	if restored.Revision != 3 || restored.Values["title"] != "first" {
		t.Fatalf("restored Row = %#v", restored)
	}
}
