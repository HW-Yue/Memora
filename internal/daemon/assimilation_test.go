package daemon

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/assimilation"
	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/ipc"
	"github.com/HW-Yue/Memora/internal/row"
	sqlitestore "github.com/HW-Yue/Memora/internal/store/sqlite"
)

func TestAssimilationHandlerPersistsTemporaryCoverageAcrossSessions(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, err := sqlitestore.Open(filepath.Join(t.TempDir(), "assimilation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	dictionary := catalog.New(databaseStore, catalog.Options{})
	handler := newDatabaseHandler(ctx, dictionary, row.New(databaseStore, dictionary, row.Options{}), databaseStore)
	defer handler.Close()

	inventory := assimilation.Event{
		Version: assimilation.EventVersion, EventID: "inventory-daemon", TaskID: "book-task",
		Workspace: "repo", Kind: assimilation.KindInventory,
		Inventory: &assimilation.Inventory{
			Source: assimilation.Source{
				ID: "book", Title: "Book", Locator: "book.pdf", ContentHash: daemonDigest("a"),
			},
			Units: []assimilation.Unit{
				{ID: "source", Kind: assimilation.UnitSource, Label: "Book"},
				{ID: "chapter", ParentID: "source", Kind: assimilation.UnitChapter, Label: "Chapter", Extent: 2},
			},
		},
	}
	created := invokeAssimilation(t, ctx, handler, "ipc-one", inventory)
	if created.Revision != 1 || created.UnreadCount != 1 {
		t.Fatalf("created receipt = %#v", created)
	}
	status := invokeAssimilation(t, ctx, handler, "ipc-two", assimilation.Event{
		Version: assimilation.EventVersion, EventID: "status-daemon", TaskID: "book-task",
		Workspace: "repo", Kind: assimilation.KindStatus,
	})
	if status.Revision != 1 || status.UnreadCount != 1 || status.Unread[0].UnitID != "chapter" {
		t.Fatalf("cross-session status = %#v", status)
	}
}

func TestAssimilationHandlerRejectsRawContentField(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, err := sqlitestore.Open(filepath.Join(t.TempDir(), "raw.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	dictionary := catalog.New(databaseStore, catalog.Options{})
	handler := newDatabaseHandler(ctx, dictionary, row.New(databaseStore, dictionary, row.Options{}), databaseStore)
	defer handler.Close()
	payload := json.RawMessage(`{
		"version":"memora.assimilation-event/v1",
		"event_id":"raw","task_id":"task","workspace":"repo","kind":"inventory",
		"content":"raw source text"
	}`)
	if _, err := handler.Handle(ctx, ipc.Session{ID: "ipc"}, ipc.Request{
		Version: ipc.Version, RequestID: "raw-request", Method: "assimilation.record", Payload: payload,
	}); err == nil || !strings.Contains(err.Error(), "payload is invalid") {
		t.Fatalf("raw content error = %v", err)
	}
}

func invokeAssimilation(
	t *testing.T,
	ctx context.Context,
	handler *databaseHandler,
	sessionID string,
	event assimilation.Event,
) assimilation.Receipt {
	t.Helper()
	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := handler.Handle(ctx, ipc.Session{ID: sessionID}, ipc.Request{
		Version: ipc.Version, RequestID: event.EventID, Method: "assimilation.record", Payload: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	var receipt assimilation.Receipt
	if err := json.Unmarshal(encoded, &receipt); err != nil {
		t.Fatal(err)
	}
	return receipt
}

func daemonDigest(character string) string {
	return "sha256:" + strings.Repeat(character, 64)
}
