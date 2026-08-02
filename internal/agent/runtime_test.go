package agent_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/HW-Yue/Memora/internal/agent"
	protocolmsql "github.com/HW-Yue/Memora/protocol/msql"
)

func TestNewRequiresMSQLExecutor(t *testing.T) {
	t.Parallel()

	if _, err := agent.New(agent.Dependencies{}); !errors.Is(err, agent.ErrInvalidDependencies) {
		t.Fatalf("New() error = %v, want ErrInvalidDependencies", err)
	}
}

func TestRuntimeValidatesAndForwardsOneVersionedMSQLRequest(t *testing.T) {
	t.Parallel()

	wantRequest := validRequest()
	wantEnvelope := validEnvelope("agent-request")
	fake := &recordingExecutor{envelope: wantEnvelope}
	runtime, err := agent.New(agent.Dependencies{MSQL: fake})
	if err != nil {
		t.Fatal(err)
	}
	got, err := runtime.ExecuteMSQL(context.Background(), wantRequest)
	if err != nil {
		t.Fatalf("ExecuteMSQL() error = %v", err)
	}
	if !reflect.DeepEqual(got, wantEnvelope) {
		t.Fatalf("Envelope = %#v, want %#v", got, wantEnvelope)
	}
	if fake.calls != 1 || !reflect.DeepEqual(fake.request, wantRequest) {
		t.Fatalf("executor calls = %d, request = %#v", fake.calls, fake.request)
	}
}

func TestRuntimeRejectsInvalidOutboundRequestWithoutCallingExecutor(t *testing.T) {
	t.Parallel()

	fake := &recordingExecutor{envelope: validEnvelope("unused")}
	runtime, err := agent.New(agent.Dependencies{MSQL: fake})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.ExecuteMSQL(context.Background(), protocolmsql.Request{
		Version: protocolmsql.RequestVersion,
		Source:  "SELECT * FROM work.notes LIMIT 1",
	})
	if !errors.Is(err, protocolmsql.ErrInvalidRequest) {
		t.Fatalf("ExecuteMSQL() error = %v, want ErrInvalidRequest", err)
	}
	if fake.calls != 0 {
		t.Fatalf("executor calls = %d, want 0", fake.calls)
	}
}

func TestRuntimeRejectsInvalidInboundEnvelope(t *testing.T) {
	t.Parallel()

	fake := &recordingExecutor{envelope: protocolmsql.Envelope{}}
	runtime, err := agent.New(agent.Dependencies{MSQL: fake})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.ExecuteMSQL(context.Background(), validRequest())
	if !errors.Is(err, protocolmsql.ErrInvalidEnvelope) {
		t.Fatalf("ExecuteMSQL() error = %v, want ErrInvalidEnvelope", err)
	}
	if fake.calls != 1 {
		t.Fatalf("executor calls = %d, want 1", fake.calls)
	}
}

func TestRuntimeReturnsExecutorErrorWithoutRetry(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("provider boundary failed")
	fake := &recordingExecutor{err: wantErr}
	runtime, err := agent.New(agent.Dependencies{MSQL: fake})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.ExecuteMSQL(context.Background(), validRequest())
	if !errors.Is(err, wantErr) {
		t.Fatalf("ExecuteMSQL() error = %v, want %v", err, wantErr)
	}
	if fake.calls != 1 {
		t.Fatalf("executor calls = %d, want exactly 1", fake.calls)
	}
}

type recordingExecutor struct {
	calls    int
	request  protocolmsql.Request
	envelope protocolmsql.Envelope
	err      error
}

func (executor *recordingExecutor) ExecuteMSQL(
	_ context.Context,
	request protocolmsql.Request,
) (protocolmsql.Envelope, error) {
	executor.calls++
	executor.request = request
	return executor.envelope, executor.err
}

func validRequest() protocolmsql.Request {
	return protocolmsql.Request{
		Version: protocolmsql.RequestVersion,
		Source:  "SELECT title FROM work.notes LIMIT :limit",
		Statements: []protocolmsql.StatementInput{{
			Parameters: protocolmsql.Parameters{Named: map[string]any{"limit": int64(5)}},
			Authorization: protocolmsql.Authorization{
				Version: protocolmsql.AuthorizationVersion, Actor: "agent:test",
				AuthorizedDatabases: []string{"work"}, DefaultLevel: protocolmsql.LevelRead,
			},
		}},
	}
}

func validEnvelope(requestID string) protocolmsql.Envelope {
	return protocolmsql.Envelope{
		Version: protocolmsql.EnvelopeVersion, RequestID: requestID, OK: true,
		Results: []protocolmsql.StatementResult{{
			Index: 0, Statement: "SELECT", Source: "SELECT title FROM work.notes LIMIT :limit",
			Status: "succeeded", Columns: []protocolmsql.Column{}, Rows: []protocolmsql.Row{},
			Warnings: []protocolmsql.Notice{},
		}},
		Warnings: []protocolmsql.Notice{},
	}
}
