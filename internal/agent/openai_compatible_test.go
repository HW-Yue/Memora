package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/agent"
)

func TestOpenAICompatibleProviderMapsToolCallAndDropsReasoning(t *testing.T) {
	t.Parallel()

	const secret = "sk-fixture-never-log"
	var calls atomic.Int64
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.Method != http.MethodPost || request.URL.Path != "/v1/chat/completions" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer "+secret || request.Header.Get("Content-Type") != "application/json" {
			t.Error("request headers do not contain the expected private credential and JSON media type")
		}
		gotBody, _ = io.ReadAll(request.Body)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{
			"id":"completion-1","object":"chat.completion","model":"model-test",
			"choices":[{"index":0,"message":{"role":"assistant","content":"",
			"reasoning_content":"private chain of thought must disappear",
			"tool_calls":[{"id":"call-1","type":"function","function":{"name":"execute_msql","arguments":"{\"source\":\"SHOW DATABASES\"}"}}]},"finish_reason":"tool_calls"}],
			"usage":{"prompt_tokens":12,"completion_tokens":7,"total_tokens":19,"cached_tokens":3}
		}`)
	}))
	defer server.Close()

	secrets := &scriptedSecretResolver{values: []string{secret}}
	provider, err := agent.NewOpenAICompatibleProvider(openAICompatibleConfig(server.URL+"/v1"), secrets)
	if err != nil {
		t.Fatal(err)
	}
	if secrets.callCount() != 0 || calls.Load() != 0 {
		t.Fatal("constructor resolved a secret or made a network request")
	}
	request := providerRequest([]agent.ProviderMessage{{Role: agent.ProviderRoleUser, Content: "Find the database"}})
	response, err := provider.Complete(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	want := agent.ProviderResponse{
		Version: agent.ProviderResponseVersion,
		Model:   "model-test",
		Message: agent.ProviderMessage{
			Role: agent.ProviderRoleAssistant,
			ToolCalls: []agent.ProviderToolCall{{
				ID: "call-1", Name: "execute_msql", Arguments: json.RawMessage(`{"source":"SHOW DATABASES"}`),
			}},
		},
		FinishReason: agent.ProviderFinishToolCalls,
		Usage: agent.ProviderUsage{
			InputTokens: 12, CachedInputTokens: 3, OutputTokens: 7, TotalTokens: 19,
		},
	}
	if !reflect.DeepEqual(response, want) {
		t.Fatalf("Complete() = %#v, want %#v", response, want)
	}
	if calls.Load() != 1 || secrets.callCount() != 1 {
		t.Fatalf("HTTP calls = %d, secret calls = %d", calls.Load(), secrets.callCount())
	}
	wantBody := `{"model":"model-test","messages":[{"role":"user","content":"Find the database"}],"tools":[{"type":"function","function":{"name":"execute_msql","description":"Execute one MSQL request","parameters":{"type":"object","additionalProperties":false}}}],"tool_choice":"auto","max_completion_tokens":1024,"stream":false}`
	if string(gotBody) != wantBody {
		t.Fatalf("request body = %s\nwant = %s", gotBody, wantBody)
	}
	encoded, _ := json.Marshal(response)
	if strings.Contains(string(encoded), "private chain of thought") {
		t.Fatalf("Provider response leaked reasoning_content: %s", encoded)
	}
}

func TestOpenAICompatibleProviderMapsMultiturnToolResultAndCachedUsage(t *testing.T) {
	t.Parallel()

	request := providerRequest([]agent.ProviderMessage{
		{Role: agent.ProviderRoleUser, Content: "Find it"},
		{Role: agent.ProviderRoleAssistant, ToolCalls: []agent.ProviderToolCall{{
			ID: "call-1", Name: "execute_msql", Arguments: json.RawMessage(`{"source":"SELECT 1"}`),
		}}},
		{Role: agent.ProviderRoleTool, ToolCallID: "call-1", Content: `{"ok":true}`},
	})
	transport := roundTripFunc(func(httpRequest *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(httpRequest.Body)
		var wire map[string]any
		if err := json.Unmarshal(body, &wire); err != nil {
			t.Fatal(err)
		}
		messages := wire["messages"].([]any)
		assistant := messages[1].(map[string]any)
		toolCall := assistant["tool_calls"].([]any)[0].(map[string]any)
		function := toolCall["function"].(map[string]any)
		toolResult := messages[2].(map[string]any)
		if function["arguments"] != `{"source":"SELECT 1"}` || toolResult["tool_call_id"] != "call-1" ||
			toolResult["content"] != `{"ok":true}` {
			t.Errorf("wire messages = %#v", messages)
		}
		return jsonHTTPResponse(http.StatusOK, `{
			"model":"model-test","choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":20,"completion_tokens":2,"total_tokens":22,"prompt_tokens_details":{"cached_tokens":8}}
		}`), nil
	})
	provider, err := agent.NewOpenAICompatibleProvider(
		openAICompatibleConfigWithTransport("https://provider.test/v1", transport),
		&scriptedSecretResolver{values: []string{"secret"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	response, err := provider.Complete(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if response.Message.Content != "done" || response.Usage.CachedInputTokens != 8 || response.Usage.TotalTokens != 22 {
		t.Fatalf("response = %#v", response)
	}
}

func TestOpenAICompatibleProviderResolvesRotatedSecretPerCall(t *testing.T) {
	t.Parallel()

	secrets := &scriptedSecretResolver{values: []string{"secret-one", "secret-two"}}
	var authorizations []string
	var mu sync.Mutex
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		mu.Lock()
		authorizations = append(authorizations, request.Header.Get("Authorization"))
		mu.Unlock()
		return validAnswerHTTPResponse(), nil
	})
	provider, err := agent.NewOpenAICompatibleProvider(
		openAICompatibleConfigWithTransport("https://provider.test/v1", transport), secrets,
	)
	if err != nil {
		t.Fatal(err)
	}
	request := providerRequest([]agent.ProviderMessage{{Role: agent.ProviderRoleUser, Content: "hello"}})
	for range 2 {
		if _, err := provider.Complete(context.Background(), request); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(authorizations, []string{"Bearer secret-one", "Bearer secret-two"}) {
		t.Fatal("Provider did not resolve and apply the rotated secret on each call")
	}
}

func TestOpenAICompatibleProviderRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	valid := openAICompatibleConfig("https://provider.test/v1")
	tests := []struct {
		name   string
		mutate func(*agent.OpenAICompatibleProviderConfig)
	}{
		{"missing URL", func(value *agent.OpenAICompatibleProviderConfig) { value.APIBaseURL = "" }},
		{"remote HTTP", func(value *agent.OpenAICompatibleProviderConfig) { value.APIBaseURL = "http://provider.test/v1" }},
		{"userinfo", func(value *agent.OpenAICompatibleProviderConfig) { value.APIBaseURL = "https://user@provider.test/v1" }},
		{"query", func(value *agent.OpenAICompatibleProviderConfig) {
			value.APIBaseURL = "https://provider.test/v1?q=secret"
		}},
		{"fragment", func(value *agent.OpenAICompatibleProviderConfig) {
			value.APIBaseURL = "https://provider.test/v1#fragment"
		}},
		{"missing secret name", func(value *agent.OpenAICompatibleProviderConfig) { value.SecretName = "" }},
		{"unsafe secret name", func(value *agent.OpenAICompatibleProviderConfig) { value.SecretName = "API KEY" }},
		{"negative timeout", func(value *agent.OpenAICompatibleProviderConfig) { value.Timeout = -time.Second }},
		{"tiny request budget", func(value *agent.OpenAICompatibleProviderConfig) { value.MaxRequestBytes = 1 }},
		{"tiny response budget", func(value *agent.OpenAICompatibleProviderConfig) { value.MaxResponseBytes = 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := valid
			test.mutate(&config)
			if _, err := agent.NewOpenAICompatibleProvider(config, &scriptedSecretResolver{values: []string{"secret"}}); !errors.Is(err, agent.ErrInvalidOpenAICompatibleProvider) {
				t.Fatalf("NewOpenAICompatibleProvider() error = %v", err)
			}
		})
	}
	if _, err := agent.NewOpenAICompatibleProvider(valid, nil); !errors.Is(err, agent.ErrInvalidOpenAICompatibleProvider) {
		t.Fatalf("nil resolver error = %v", err)
	}
	loopback := valid
	loopback.APIBaseURL = "http://127.0.0.1:3888/v1/"
	if _, err := agent.NewOpenAICompatibleProvider(loopback, &scriptedSecretResolver{values: []string{"secret"}}); err != nil {
		t.Fatalf("numeric loopback HTTP error = %v", err)
	}
}

func TestOpenAICompatibleProviderBoundsAndRedactsFailures(t *testing.T) {
	t.Parallel()

	const remoteSecret = "remote-body-secret"
	tests := []struct {
		name       string
		body       string
		status     int
		mutate     func(*agent.OpenAICompatibleProviderConfig)
		want       error
		wantStatus int
	}{
		{"http status", `{"error":{"message":"` + remoteSecret + `"}}`, http.StatusTooManyRequests, nil, agent.ErrProviderHTTPStatus, http.StatusTooManyRequests},
		{"malformed JSON", `{"secret":"` + remoteSecret + `"`, http.StatusOK, nil, agent.ErrProviderWireResponse, 0},
		{"trailing JSON", validAnswerJSON() + ` {}`, http.StatusOK, nil, agent.ErrProviderWireResponse, 0},
		{"multiple choices", strings.Replace(validAnswerJSON(), `"choices":[`, `"choices":[{"index":1},`, 1), http.StatusOK, nil, agent.ErrProviderWireResponse, 0},
		{"cached conflict", strings.Replace(validAnswerJSON(), `"total_tokens":3`, `"total_tokens":3,"cached_tokens":1,"prompt_tokens_details":{"cached_tokens":2}`, 1), http.StatusOK, nil, agent.ErrProviderWireResponse, 0},
		{"oversize", strings.Repeat("x", 2048), http.StatusOK, func(value *agent.OpenAICompatibleProviderConfig) { value.MaxResponseBytes = 1024 }, agent.ErrProviderResponseTooLarge, 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int64
			transport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				calls.Add(1)
				return jsonHTTPResponse(test.status, test.body), nil
			})
			config := openAICompatibleConfigWithTransport("https://provider.test/v1", transport)
			if test.mutate != nil {
				test.mutate(&config)
			}
			provider, err := agent.NewOpenAICompatibleProvider(config, &scriptedSecretResolver{values: []string{"secret"}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = provider.Complete(context.Background(), providerRequest([]agent.ProviderMessage{{Role: agent.ProviderRoleUser, Content: "hello"}}))
			if !errors.Is(err, test.want) || calls.Load() != 1 {
				t.Fatalf("Complete() error = %v, calls = %d", err, calls.Load())
			}
			if strings.Contains(fmt.Sprint(err), remoteSecret) {
				t.Fatalf("error leaked remote body: %v", err)
			}
			if test.wantStatus != 0 {
				var statusError *agent.ProviderHTTPStatusError
				if !errors.As(err, &statusError) || statusError.StatusCode != test.wantStatus {
					t.Fatalf("HTTP status error = %#v", statusError)
				}
			}
		})
	}
}

func TestOpenAICompatibleProviderRejectsOversizeBeforeSecretOrNetwork(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	secrets := &scriptedSecretResolver{values: []string{"secret"}}
	config := openAICompatibleConfigWithTransport("https://provider.test/v1", roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		calls.Add(1)
		return validAnswerHTTPResponse(), nil
	}))
	config.MaxRequestBytes = 256
	provider, err := agent.NewOpenAICompatibleProvider(config, secrets)
	if err != nil {
		t.Fatal(err)
	}
	request := providerRequest([]agent.ProviderMessage{{Role: agent.ProviderRoleUser, Content: strings.Repeat("x", 300)}})
	if _, err := provider.Complete(context.Background(), request); !errors.Is(err, agent.ErrProviderRequestTooLarge) {
		t.Fatalf("Complete() error = %v", err)
	}
	if secrets.callCount() != 0 || calls.Load() != 0 {
		t.Fatalf("oversize request resolved secret or used network: secret=%d HTTP=%d", secrets.callCount(), calls.Load())
	}
}

func TestOpenAICompatibleProviderRedactsResolverAndTransportErrors(t *testing.T) {
	t.Parallel()

	const leaked = "sk-should-never-cross-provider-boundary"
	request := providerRequest([]agent.ProviderMessage{{Role: agent.ProviderRoleUser, Content: "hello"}})
	t.Run("resolver", func(t *testing.T) {
		var calls atomic.Int64
		config := openAICompatibleConfigWithTransport("https://provider.test/v1", roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			calls.Add(1)
			return validAnswerHTTPResponse(), nil
		}))
		provider, err := agent.NewOpenAICompatibleProvider(config, &scriptedSecretResolver{err: errors.New(leaked)})
		if err != nil {
			t.Fatal(err)
		}
		_, err = provider.Complete(context.Background(), request)
		if !errors.Is(err, agent.ErrProviderSecretUnavailable) || strings.Contains(fmt.Sprint(err), leaked) || calls.Load() != 0 {
			t.Fatalf("Complete() error = %v, calls = %d", err, calls.Load())
		}
	})
	t.Run("transport", func(t *testing.T) {
		config := openAICompatibleConfigWithTransport("https://provider.test/v1", roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return nil, errors.New(leaked)
		}))
		provider, err := agent.NewOpenAICompatibleProvider(config, &scriptedSecretResolver{values: []string{"secret"}})
		if err != nil {
			t.Fatal(err)
		}
		_, err = provider.Complete(context.Background(), request)
		if !errors.Is(err, agent.ErrProviderTransport) || strings.Contains(fmt.Sprint(err), leaked) {
			t.Fatalf("Complete() error = %v", err)
		}
	})
}

func TestOpenAICompatibleProviderPreservesCancellationWithoutRetry(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		cancel()
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	provider, err := agent.NewOpenAICompatibleProvider(
		openAICompatibleConfigWithTransport("https://provider.test/v1", transport),
		&scriptedSecretResolver{values: []string{"secret"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Complete(ctx, providerRequest([]agent.ProviderMessage{{Role: agent.ProviderRoleUser, Content: "hello"}}))
	if !errors.Is(err, context.Canceled) || calls.Load() != 1 {
		t.Fatalf("Complete() error = %v, calls = %d", err, calls.Load())
	}
}

func TestOpenAICompatibleProviderDoesNotFollowRedirect(t *testing.T) {
	t.Parallel()

	var targetCalls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		targetCalls.Add(1)
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Redirect(writer, &http.Request{}, target.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	provider, err := agent.NewOpenAICompatibleProvider(
		openAICompatibleConfig(origin.URL+"/v1"), &scriptedSecretResolver{values: []string{"secret"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Complete(context.Background(), providerRequest([]agent.ProviderMessage{{Role: agent.ProviderRoleUser, Content: "hello"}}))
	if !errors.Is(err, agent.ErrProviderHTTPStatus) || targetCalls.Load() != 0 {
		t.Fatalf("Complete() error = %v, redirect target calls = %d", err, targetCalls.Load())
	}
}

func TestEnvironmentProviderSecretResolver(t *testing.T) {
	const name = "MEMORA_F180_TEST_API_KEY"
	t.Setenv(name, "secret-value")
	resolver := agent.EnvironmentProviderSecretResolver{}
	value, err := resolver.ResolveProviderSecret(context.Background(), name)
	if err != nil || value != "secret-value" {
		t.Fatalf("ResolveProviderSecret() returned an unexpected result: present=%t, error=%v", value != "", err)
	}
	t.Setenv(name, "")
	if _, err := resolver.ResolveProviderSecret(context.Background(), name); !errors.Is(err, agent.ErrProviderSecretUnavailable) {
		t.Fatalf("empty secret error = %v", err)
	}
}

func TestKimiOpenAICompatibleProviderSmoke(t *testing.T) {
	if os.Getenv("MEMORA_RUN_KIMI_SMOKE") != "1" {
		t.Skip("set MEMORA_RUN_KIMI_SMOKE=1 for the opt-in live API test")
	}
	model := os.Getenv("MEMORA_KIMI_MODEL")
	if model == "" {
		model = "kimi-k2.5"
	}
	provider, err := agent.NewOpenAICompatibleProvider(agent.OpenAICompatibleProviderConfig{
		APIBaseURL: "https://api.moonshot.ai/v1", SecretName: "MOONSHOT_API_KEY",
		Timeout: 90 * time.Second, MaxRequestBytes: 1 << 20, MaxResponseBytes: 2 << 20,
	}, agent.EnvironmentProviderSecretResolver{})
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := agent.NewProviderGateway(provider)
	if err != nil {
		t.Fatal(err)
	}
	request := agent.ProviderRequest{
		Version: agent.ProviderRequestVersion, Model: model,
		Messages: []agent.ProviderMessage{{
			Role:    agent.ProviderRoleUser,
			Content: `Call memora_probe exactly once with {"ok":true}. Do not answer directly.`,
		}},
		Tools: []agent.ProviderTool{{
			Name: "memora_probe", Description: "Return the requested smoke-test boolean",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}`),
		}},
		ToolChoice: agent.ProviderToolChoiceRequired, MaxOutputTokens: 128,
	}
	response, err := gateway.Complete(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if response.FinishReason != agent.ProviderFinishToolCalls || len(response.Message.ToolCalls) != 1 ||
		response.Message.ToolCalls[0].Name != "memora_probe" {
		t.Fatalf("unexpected model=%q finish=%q tool_count=%d", response.Model, response.FinishReason, len(response.Message.ToolCalls))
	}
	var arguments struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(response.Message.ToolCalls[0].Arguments, &arguments); err != nil || !arguments.OK {
		t.Fatalf("tool arguments did not satisfy the smoke contract: error=%v", err)
	}
	t.Logf("Kimi smoke passed: model=%s finish=%s input_tokens=%d cached_input_tokens=%d output_tokens=%d",
		response.Model, response.FinishReason, response.Usage.InputTokens,
		response.Usage.CachedInputTokens, response.Usage.OutputTokens)
}

