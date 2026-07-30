package daemon

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/conversation"
	"github.com/HW-Yue/Memora/internal/ipc"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/skillwrite"
	nativekvstore "github.com/HW-Yue/Memora/internal/store/nativekv"
)

func TestReflectHandlerPersistsIdempotencyAcrossSessions(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, err := nativekvstore.Open(filepath.Join(t.TempDir(), "reflect.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	dictionary := catalog.New(databaseStore, catalog.Options{})
	handler := newDatabaseHandler(ctx, dictionary, row.New(databaseStore, dictionary, row.Options{}), databaseStore)
	defer handler.Close()
	event := conversation.Event{
		Version: conversation.EventVersion, EventID: "checkpoint-daemon", SessionID: "host-session",
		Kind: conversation.KindCheckpoint, Workspace: "repo", AuthorizedDatabases: []string{"work"},
		Checkpoint: &conversation.Checkpoint{ActiveDatabase: "work", LastEventID: "event-before"},
	}
	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	for index, sessionID := range []string{"ipc-one", "ipc-two"} {
		encoded, err := handler.Handle(ctx, ipc.Session{ID: sessionID}, ipc.Request{
			Version: ipc.Version, RequestID: "reflect-request", Method: "conversation.reflect", Payload: payload,
		})
		if err != nil {
			t.Fatal(err)
		}
		var receipt conversation.Receipt
		if err := json.Unmarshal(encoded, &receipt); err != nil {
			t.Fatal(err)
		}
		if receipt.Status != conversation.StatusCheckpointed || receipt.Replayed != (index == 1) {
			t.Fatalf("receipt %d = %#v", index, receipt)
		}
	}
}

func TestReflectHandlerCommitsStableDeltaOnceThroughMSQL(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, err := nativekvstore.Open(filepath.Join(t.TempDir(), "reflect-write.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	dictionary := catalog.New(databaseStore, catalog.Options{})
	if _, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{Name: "work", Purpose: "Work", Scope: "Projects"}); err != nil {
		t.Fatal(err)
	}
	if _, err := dictionary.CreateTable(ctx, "work", catalog.TableDefinition{
		Name: "notes", Purpose: "Notes", RowSemantics: "One decision",
		Columns: []catalog.ColumnDefinition{{Name: "title", Type: "TEXT", Purpose: "Decision"}},
	}); err != nil {
		t.Fatal(err)
	}
	rows := row.New(databaseStore, dictionary, row.Options{})
	handler := newDatabaseHandler(ctx, dictionary, rows, databaseStore)
	defer handler.Close()
	zero, one := 0, 1
	plan := skillwrite.Plan{
		Version: skillwrite.PlanVersion, ID: "reflect-plan", Decision: skillwrite.DecisionInsert,
		Database: "work", Table: "notes", Actor: "agent:test", SourceEventID: "stable-event",
		Reason: "stable decision", AuthorizedDatabases: []string{"work"},
		Preflight: []skillwrite.Check{{
			ID: "duplicate", MSQL: "SELECT row_id FROM work.notes LIMIT 1", ExpectRows: &zero,
		}},
		Steps: []skillwrite.Step{{
			ID: "insert", Kind: "INSERT", Target: "decision",
			MSQL: "INSERT INTO work.notes (title) VALUES (:title)",
			Input: executor.StatementInput{
				Parameters: executor.Parameters{Named: map[string]any{"title": "Keep Route results locator-only"}},
				Mutation: executor.MutationOptions{
					ExpectedSchemaVersion: 1, MaxAffectedRows: 1, Actor: "agent:test",
					Source: "stable-event", Reason: "stable decision",
					RouteLeafIDs: []string{},
				},
			},
		}},
		Verify: []skillwrite.Check{{
			ID: "verify", MSQL: "SELECT row_id FROM work.notes LIMIT 1", ExpectRows: &one,
		}},
	}
	event := conversation.Event{
		Version: conversation.EventVersion, EventID: "stable-event", SessionID: "host-session",
		Kind: conversation.KindDelta, Workspace: "repo", AuthorizedDatabases: []string{"work"},
		Deltas: []conversation.Delta{{
			ID: "decision", Disposition: conversation.DispositionPersist, Database: "work", Plan: &plan,
		}},
	}
	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	for index, sessionID := range []string{"ipc-write-one", "ipc-write-two"} {
		encoded, err := handler.Handle(ctx, ipc.Session{ID: sessionID}, ipc.Request{
			Version: ipc.Version, RequestID: "reflect-write", Method: "conversation.reflect", Payload: payload,
		})
		if err != nil {
			t.Fatal(err)
		}
		var receipt conversation.Receipt
		if err := json.Unmarshal(encoded, &receipt); err != nil || receipt.Replayed != (index == 1) {
			t.Fatalf("receipt %d = %#v, %v", index, receipt, err)
		}
	}
	persisted, err := rows.List(ctx, "work", "notes", 10)
	if err != nil || len(persisted) != 1 || persisted[0].Values["title"] != "Keep Route results locator-only" {
		t.Fatalf("persisted rows = %#v, %v", persisted, err)
	}
}
