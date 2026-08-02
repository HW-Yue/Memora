// Package agenttest provides deterministic fakes for Agent package tests.
package agenttest

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"

	protocolmsql "github.com/HW-Yue/Memora/protocol/msql"
)

var (
	ErrUnexpectedCall    = errors.New("unexpected MSQL call")
	ErrUnexpectedRequest = errors.New("unexpected MSQL request")
	ErrPendingCalls      = errors.New("scripted MSQL calls remain")
)

// MSQLStep is one exact request and its deterministic result.
type MSQLStep struct {
	Request  protocolmsql.Request
	Envelope protocolmsql.Envelope
	Error    error
}

// ScriptedMSQL is a concurrency-safe, order-sensitive MSQLExecutor fake.
// It intentionally does not simulate Parser, Policy, storage, or retries.
type ScriptedMSQL struct {
	mu    sync.Mutex
	steps []MSQLStep
	next  int
	calls []protocolmsql.Request
}

func NewScriptedMSQL(steps ...MSQLStep) *ScriptedMSQL {
	return &ScriptedMSQL{steps: append([]MSQLStep(nil), steps...)}
}

func (fake *ScriptedMSQL) ExecuteMSQL(
	ctx context.Context,
	request protocolmsql.Request,
) (protocolmsql.Envelope, error) {
	if err := ctx.Err(); err != nil {
		return protocolmsql.Envelope{}, err
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls = append(fake.calls, request)
	if fake.next == len(fake.steps) {
		return protocolmsql.Envelope{}, ErrUnexpectedCall
	}
	step := fake.steps[fake.next]
	fake.next++
	if !reflect.DeepEqual(request, step.Request) {
		return protocolmsql.Envelope{}, fmt.Errorf(
			"%w at call %d: got %#v, want %#v",
			ErrUnexpectedRequest, fake.next, request, step.Request,
		)
	}
	return step.Envelope, step.Error
}

// Calls returns a stable copy of the observed call sequence.
func (fake *ScriptedMSQL) Calls() []protocolmsql.Request {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return append([]protocolmsql.Request(nil), fake.calls...)
}

// Verify reports whether every scripted step was consumed exactly once.
func (fake *ScriptedMSQL) Verify() error {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.next != len(fake.steps) {
		return fmt.Errorf("%w: completed %d of %d", ErrPendingCalls, fake.next, len(fake.steps))
	}
	return nil
}
