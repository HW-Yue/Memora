package daemon

import (
	"context"
	"net"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/ipc"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
	sqlitestore "github.com/HW-Yue/Memora/internal/store/sqlite"
)

func TestExecuteHandlerKeepsTransactionPerConnectionAndRollsBackOnDisconnect(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, err := sqlitestore.Open(filepath.Join(t.TempDir(), "database.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	dictionary := catalog.New(databaseStore, catalog.Options{})
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
		},
	}); err != nil {
		t.Fatal(err)
	}
	rows := row.New(databaseStore, dictionary, row.Options{})
	handler := newDatabaseHandler(ctx, dictionary, rows, databaseStore)
	defer handler.Close()
	server := ipc.NewServer(handler)
	defer server.Close()
	serverConnection, clientConnection := net.Pipe()
	done := make(chan struct{})
	go func() {
		server.ServeConnection(serverConnection)
		close(done)
	}()
	client := ipc.NewClient(clientConnection)

	var first result.Envelope
	if err := client.Call(ctx, "msql.execute", executePayload{
		Source: "BEGIN; INSERT INTO work.notes (title, count) VALUES ('pending', :count)",
		Statements: []executor.StatementInput{
			{},
			{
				Parameters: executor.Parameters{Named: map[string]any{"count": int64(7)}},
				Mutation: executor.MutationOptions{
					ExpectedSchemaVersion: 1, MaxAffectedRows: 1,
					Actor: "agent:test", Source: "ipc:test", Reason: "verify IPC history metadata",
				},
			},
		},
	}, &first); err != nil {
		t.Fatalf("execute first request: %v", err)
	}
	if len(first.Results) != 2 || first.Results[1].Status != result.StatusSucceeded ||
		first.Results[1].CommitSequence == nil || *first.Results[1].CommitSequence != 1 {
		t.Fatalf("first envelope = %#v", first)
	}

	var second result.Envelope
	if err := client.Call(ctx, "msql.execute", executePayload{
		Source: "SELECT title FROM work.notes LIMIT 10",
	}, &second); err != nil {
		t.Fatalf("execute second request: %v", err)
	}
	if len(second.Results) != 1 || len(second.Results[0].Rows) != 1 ||
		second.Results[0].Rows[0]["title"] != "pending" {
		t.Fatalf("second envelope = %#v", second)
	}

	if err := client.Close(); err != nil {
		t.Fatalf("client Close() error = %v", err)
	}
	<-done
	persisted, err := rows.List(ctx, "work", "notes", 10)
	if err != nil || len(persisted) != 0 {
		t.Fatalf("rows after disconnect = %#v, %v", persisted, err)
	}
}
