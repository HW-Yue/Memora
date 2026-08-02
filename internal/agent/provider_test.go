package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/HW-Yue/Memora/internal/agent"
)

func TestProviderGatewayReplaysValidatedToolCallTranscript(t *testing.T) {
	t.Parallel()

	firstRequest := providerRequest([]agent.ProviderMessage{{Role: agent.ProviderRoleUser, Content: "Find the note"}})
	firstResponse := providerResponse("tool_calls", agent.ProviderMessage{
		Role: agent.ProviderRoleAssistant,
		ToolCalls: []agent.ProviderToolCall{{
			ID: "call-1", Name: "execute_msql", Arguments: json.RawMessage(`{"source":"SELECT 1"}`),
		}},
	})
	secondRequest := providerRequest([]agent.ProviderMessage{
		{Role: agent.ProviderRoleUser, Content: "Find the note"},
		firstResponse.Message,
		{Role: agent.ProviderRoleTool, ToolCallID: "call-1", Content: `{"ok":true}`},
	})
	secondResponse := providerResponse("stop", agent.ProviderMessage{
		Role: agent.ProviderRoleAssistant, Content: "Found it.",
	})
	fake := &recordingProvider{responses: []agent.ProviderResponse{firstResponse, secondResponse}}
	gateway, err := agent.NewProviderGateway(fake)
	if err != nil {
		t.Fatal(err)
	}
	gotFirst, err := gateway.Complete(context.Background(), firstRequest)
	if err != nil || !reflect.DeepEqual(gotFirst, firstResponse) {
		t.Fatalf("first Complete() = %#v, %v", gotFirst, err)
	}
	gotSecond, err := gateway.Complete(context.Background(), secondRequest)
	if err != nil || !reflect.DeepEqual(gotSecond, secondResponse) {
		t.Fatalf("second Complete() = %#v, %v", gotSecond, err)
	}
	if !reflect.DeepEqual(fake.requests, []agent.ProviderRequest{firstRequest, secondRequest}) {
		t.Fatalf("requests = %#v", fake.requests)
	}
}

func TestProviderGatewayValidatesBothSidesAndDoesNotRetry(t *testing.T) {
	t.Parallel()

	t.Run("nil dependency", func(t *testing.T) {
		if _, err := agent.NewProviderGateway(nil); !errors.Is(err, agent.ErrInvalidProviderDependencies) {
			t.Fatalf("NewProviderGateway() error = %v", err)
		}
	})
	t.Run("invalid outbound", func(t *testing.T) {
		fake := &recordingProvider{}
		gateway, _ := agent.NewProviderGateway(fake)
		_, err := gateway.Complete(context.Background(), agent.ProviderRequest{})
		if !errors.Is(err, agent.ErrInvalidProviderRequest) || len(fake.requests) != 0 {
			t.Fatalf("Complete() error = %v, calls = %d", err, len(fake.requests))
		}
	})
	t.Run("invalid inbound", func(t *testing.T) {
		fake := &recordingProvider{responses: []agent.ProviderResponse{{}}}
		gateway, _ := agent.NewProviderGateway(fake)
		_, err := gateway.Complete(context.Background(), providerRequest([]agent.ProviderMessage{{Role: agent.ProviderRoleUser, Content: "hello"}}))
		if !errors.Is(err, agent.ErrInvalidProviderResponse) || len(fake.requests) != 1 {
			t.Fatalf("Complete() error = %v, calls = %d", err, len(fake.requests))
		}
	})
	t.Run("provider error", func(t *testing.T) {
		wantErr := errors.New("model unavailable")
		fake := &recordingProvider{errors: []error{wantErr}}
		gateway, _ := agent.NewProviderGateway(fake)
		_, err := gateway.Complete(context.Background(), providerRequest([]agent.ProviderMessage{{Role: agent.ProviderRoleUser, Content: "hello"}}))
		if !errors.Is(err, wantErr) || len(fake.requests) != 1 {
			t.Fatalf("Complete() error = %v, calls = %d", err, len(fake.requests))
		}
	})
}

func TestProviderContractsRejectMalformedToolAndUsageShapes(t *testing.T) {
	t.Parallel()

	request := providerRequest([]agent.ProviderMessage{{Role: agent.ProviderRoleUser, Content: "hello"}})
	request.Tools[0].InputSchema = json.RawMessage(`[]`)
	if !errors.Is(request.Validate(), agent.ErrInvalidProviderRequest) {
		t.Fatalf("Request.Validate() error = %v", request.Validate())
	}
	response := providerResponse("tool_calls", agent.ProviderMessage{
		Role:      agent.ProviderRoleAssistant,
		ToolCalls: []agent.ProviderToolCall{{ID: "call", Name: "execute_msql", Arguments: json.RawMessage(`[]`)}},
	})
	if !errors.Is(response.Validate(), agent.ErrInvalidProviderResponse) {
		t.Fatalf("Response.Validate() arguments error = %v", response.Validate())
	}
	response = providerResponse("stop", agent.ProviderMessage{Role: agent.ProviderRoleAssistant, Content: "done"})
	response.Usage.TotalTokens++
	if !errors.Is(response.Validate(), agent.ErrInvalidProviderResponse) {
		t.Fatalf("Response.Validate() usage error = %v", response.Validate())
	}
}

type recordingProvider struct {
	requests  []agent.ProviderRequest
	responses []agent.ProviderResponse
	errors    []error
}

func (provider *recordingProvider) Complete(
	_ context.Context,
	request agent.ProviderRequest,
) (agent.ProviderResponse, error) {
	provider.requests = append(provider.requests, request)
	index := len(provider.requests) - 1
	if index < len(provider.errors) && provider.errors[index] != nil {
		return agent.ProviderResponse{}, provider.errors[index]
	}
	if index < len(provider.responses) {
		return provider.responses[index], nil
	}
	return agent.ProviderResponse{}, nil
}

func providerRequest(messages []agent.ProviderMessage) agent.ProviderRequest {
	return agent.ProviderRequest{
		Version: agent.ProviderRequestVersion, Model: "model-test", Messages: messages,
		Tools: []agent.ProviderTool{{
			Name: "execute_msql", Description: "Execute one MSQL request",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
		}},
		ToolChoice: agent.ProviderToolChoiceAuto, MaxOutputTokens: 1024,
	}
}

func providerResponse(reason agent.ProviderFinishReason, message agent.ProviderMessage) agent.ProviderResponse {
	return agent.ProviderResponse{
		Version: agent.ProviderResponseVersion, Model: "model-test", Message: message,
		FinishReason: reason,
		Usage:        agent.ProviderUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
	}
}
