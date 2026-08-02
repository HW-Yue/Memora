package agenttest

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"

	"github.com/HW-Yue/Memora/internal/agent"
)

var (
	ErrUnexpectedProviderCall    = errors.New("unexpected Provider call")
	ErrUnexpectedProviderRequest = errors.New("unexpected Provider request")
	ErrPendingProviderCalls      = errors.New("scripted Provider calls remain")
)

type ProviderStep struct {
	Request  agent.ProviderRequest
	Response agent.ProviderResponse
	Error    error
}

type ScriptedProvider struct {
	mu    sync.Mutex
	steps []ProviderStep
	next  int
	calls []agent.ProviderRequest
}

func NewScriptedProvider(steps ...ProviderStep) *ScriptedProvider {
	return &ScriptedProvider{steps: append([]ProviderStep(nil), steps...)}
}

func (fake *ScriptedProvider) Complete(
	ctx context.Context,
	request agent.ProviderRequest,
) (agent.ProviderResponse, error) {
	if err := ctx.Err(); err != nil {
		return agent.ProviderResponse{}, err
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls = append(fake.calls, request)
	if fake.next == len(fake.steps) {
		return agent.ProviderResponse{}, ErrUnexpectedProviderCall
	}
	step := fake.steps[fake.next]
	fake.next++
	if !reflect.DeepEqual(request, step.Request) {
		return agent.ProviderResponse{}, fmt.Errorf(
			"%w at call %d: got %#v, want %#v",
			ErrUnexpectedProviderRequest, fake.next, request, step.Request,
		)
	}
	return step.Response, step.Error
}

func (fake *ScriptedProvider) Calls() []agent.ProviderRequest {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return append([]agent.ProviderRequest(nil), fake.calls...)
}

func (fake *ScriptedProvider) Verify() error {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.next != len(fake.steps) {
		return fmt.Errorf("%w: completed %d of %d", ErrPendingProviderCalls, fake.next, len(fake.steps))
	}
	return nil
}
