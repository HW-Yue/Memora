package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/ipc"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/security"
)

// One request that opens a transaction and never closes it must cost exactly
// that: one session holding the write lock. It used to cost the instance —
// the audit written on the way out of that very request went for the lock the
// request itself was holding, and every request after it, ping and doctor
// included, queued behind that.
func TestAnOpenTransactionLeavesTheDaemonAnswering(t *testing.T) {
	dataDir := t.TempDir()
	serveTestInstance(t, dataDir)

	holder := dialTestDaemon(t, dataDir)
	envelope := executeOnTestDaemon(t, holder, "BEGIN")
	if !envelope.OK {
		t.Fatalf("BEGIN failed: %+v", envelope)
	}

	other := dialTestDaemon(t, dataDir)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var pong struct {
		Message string `json:"message"`
	}
	if err := other.Call(ctx, "ping", nil, &pong); err != nil {
		t.Fatalf("ping while a transaction is open: %v", err)
	}
	if pong.Message != "pong" {
		t.Fatalf("ping answered %q, want pong", pong.Message)
	}
	var report DoctorReport
	if err := other.Call(ctx, "doctor", nil, &report); err != nil {
		t.Fatalf("doctor while a transaction is open: %v", err)
	}
	if report.Status == "" {
		t.Fatalf("doctor answered an empty report: %+v", report)
	}
}

// The audit of the work done inside a transaction belongs to that transaction:
// it is visible when the transaction commits and gone when it rolls back, the
// same way the Row it describes is. An audit written beside the transaction
// would outlive a rollback and record a write that never landed.
func TestAuditOfATransactionEndsWithIt(t *testing.T) {
	tests := []struct {
		name   string
		close  string
		rows   int
		events int
	}{
		// Three setup requests, each audited on its own transaction, plus the
		// closing request, whose audit is written after the transaction has
		// already ended. BEGIN and INSERT are the two inside it.
		{name: "rollback", close: "ROLLBACK", rows: 0, events: 4},
		{name: "commit", close: "COMMIT", rows: 1, events: 6},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dataDir := t.TempDir()
			stop := serveTestInstance(t, dataDir)
			client := dialTestDaemon(t, dataDir)

			for _, source := range []string{
				"CREATE DATABASE work PURPOSE 'Work memory' SCOPE 'Projects'",
				"CREATE TABLE work.notes PURPOSE 'Notes' ROW SEMANTICS 'One fact' " +
					"(title TEXT NOT NULL PURPOSE 'Title' ROLE title)",
			} {
				if envelope := executeOnTestDaemon(t, client, source); !envelope.OK {
					t.Fatalf("%q failed: %s", source, statementErrors(envelope))
				}
			}
			root := executeOnTestDaemon(t, client,
				"CREATE ROUTE ROOT FOR TABLE work.notes PURPOSE 'Everything'", mutation())
			if !root.OK {
				t.Fatalf("CREATE ROUTE ROOT failed: %s", statementErrors(root))
			}
			if envelope := executeOnTestDaemon(t, client, "BEGIN"); !envelope.OK {
				t.Fatalf("BEGIN failed: %s", statementErrors(envelope))
			}
			insert := executeOnTestDaemon(t, client,
				"INSERT INTO work.notes (title) VALUES ('written inside a transaction')",
				mountedInsert())
			if !insert.OK {
				t.Fatalf("INSERT failed: %s", statementErrors(insert))
			}
			if envelope := executeOnTestDaemon(t, client, test.close); !envelope.OK {
				t.Fatalf("%s failed: %s", test.close, statementErrors(envelope))
			}
			if err := client.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
			stop()

			database, err := OpenStore(dataDir)
			if err != nil {
				t.Fatalf("OpenStore() error = %v", err)
			}
			defer func() { _ = database.Close() }()
			report, err := database.Doctor(context.Background())
			if err != nil {
				t.Fatalf("Doctor() error = %v", err)
			}
			if report.Rows != test.rows {
				t.Fatalf("live Rows = %d, want %d", report.Rows, test.rows)
			}
			events, err := security.New(database.KV("security"), security.Options{}).
				Events(context.Background())
			if err != nil {
				t.Fatalf("Events() error = %v", err)
			}
			if len(events) != test.events {
				t.Fatalf("audit events = %d, want %d: %v", len(events), test.events, events)
			}
		})
	}
}

// mutation is the envelope every write carries: who, why, and how much it may
// touch.
func mutation() executor.StatementInput {
	return executor.StatementInput{Mutation: executor.MutationOptions{
		Actor: "agent:test", Source: "daemon-test", Reason: "audit and its transaction",
		SourceKind: "conversation_assertion", MaxAffectedRows: 1, ExpectedSchemaVersion: 1,
	}}
}

// mountedInsert adds the leaf the Row is mounted under.
func mountedInsert() executor.StatementInput {
	input := mutation()
	input.Mutation.RoutePath = []router.PathSegment{
		{Name: "transactions", Kind: router.KindLeaf, Purpose: "What a transaction covers"},
	}
	return input
}

func statementErrors(envelope result.Envelope) string {
	messages := []string{}
	if envelope.Error != nil {
		messages = append(messages, string(envelope.Error.Code)+": "+envelope.Error.Message)
	}
	for _, statement := range envelope.Results {
		if statement.Error != nil {
			messages = append(messages, string(statement.Error.Code)+": "+statement.Error.Message)
		}
	}
	return strings.Join(messages, "; ")
}

func executeOnTestDaemon(
	t *testing.T, client *ipc.Client, source string, statements ...executor.StatementInput,
) result.Envelope {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var envelope result.Envelope
	payload := executePayload{Source: source, Statements: statements}
	if err := client.Call(ctx, "msql.execute", payload, &envelope); err != nil {
		t.Fatalf("execute %q: %v", source, err)
	}
	return envelope
}

func dialTestDaemon(t *testing.T, dataDir string) *ipc.Client {
	t.Helper()
	path, err := SocketPath(dataDir)
	if err != nil {
		t.Fatalf("SocketPath() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := ipc.Dial(ctx, path)
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// serveTestInstance serves one temporary instance in this process and returns the
// stop a test calls before it reads the database file for itself.
func serveTestInstance(t *testing.T, dataDir string) func() {
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
