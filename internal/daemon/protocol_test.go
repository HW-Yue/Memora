package daemon

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/ipc"
	"github.com/HW-Yue/Memora/internal/msql/executor"
)

// A client from another build must be refused by the daemon, before the daemon
// runs a single statement for it. The evidence is the database itself: the
// refused request carries a CREATE DATABASE, and nothing of it may reach disk.
func TestDaemonRefusesSkewedEngineProtocolBeforeExecuting(t *testing.T) {
	dataDir := t.TempDir()
	stop := startProtocolDaemon(t, dataDir)

	response := sendRawRequest(t, dataDir, ipc.Request{
		Version:        ipc.Version,
		EngineProtocol: ipc.EngineProtocol + 1,
		RequestID:      "skewed-create",
		Method:         "msql.execute",
		Payload:        executePayloadJSON(t, "CREATE DATABASE probe PURPOSE 'protocol skew' SCOPE 'tests'"),
	})

	if response.Error == nil {
		t.Fatalf("a skewed request was answered without an error; it ran: %s", response.Payload)
	}
	if response.Error.Code != ipc.CodeEngineProtocol {
		t.Fatalf("error code = %q, want %q (message %q)",
			response.Error.Code, ipc.CodeEngineProtocol, response.Error.Message)
	}

	stop()
	database, err := OpenStore(dataDir)
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer func() { _ = database.Close() }()
	databases, err := database.ShowDatabases(context.Background())
	if err != nil {
		t.Fatalf("ShowDatabases() error = %v", err)
	}
	if len(databases) != 0 {
		t.Fatalf("the refused statement still ran: databases = %v", databaseNames(databases))
	}
}

// The control for the test above: the very same request, with the protocol this
// daemon speaks, does create the Database. Without it, "no rows" would also be
// what a statement that cannot run at all looks like.
func TestDaemonRunsTheSameRequestAtTheMatchingEngineProtocol(t *testing.T) {
	dataDir := t.TempDir()
	stop := startProtocolDaemon(t, dataDir)

	response := sendRawRequest(t, dataDir, ipc.Request{
		Version:        ipc.Version,
		EngineProtocol: ipc.EngineProtocol,
		RequestID:      "matching-create",
		Method:         "msql.execute",
		Payload:        executePayloadJSON(t, "CREATE DATABASE probe PURPOSE 'protocol skew' SCOPE 'tests'"),
	})
	if response.Error != nil {
		t.Fatalf("matching request failed: %+v", response.Error)
	}

	stop()
	database, err := OpenStore(dataDir)
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer func() { _ = database.Close() }()
	databases, err := database.ShowDatabases(context.Background())
	if err != nil {
		t.Fatalf("ShowDatabases() error = %v", err)
	}
	if len(databases) != 1 || databases[0].Name != "probe" {
		t.Fatalf("databases = %v, want [probe]", databaseNames(databases))
	}
}

func databaseNames(databases []catalog.Database) []string {
	names := make([]string, 0, len(databases))
	for _, database := range databases {
		names = append(names, database.Name)
	}
	return names
}

func executePayloadJSON(t *testing.T, source string) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(executePayload{Source: source, Statements: []executor.StatementInput{}})
	if err != nil {
		t.Fatalf("encode execute payload: %v", err)
	}
	return encoded
}

// sendRawRequest writes one frame the ipc client would never write: the client
// stamps its own engine protocol, and this test is about what the server does
// with a number it did not choose.
func sendRawRequest(t *testing.T, dataDir string, request ipc.Request) ipc.Response {
	t.Helper()
	path, err := SocketPath(dataDir)
	if err != nil {
		t.Fatalf("SocketPath() error = %v", err)
	}
	connection, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	defer func() { _ = connection.Close() }()
	if err := connection.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
		t.Fatalf("SetDeadline() error = %v", err)
	}
	if err := ipc.WriteFrame(connection, request, ipc.DefaultMaxFrameSize); err != nil {
		t.Fatalf("WriteFrame() error = %v", err)
	}
	var response ipc.Response
	if err := ipc.ReadFrame(connection, &response, ipc.DefaultMaxFrameSize); err != nil {
		t.Fatalf("ReadFrame() error = %v", err)
	}
	return response
}

// startProtocolDaemon serves one temporary instance in this process and returns
// the stop the test calls before it reads the database file directly.
func startProtocolDaemon(t *testing.T, dataDir string) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan State, 1)
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, dataDir, Identity{Version: "test", EngineProtocol: ipc.EngineProtocol}, ready)
	}()
	select {
	case <-ready:
	case err := <-done:
		cancel()
		t.Fatalf("daemon exited before it was ready: %v", err)
	case <-time.After(20 * time.Second):
		cancel()
		t.Fatal("daemon did not become ready")
	}
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("daemon exited with %v", err)
			}
		case <-time.After(20 * time.Second):
			t.Error("daemon did not stop")
		}
	}
	t.Cleanup(stop)
	return stop
}
