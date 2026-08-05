package agent_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/agent"
)

func TestRetryingProviderRetriesRateLimitAndServerFailureSerially(t *testing.T) {
	fake := &retryProvider{errors: []error{
		&agent.ProviderHTTPStatusError{StatusCode: http.StatusTooManyRequests},
		&agent.ProviderHTTPStatusError{StatusCode: http.StatusServiceUnavailable},
		nil,
	}}
	delays := make([]time.Duration, 0)
	provider, err := agent.NewRetryingProvider(fake, agent.RetryPolicy{MaxAttempts: 4, InitialDelay: time.Second, MaxDelay: 8 * time.Second,
		Sleep: func(_ context.Context, delay time.Duration) error { delays = append(delays, delay); return nil }})
	if err != nil {
		t.Fatal(err)
	}
	response, err := provider.Complete(context.Background(), agent.ProviderRequest{})
	if err != nil || response.Version != agent.ProviderResponseVersion || fake.calls != 3 {
		t.Fatalf("response/error/calls = %#v/%v/%d", response, err, fake.calls)
	}
	if len(delays) != 2 || delays[0] != time.Second || delays[1] != 2*time.Second {
		t.Fatalf("delays = %v", delays)
	}
}

func TestRetryingProviderDoesNotRetryInvalidOrCancelledRequests(t *testing.T) {
	fake := &retryProvider{errors: []error{agent.ErrProviderWireResponse}}
	provider, err := agent.NewRetryingProvider(fake, agent.RetryPolicy{MaxAttempts: 4, InitialDelay: 0, MaxDelay: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Complete(context.Background(), agent.ProviderRequest{}); !errors.Is(err, agent.ErrProviderWireResponse) || fake.calls != 1 {
		t.Fatalf("wire error/calls = %v/%d", err, fake.calls)
	}
	fake.errors = []error{&agent.ProviderHTTPStatusError{StatusCode: http.StatusTooManyRequests}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := provider.Complete(ctx, agent.ProviderRequest{}); !errors.Is(err, context.Canceled) || fake.calls != 1 {
		t.Fatalf("cancel error/calls = %v/%d", err, fake.calls)
	}
}

type retryProvider struct {
	errors []error
	calls  int
}

func (provider *retryProvider) Complete(context.Context, agent.ProviderRequest) (agent.ProviderResponse, error) {
	provider.calls++
	index := provider.calls - 1
	if index < len(provider.errors) && provider.errors[index] != nil {
		return agent.ProviderResponse{}, provider.errors[index]
	}
	return agent.ProviderResponse{Version: agent.ProviderResponseVersion, Model: "fixture", FinishReason: agent.ProviderFinishStop,
		Message: agent.ProviderMessage{Role: agent.ProviderRoleAssistant, Content: "ok"}, Usage: agent.ProviderUsage{TotalTokens: 1}}, nil
}
