package memora_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/HW-Yue/Memora/internal/ipc"
	"github.com/HW-Yue/Memora/sdk/memora"
)

func TestClientExecuteUsesVersionedMSQLWireContract(t *testing.T) {
	t.Parallel()

	transport := &fakeTransport{}
	client, err := memora.NewClient(transport)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	request := validRequest()
	envelope, err := client.Execute(context.Background(), request)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if transport.method != "msql.execute" || transport.source != request.Source || transport.statementCount != 1 {
		t.Fatalf("wire call = %q, %q, %d", transport.method, transport.source, transport.statementCount)
	}
	if envelope.Version != memora.EnvelopeVersion || !envelope.OK || len(envelope.Results) != 1 {
		t.Fatalf("envelope = %#v", envelope)
	}
	if len(envelope.RawJSON()) == 0 {
		t.Fatal("RawJSON() is empty")
	}
}

func TestClientRejectsInvalidRequestBeforeTransport(t *testing.T) {
	t.Parallel()

	transport := &fakeTransport{}
	client, _ := memora.NewClient(transport)
	request := validRequest()
	request.Version = "future"
	if _, err := client.Execute(context.Background(), request); !errors.Is(err, memora.ErrInvalidRequest) {
		t.Fatalf("Execute() error = %v", err)
	}
	if transport.calls != 0 {
		t.Fatalf("transport calls = %d", transport.calls)
	}
}

func TestClientConvertsRemoteErrorAndClosesIdempotently(t *testing.T) {
	t.Parallel()

	transport := &fakeTransport{callErr: &ipc.RemoteError{Code: "permission_denied", Message: "denied"}}
	client, _ := memora.NewClient(transport)
	_, err := client.Execute(context.Background(), validRequest())
	var remote *memora.RemoteError
	if !errors.As(err, &remote) || remote.Code != "permission_denied" {
		t.Fatalf("Execute() error = %#v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil || transport.closes != 1 {
		t.Fatalf("second Close() = %v, closes = %d", err, transport.closes)
	}
	if _, err := client.Execute(context.Background(), validRequest()); !errors.Is(err, memora.ErrClientClosed) {
		t.Fatalf("Execute() after Close error = %v", err)
	}
}

func TestClientSupportsConcurrentExecute(t *testing.T) {
	t.Parallel()

	transport := &fakeTransport{}
	client, _ := memora.NewClient(transport)
	var wait sync.WaitGroup
	for index := 0; index < 16; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, err := client.Execute(context.Background(), validRequest()); err != nil {
				t.Errorf("Execute() error = %v", err)
			}
		}()
	}
	wait.Wait()
	if transport.calls != 16 {
		t.Fatalf("transport calls = %d", transport.calls)
	}
}

func TestMutationOptionsPreserveExplicitEmptyRouteMembership(t *testing.T) {
	t.Parallel()

	encoded, err := json.Marshal(memora.MutationOptions{RouteLeafIDs: []string{}, TargetRouteLeafIDs: [][]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"route_leaf_ids":[]`)) || !bytes.Contains(encoded, []byte(`"target_route_leaf_ids":[]`)) {
		t.Fatalf("encoded = %s", encoded)
	}
}

func TestDialRejectsRelativeDataDir(t *testing.T) {
	t.Parallel()

	if _, err := memora.Dial(context.Background(), memora.DialOptions{DataDir: "relative"}); !errors.Is(err, memora.ErrInvalidRequest) {
		t.Fatalf("Dial() error = %v", err)
	}
}

func validRequest() memora.Request {
	return memora.Request{
		Version: memora.RequestVersion,
		Source:  "SHOW DATABASES LIMIT 8",
		Statements: []memora.StatementInput{{
			Authorization: memora.Authorization{
				Version: memora.AuthorizationVersion,
				Actor:   "sdk:test", AuthorizedDatabases: []string{"work"}, DefaultLevel: memora.LevelRead,
			},
		}},
	}
}

type fakeTransport struct {
	mu             sync.Mutex
	method         string
	source         string
	statementCount int
	calls          int
	closes         int
	callErr        error
}

func (transport *fakeTransport) Call(_ context.Context, method string, payload, target any) error {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.calls++
	transport.method = method
	encoded, _ := json.Marshal(payload)
	var request struct {
		Source     string            `json:"source"`
		Statements []json.RawMessage `json:"statements"`
	}
	_ = json.Unmarshal(encoded, &request)
	transport.source = request.Source
	transport.statementCount = len(request.Statements)
	if transport.callErr != nil {
		return transport.callErr
	}
	response := map[string]any{
		"version": memora.EnvelopeVersion, "request_id": "daemon", "ok": true,
		"results": []any{map[string]any{
			"index": 0, "statement": request.Source, "source": "sdk", "status": "succeeded",
			"columns": []any{}, "rows": []any{}, "affected_rows": 0, "truncated": false, "warnings": []any{},
		}},
		"truncated": false, "warnings": []any{},
	}
	wire, _ := json.Marshal(response)
	return json.Unmarshal(wire, target)
}

func (transport *fakeTransport) Close() error {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.closes++
	return nil
}
