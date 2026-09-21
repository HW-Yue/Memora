package adminapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/adminapi"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
)

// The search endpoint is the one place a vector query can be asked for: the page
// is a read-only MSQL client and the engine never calls a model. These tests pin
// what it does with a provider, without one, and when the provider fails — the
// three cases the page has to tell apart.

type stubEmbedder struct {
	dimensions int
	failure    error
	seen       []string
}

func (stub *stubEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	stub.seen = append(stub.seen, texts...)
	if stub.failure != nil {
		return nil, stub.failure
	}
	vectors := make([][]float32, 0, len(texts))
	for range texts {
		vector := make([]float32, stub.dimensions)
		vector[0] = 1
		vectors = append(vectors, vector)
	}
	return vectors, nil
}
func (stub *stubEmbedder) Model() string   { return "text-embedding-v4" }
func (stub *stubEmbedder) Dimensions() int { return stub.dimensions }

type searchHarness struct {
	gateway *adminapi.Gateway
	origin  string
	csrf    string
	client  *http.Client
	sources []string
}

func newSearchGateway(t *testing.T, embedder adminapi.Embedder, vectorFails bool) *searchHarness {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	// The session lives in a cookie the bootstrap sets, so the client has to keep
	// it — exactly like the browser this endpoint serves.
	harness := &searchHarness{client: &http.Client{Jar: jar}}
	execute := func(_ context.Context, _, source string, _ []executor.StatementInput) (result.Envelope, error) {
		harness.sources = append(harness.sources, source)
		statement := result.NewStatement(0, "RECALL", source)
		statement.Status = result.StatusSucceeded
		if strings.Contains(source, "NEAREST") && vectorFails {
			statement.Status = result.StatusFailed
			statement.Error = &result.ResultError{
				Code: result.CodeValidation, Message: `database "memora" has no vector identity yet`}
			return result.Envelope{Version: result.Version, RequestID: "t", OK: true,
				Results: []result.StatementResult{statement}}, nil
		}
		statement.Rows = []result.Row{{
			"database": "memora", "table": "modules", "kind": "leaf", "object_id": "row_1",
			"path": json.RawMessage(`[{"name":"检索","route_id":"route_a"}]`),
		}}
		return result.Envelope{Version: result.Version, RequestID: "t", OK: true,
			Results: []result.StatementResult{statement}}, nil
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := adminapi.Start(context.Background(), adminapi.Config{
		DataDir: t.TempDir(), Scopes: []string{"memora"}, Execute: execute,
		Embedder: embedder, Listen: func(string, string) (net.Listener, error) { return listener, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gateway.Close() })
	harness.gateway = gateway
	harness.origin = gateway.Descriptor().Origin
	token := strings.TrimPrefix(gateway.Descriptor().URL, harness.origin+"/#token=")
	harness.csrf = harness.bootstrap(t, token)
	return harness
}

func (harness *searchHarness) bootstrap(t *testing.T, token string) string {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, harness.origin+"/api/v1/session", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", harness.origin)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := harness.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("bootstrap = %s", response.Status)
	}
	var receipt struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&receipt); err != nil {
		t.Fatal(err)
	}
	return receipt.CSRFToken
}

func (harness *searchHarness) search(t *testing.T, body string) (int, map[string]any) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, harness.origin+"/api/v1/search", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", harness.origin)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Memora-CSRF", harness.csrf)
	response, err := harness.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	payload := map[string]any{}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, payload
}

func vectorOf(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	vector, ok := payload["vector"].(map[string]any)
	if !ok {
		t.Fatalf("receipt has no vector status: %v", payload)
	}
	return vector
}