type scriptedSecretResolver struct {
	mu     sync.Mutex
	values []string
	err    error
	calls  int
}

func (resolver *scriptedSecretResolver) ResolveProviderSecret(ctx context.Context, _ string) (string, error) {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	resolver.calls++
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if resolver.err != nil {
		return "", resolver.err
	}
	if len(resolver.values) == 0 {
		return "", nil
	}
	index := resolver.calls - 1
	if index >= len(resolver.values) {
		index = len(resolver.values) - 1
	}
	return resolver.values[index], nil
}

func (resolver *scriptedSecretResolver) callCount() int {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	return resolver.calls
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func openAICompatibleConfig(baseURL string) agent.OpenAICompatibleProviderConfig {
	return agent.OpenAICompatibleProviderConfig{
		APIBaseURL: baseURL, SecretName: "PROVIDER_API_KEY", Timeout: 2 * time.Second,
		MaxRequestBytes: 1 << 20, MaxResponseBytes: 1 << 20,
	}
}

func openAICompatibleConfigWithTransport(baseURL string, transport http.RoundTripper) agent.OpenAICompatibleProviderConfig {
	config := openAICompatibleConfig(baseURL)
	config.Transport = transport
	return config
}

func jsonHTTPResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func validAnswerHTTPResponse() *http.Response {
	return jsonHTTPResponse(http.StatusOK, validAnswerJSON())
}

func validAnswerJSON() string {
	return `{"model":"model-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`
}
