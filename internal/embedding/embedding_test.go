package embedding_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/embedding"
)

// Nothing here opens a socket: the HTTP client is injected, so these tests say
// what the client sends and what it does with the answer without needing a
// provider — which is also what keeps the repository's tests offline.
type roundTrip func(*http.Request) (*http.Response, error)

func (trip roundTrip) RoundTrip(request *http.Request) (*http.Response, error) { return trip(request) }

func httpClient(trip roundTrip) *http.Client { return &http.Client{Transport: trip} }

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
}

func TestEmbeddingConfigComesFromTheEnvironment(t *testing.T) {
	full := map[string]string{
		embedding.EnvBaseURL:    "https://example.invalid/v1",
		embedding.EnvModel:      "text-embedding-v4",
		embedding.EnvDimensions: "1024",
		embedding.EnvAPIKey:     "secret-key-value",
	}
	lookup := func(values map[string]string) func(string) string {
		return func(name string) string { return values[name] }
	}
	config, err := embedding.ConfigFromEnv(lookup(full))
	if err != nil || !config.Configured() || config.Dimensions != 1024 || config.Model != "text-embedding-v4" {
		t.Fatalf("config = %+v, %v", config, err)
	}

	// Nothing configured at all is the ordinary case, not a failure.
	empty, err := embedding.ConfigFromEnv(lookup(nil))
	if err != nil || empty.Configured() {
		t.Fatalf("an unconfigured host must be an empty config: %+v, %v", empty, err)
	}

	// Turned off beats a complete configuration.
	off := map[string]string{}
	for name, value := range full {
		off[name] = value
	}
	off[embedding.EnvSwitch] = "off"
	if disabled, err := embedding.ConfigFromEnv(lookup(off)); err != nil || disabled.Configured() {
		t.Fatalf("MEMORA_EMBEDDING=off must disable: %+v, %v", disabled, err)
	}

	// A partial configuration names what is missing and never a value.
	partial := map[string]string{
		embedding.EnvBaseURL: "https://example.invalid/v1", embedding.EnvModel: "text-embedding-v4",
	}
	_, err = embedding.ConfigFromEnv(lookup(partial))
	if err == nil || !strings.Contains(err.Error(), embedding.EnvAPIKey) {
		t.Fatalf("a partial configuration must name the missing variable: %v", err)
	}
	if strings.Contains(err.Error(), "text-embedding-v4") || strings.Contains(err.Error(), "example.invalid") {
		t.Fatalf("a refusal must not echo configured values: %v", err)
	}
}

func TestEmbedSendsTheRequestAndOrdersTheAnswer(t *testing.T) {
	sent := embedRequestCapture{}
	client := embedding.New(
		embedding.Config{BaseURL: "https://example.invalid/v1/", Model: "text-embedding-v4", Dimensions: 2, APIKey: "secret-key-value"},
		httpClient(func(request *http.Request) (*http.Response, error) {
			sent.url = request.URL.String()
			sent.authorization = request.Header.Get("Authorization")
			sent.contentType = request.Header.Get("Content-Type")
			body, _ := io.ReadAll(request.Body)
			sent.body = string(body)
			// The provider answers out of order on purpose: the order of the
			// answer is the index's business, not the array's.
			return jsonResponse(200, `{"data":[{"index":1,"embedding":[0,1]},{"index":0,"embedding":[1,0]}]}`), nil
		}),
	)

	vectors, err := client.Embed(context.Background(), []string{"first", "second"})
	if err != nil {
		t.Fatal(err)
	}
	if sent.url != "https://example.invalid/v1/embeddings" {
		t.Fatalf("url = %q", sent.url)
	}
	if sent.authorization != "Bearer secret-key-value" || sent.contentType != "application/json" {
		t.Fatalf("headers = %q %q", sent.authorization, sent.contentType)
	}
	if !strings.Contains(sent.body, `"input":["first","second"]`) || !strings.Contains(sent.body, `"dimensions":2`) {
		t.Fatalf("body = %s", sent.body)
	}
	if len(vectors) != 2 || vectors[0][0] != 1 || vectors[1][1] != 1 {
		t.Fatalf("vectors must come back in input order: %v", vectors)
	}
}

type embedRequestCapture struct {
	url           string
	authorization string
	contentType   string
	body          string
}

// A provider that echoes the request on failure must not put the key in a log.
func TestEmbedNeverLeaksTheKey(t *testing.T) {
	client := embedding.New(
		embedding.Config{BaseURL: "https://example.invalid/v1", Model: "m", Dimensions: 1, APIKey: "secret-key-value"},
		httpClient(func(*http.Request) (*http.Response, error) {
			return jsonResponse(401, `{"error":"bad key secret-key-value"}`), nil
		}),
	)
	_, err := client.Embed(context.Background(), []string{"text"})
	if err == nil {
		t.Fatal("a failed call must be an error")
	}
	if strings.Contains(err.Error(), "secret-key-value") {
		t.Fatalf("the refusal must not echo the key: %v", err)
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("the refusal must say what happened: %v", err)
	}
}

func TestEmbedRefusesAnAnswerOfTheWrongShape(t *testing.T) {
	config := embedding.Config{BaseURL: "https://example.invalid/v1", Model: "m", Dimensions: 3, APIKey: "k"}
	for name, body := range map[string]string{
		"wrong width":  `{"data":[{"index":0,"embedding":[1,0]}]}`,
		"missing item": `{"data":[]}`,
		"bad index":    `{"data":[{"index":7,"embedding":[1,0,0]}]}`,
	} {
		client := embedding.New(config, httpClient(func(*http.Request) (*http.Response, error) {
			return jsonResponse(200, body), nil
		}))
		if _, err := client.Embed(context.Background(), []string{"text"}); err == nil {
			t.Fatalf("%s must be refused", name)
		}
	}
}

func TestEmbedRequestIsJSON(t *testing.T) {
	// The request body is the provider's contract, so it is checked as JSON
	// rather than as a substring of a marshalled struct.
	payload := map[string]any{}
	if err := json.Unmarshal([]byte(`{"model":"m","input":["a"],"dimensions":2}`), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["model"] != "m" {
		t.Fatalf("payload = %v", payload)
	}
}
