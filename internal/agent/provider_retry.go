package agent

import (
	"context"
	"errors"
	"net/http"
	"time"
)

var ErrInvalidRetryingProvider = errors.New("retrying Provider configuration is invalid")

type RetryPolicy struct {
	MaxAttempts  int
	InitialDelay time.Duration
	MaxDelay     time.Duration
	Sleep        func(context.Context, time.Duration) error
}

type RetryingProvider struct {
	provider Provider
	policy   RetryPolicy
}

func NewRetryingProvider(provider Provider, policy RetryPolicy) (*RetryingProvider, error) {
	if provider == nil || policy.MaxAttempts < 1 || policy.MaxAttempts > 8 || policy.InitialDelay < 0 ||
		policy.MaxDelay < policy.InitialDelay || policy.MaxDelay > 10*time.Minute {
		return nil, ErrInvalidRetryingProvider
	}
	if policy.Sleep == nil {
		policy.Sleep = sleepWithContext
	}
	return &RetryingProvider{provider: provider, policy: policy}, nil
}

func (provider *RetryingProvider) Complete(ctx context.Context, request ProviderRequest) (ProviderResponse, error) {
	if provider == nil || provider.provider == nil || ctx == nil {
		return ProviderResponse{}, ErrInvalidRetryingProvider
	}
	var lastErr error
	for attempt := 1; attempt <= provider.policy.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return ProviderResponse{}, err
		}
		response, err := provider.provider.Complete(ctx, request)
		if err == nil {
			return response, nil
		}
		lastErr = err
		if attempt == provider.policy.MaxAttempts || !retryableProviderError(err) {
			break
		}
		delay := retryDelay(provider.policy, attempt)
		if err := provider.policy.Sleep(ctx, delay); err != nil {
			return ProviderResponse{}, err
		}
	}
	return ProviderResponse{}, lastErr
}

func retryableProviderError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, ErrInvalidProviderRequest) || errors.Is(err, ErrInvalidProviderResponse) ||
		errors.Is(err, ErrProviderWireResponse) || errors.Is(err, ErrProviderRequestTooLarge) ||
		errors.Is(err, ErrProviderResponseTooLarge) {
		return false
	}
	var statusError *ProviderHTTPStatusError
	if errors.As(err, &statusError) {
		switch statusError.StatusCode {
		case http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusInternalServerError,
			http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return true
		default:
			return false
		}
	}
	return errors.Is(err, ErrProviderTransport)
}

func retryDelay(policy RetryPolicy, attempt int) time.Duration {
	delay := policy.InitialDelay
	for index := 1; index < attempt && delay < policy.MaxDelay; index++ {
		if delay > policy.MaxDelay/2 {
			return policy.MaxDelay
		}
		delay *= 2
	}
	if delay > policy.MaxDelay {
		return policy.MaxDelay
	}
	return delay
}

func sleepWithContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
