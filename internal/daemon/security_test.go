package daemon

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/ipc"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/security"
	sqlitestore "github.com/HW-Yue/Memora/internal/store/sqlite"
)

func TestHandlerAuditsRequestHashesWithoutPayloadOrParameters(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	database, err := sqlitestore.Open(filepath.Join(t.TempDir(), "security-audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	dictionary := catalog.New(database, catalog.Options{})
	if _, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{
		Name: "work", Purpose: "Work", Scope: "Reviewed",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := dictionary.CreateTable(ctx, "work", catalog.TableDefinition{
		Name: "notes", Purpose: "Notes", RowSemantics: "One note",
		Columns: []catalog.ColumnDefinition{{Name: "title", Type: "TEXT", Purpose: "Title"}},
	}); err != nil {
		t.Fatal(err)
	}
	rows := row.New(database, dictionary, row.Options{})
	handler := newDatabaseHandler(ctx, dictionary, rows, database)
	secret := "sk-private-parameter"
	authorization := security.Authorization{
		Version: security.AuthorizationVersion, Actor: "agent:host",
		AuthorizedDatabases: []string{"work"},
	}
	payload, err := json.Marshal(executePayload{
		Source: "SELECT title FROM work.notes WHERE title = :title LIMIT 1",
		Statements: []executor.StatementInput{{
			Parameters:    executor.Parameters{Named: map[string]any{"title": secret}},
			Authorization: authorization,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := handler.Handle(ctx, ipc.Session{ID: "security-session"}, ipc.Request{
		RequestID: "security-request-1", Method: "msql.execute", Payload: payload,
	})
	if err != nil || !strings.Contains(string(response), `"ok":true`) {
		t.Fatalf("Handle() = %s, %v", response, err)
	}

	deniedPayload, err := json.Marshal(executePayload{
		Source:     "SELECT title FROM private.notes LIMIT 1",
		Statements: []executor.StatementInput{{Authorization: authorization}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handler.Handle(ctx, ipc.Session{ID: "security-session"}, ipc.Request{
		RequestID: "security-request-2", Method: "msql.execute", Payload: deniedPayload,
	}); err != nil {
		t.Fatal(err)
	}
	events, err := handler.security.Events(ctx)
	if err != nil || len(events) != 2 {
		t.Fatalf("Events() = %#v, %v", events, err)
	}
	if events[0].Actor != "agent:host" || len(events[0].AuthorizedDatabases) != 1 ||
		events[0].PayloadSHA256 != security.HashPayload(payload) ||
		events[1].Status != security.AuditFailed ||
		events[1].ErrorCode != "permission_denied" {
		t.Fatalf("audit events = %#v", events)
	}
	for _, event := range events {
		if strings.Contains(event.String(), secret) || strings.Contains(event.String(), "SELECT title") {
			t.Fatalf("audit leaked request content: %s", event.String())
		}
	}
}
