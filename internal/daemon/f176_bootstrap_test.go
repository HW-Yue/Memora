package daemon_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/agent"
	"github.com/HW-Yue/Memora/internal/daemon"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	protocolmsql "github.com/HW-Yue/Memora/protocol/msql"
	"github.com/HW-Yue/Memora/sdk/memora"
)

func TestQueryBootstrapRunsAgainstNativeDaemonThroughMSQLOnlyAdapter(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "instance")
	runContext, stop := context.WithCancel(context.Background())
	ready, done := make(chan daemon.State, 1), make(chan error, 1)
	go func() { done <- daemon.Run(runContext, dataDir, ready) }()
	<-ready
	t.Cleanup(func() {
		stop()
		if err := <-done; err != nil {
			t.Errorf("daemon.Run() error = %v", err)
		}
	})

	ctx := context.Background()
	database := executeBootstrapSeed(t, ctx, dataDir,
		"CREATE DATABASE work PURPOSE 'Bootstrap knowledge' SCOPE 'Projects'", nil)
	databaseID := bootstrapText(t, database.Results[0].Rows[0], "database_id")
	table := executeBootstrapSeed(t, ctx, dataDir,
		"CREATE TABLE work.notes PURPOSE 'Bootstrap service notes' ROW SEMANTICS 'One note' "+
			"(title TEXT(100) NOT NULL PURPOSE 'Title')", nil)
	tableID := bootstrapText(t, table.Results[0].Rows[0], "table_id")
	executeBootstrapSeed(t, ctx, dataDir,
		"CREATE ROUTE ROOT FOR TABLE work.notes PURPOSE 'Bootstrap navigation'",
		[]executor.StatementInput{{Mutation: executor.MutationOptions{MaxAffectedRows: 1}}},
	)

	client, err := memora.Dial(ctx, memora.DialOptions{DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	assembler, err := agent.NewBootstrapAssembler(sdkBootstrapAdapter{client: client})
	if err != nil {
		t.Fatal(err)
	}
	frame, err := assembler.Assemble(ctx, agent.BootstrapRequest{
		Query: "bootstrap navigation",
		Authorization: protocolmsql.Authorization{
			Version: protocolmsql.AuthorizationVersion, Actor: "agent:bootstrap-integration",
			AuthorizedDatabases: []string{"work"}, DefaultLevel: protocolmsql.LevelRead,
		},
		Budget: agent.DefaultBootstrapBudget(),
	})
	if err != nil {
		t.Fatalf("Assemble() error = %v", err)
	}
	if !frame.Catalog.Complete || len(frame.Catalog.Entries) != 2 || len(frame.Lexical.Locations) == 0 {
		t.Fatalf("Bootstrap Frame = %#v", frame)
	}
	root, found := frame.Root(databaseID, tableID)
	if !found || root.Status != agent.RootPrefetchSucceeded || root.Snapshot == "" {
		t.Fatalf("native root prefetch = %#v, found=%t", root, found)
	}
}

type sdkBootstrapAdapter struct {
	client *memora.Client
}

func (adapter sdkBootstrapAdapter) ExecuteMSQL(
	ctx context.Context,
	request protocolmsql.Request,
) (protocolmsql.Envelope, error) {
	return adapter.client.Execute(ctx, request)
}

func executeBootstrapSeed(
	t *testing.T,
	ctx context.Context,
	dataDir, source string,
	statements []executor.StatementInput,
) result.Envelope {
	t.Helper()
	envelope, err := daemon.Execute(ctx, dataDir, source, statements)
	if err != nil || !envelope.OK || len(envelope.Results) != 1 {
		t.Fatalf("Execute(%q) = %#v, %v", source, envelope, err)
	}
	return envelope
}

func bootstrapText(t *testing.T, row result.Row, field string) string {
	t.Helper()
	value, ok := row[field].(string)
	if !ok || value == "" {
		t.Fatalf("%s missing from %#v", field, row)
	}
	return value
}