func TestSearchFusesBothArmsWhenTheHostHasAProvider(t *testing.T) {
	embedder := &stubEmbedder{dimensions: 2}
	harness := newSearchGateway(t, embedder, false)

	status, payload := harness.search(t, `{"database":"memora","query":"换嵌入模型时向量身份怎么处理"}`)
	if status != http.StatusOK {
		t.Fatalf("search = %d %v", status, payload)
	}
	if len(embedder.seen) != 1 || embedder.seen[0] != "换嵌入模型时向量身份怎么处理" {
		t.Fatalf("the gateway must embed the query: %v", embedder.seen)
	}
	vector := vectorOf(t, payload)
	if vector["ran"] != true || vector["model"] != "text-embedding-v4" || vector["dimensions"] != float64(2) {
		t.Fatalf("vector status = %v", vector)
	}
	// One fused statement, and the receipt says exactly what was run: a read-only
	// observer has to be reproducible.
	source, _ := payload["source"].(string)
	if len(harness.sources) != 1 || !strings.Contains(harness.sources[0], "NEAREST") {
		t.Fatalf("sources = %v", harness.sources)
	}
	if source != harness.sources[0] || !strings.Contains(source, "LIMIT :limit") {
		t.Fatalf("receipt source = %q, ran %v", source, harness.sources)
	}
	if rows, ok := payload["rows"].([]any); !ok || len(rows) != 1 {
		t.Fatalf("rows = %v", payload["rows"])
	}
}

func TestSearchSaysSoWhenNobodyConfiguredAProvider(t *testing.T) {
	harness := newSearchGateway(t, nil, false)
	status, payload := harness.search(t, `{"database":"memora","query":"rekey"}`)
	if status != http.StatusOK {
		t.Fatalf("search = %d %v", status, payload)
	}
	vector := vectorOf(t, payload)
	if vector["ran"] != false || vector["reason"] != "not_configured" {
		t.Fatalf("vector status = %v", vector)
	}
	if len(harness.sources) != 1 || strings.Contains(harness.sources[0], "NEAREST") {
		t.Fatalf("an unconfigured host must run the keyword arm alone: %v", harness.sources)
	}
}

func TestSearchDistinguishesAProviderFailureFromAnUnreadyDatabase(t *testing.T) {
	// The provider is down: a transient condition, reported as one.
	down := newSearchGateway(t, &stubEmbedder{dimensions: 2, failure: errors.New("connection refused")}, false)
	_, payload := down.search(t, `{"database":"memora","query":"rekey"}`)
	if vector := vectorOf(t, payload); vector["ran"] != false || vector["reason"] != "provider_unavailable" {
		t.Fatalf("provider failure = %v", vector)
	}
	if !strings.Contains(vectorOf(t, payload)["detail"].(string), "connection refused") {
		t.Fatalf("the receipt must carry why: %v", payload)
	}

	// The provider answers, but this Database cannot answer a vector query: a
	// different, stable condition.
	unready := newSearchGateway(t, &stubEmbedder{dimensions: 2}, true)
	status, payload := unready.search(t, `{"database":"memora","query":"rekey"}`)
	if status != http.StatusOK {
		t.Fatalf("search = %d %v", status, payload)
	}
	if vector := vectorOf(t, payload); vector["ran"] != false || vector["reason"] != "vector_not_ready" {
		t.Fatalf("unready database = %v", vector)
	}
	if len(unready.sources) != 2 || !strings.Contains(unready.sources[0], "NEAREST") ||
		strings.Contains(unready.sources[1], "NEAREST") {
		t.Fatalf("the fallback must be a keyword statement: %v", unready.sources)
	}
	if rows, ok := payload["rows"].([]any); !ok || len(rows) != 1 {
		t.Fatalf("the keyword listing is still the answer: %v", payload["rows"])
	}
}

func TestSearchNeedsADatabaseAndAQuery(t *testing.T) {
	harness := newSearchGateway(t, nil, false)
	for _, body := range []string{`{}`, `{"database":"memora"}`, `{"query":"rekey"}`, `{"database":" ","query":" "}`} {
		if status, _ := harness.search(t, body); status != http.StatusBadRequest {
			t.Fatalf("%s = %d, want 400", body, status)
		}
	}
	if status, _ := harness.search(t, fmt.Sprintf(`{"database":"memora","query":"rekey","extra":%d}`, 1)); status != http.StatusBadRequest {
		t.Fatalf("an unknown field must be refused: %d", status)
	}
}
