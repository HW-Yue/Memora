package answerbenchmark_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/HW-Yue/Memora/internal/answerbenchmark"
	"github.com/HW-Yue/Memora/internal/answercorpus"
	"github.com/HW-Yue/Memora/internal/daemon"
	"github.com/HW-Yue/Memora/internal/instance"
	protocolmsql "github.com/HW-Yue/Memora/protocol/msql"
	"github.com/HW-Yue/Memora/sdk/memora"
)

func TestMaterializerLowersPublicFixtureThroughDaemonMSQL(t *testing.T) {
	ctx := context.Background()
	executor, closeInstance := cleanDaemonExecutor(t, ctx)
	defer closeInstance()
	bundle, err := answercorpus.Generate()
	if err != nil {
		t.Fatal(err)
	}
	materializer, err := answerbenchmark.NewMaterializer(executor)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := materializer.Materialize(ctx, bundle.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Version != answerbenchmark.MaterializationReceiptVersion || receipt.Hash == "" ||
		receipt.MSQLRequests == 0 || receipt.MSQLStatements < receipt.MSQLRequests || len(receipt.Objects) != 46 {
		t.Fatalf("MaterializationReceipt = %#v", receipt)
	}

	runtimeRoute, ok := receipt.Mapping(answerbenchmark.ObjectRoute, "route_projects_13_runtime")
	if !ok || runtimeRoute.Revision != 2 {
		t.Fatalf("runtime Route mapping = %#v, %v", runtimeRoute, ok)
	}
	described := executeRead(t, ctx, executor, "DESCRIBE ROUTE :route", map[string]any{"route": runtimeRoute.ActualID}, []string{"projects"})
	if len(described.Results) != 1 || len(described.Results[0].Rows) != 1 ||
		!reflect.DeepEqual(textList(described.Results[0].Rows[0]["aliases"]), []string{"Runtime", "agent loop", "运行时"}) {
		t.Fatalf("materialized Route aliases = %#v", described)
	}

	runtimeRow, ok := receipt.Mapping(answerbenchmark.ObjectRow, "row_projects_runtime")
	if !ok || runtimeRow.Revision != 1 {
		t.Fatalf("runtime Row mapping = %#v, %v", runtimeRow, ok)
	}
	selected := executeRead(t, ctx, executor,
		"SELECT decision FROM projects.decisions WHERE row_id = :row LIMIT 1",
		map[string]any{"row": runtimeRow.ActualID}, []string{"projects"})
	if len(selected.Results) != 1 || len(selected.Results[0].Rows) != 1 ||
		selected.Results[0].Rows[0]["decision"] != "Use a Memora-owned thin Go loop" {
		t.Fatalf("materialized Row = %#v", selected)
	}
}

type sdkExecutor struct{ client *memora.Client }

func (executor *sdkExecutor) ExecuteMSQL(ctx context.Context, request protocolmsql.Request) (protocolmsql.Envelope, error) {
	return executor.client.Execute(ctx, request)
}

func cleanDaemonExecutor(t *testing.T, ctx context.Context) (*sdkExecutor, func()) {
	t.Helper()
	dataDir := t.TempDir()
	if _, err := instance.Initialize(ctx, dataDir, instance.Options{}); err != nil {
		t.Fatal(err)
	}
	daemonContext, cancel := context.WithCancel(ctx)
	ready := make(chan daemon.State, 1)
	done := make(chan error, 1)
	go func() { done <- daemon.Run(daemonContext, dataDir, ready) }()
	select {
	case <-ready:
	case err := <-done:
		cancel()
		t.Fatalf("daemon.Run() before ready = %v", err)
	}
	client, err := memora.Dial(ctx, memora.DialOptions{DataDir: dataDir})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	return &sdkExecutor{client: client}, func() {
		_ = client.Close()
		cancel()
		err := <-done
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("daemon.Run() shutdown = %v", err)
		}
	}
}

func executeRead(
	t *testing.T,
	ctx context.Context,
	executor *sdkExecutor,
	source string,
	parameters map[string]any,
	databases []string,
) protocolmsql.Envelope {
	t.Helper()
	envelope, err := executor.ExecuteMSQL(ctx, protocolmsql.Request{
		Version: protocolmsql.RequestVersion, Source: source,
		Statements: []protocolmsql.StatementInput{{
			Parameters: protocolmsql.Parameters{Named: parameters},
			Authorization: protocolmsql.Authorization{
				Version: protocolmsql.AuthorizationVersion, Actor: "benchmark:test",
				AuthorizedDatabases: databases, DefaultLevel: protocolmsql.LevelRead,
			},
		}},
	})
	if err != nil || !envelope.OK {
		t.Fatalf("ExecuteMSQL(%q) = %#v, %v", source, envelope, err)
	}
	return envelope
}

func textList(value any) []string {
	values, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		text, ok := value.(string)
		if !ok {
			return nil
		}
		result = append(result, text)
	}
	return result
}
