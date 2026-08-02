package agenttest_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/HW-Yue/Memora/internal/agent"
	"github.com/HW-Yue/Memora/internal/agent/agenttest"
)

func TestScriptedProviderChecksSequenceAndExhaustion(t *testing.T) {
	t.Parallel()

	first := scriptedProviderRequest("first")
	second := scriptedProviderRequest("second")
	fake := agenttest.NewScriptedProvider(
		agenttest.ProviderStep{Request: first, Response: scriptedProviderResponse("one")},
		agenttest.ProviderStep{Request: second, Error: errors.New("scripted provider failure")},
	)
	var port agent.Provider = fake
	if response, err := port.Complete(context.Background(), first); err != nil || response.Message.Content != "one" {
		t.Fatalf("first Complete() = %#v, %v", response, err)
	}
	if !errors.Is(fake.Verify(), agenttest.ErrPendingProviderCalls) {
		t.Fatalf("early Verify() = %v", fake.Verify())
	}
	if _, err := port.Complete(context.Background(), second); err == nil {
		t.Fatal("scripted error missing")
	}
	if err := fake.Verify(); err != nil {
		t.Fatal(err)
	}
	if calls := fake.Calls(); len(calls) != 2 || calls[0].Model != "first" || calls[1].Model != "second" {
		t.Fatalf("Calls() = %#v", calls)
	}
	if _, err := fake.Complete(context.Background(), second); !errors.Is(err, agenttest.ErrUnexpectedProviderCall) {
		t.Fatalf("extra call error = %v", err)
	}
}

func TestScriptedProviderReportsMismatchAndIsConcurrentSafe(t *testing.T) {
	t.Parallel()

	mismatch := agenttest.NewScriptedProvider(agenttest.ProviderStep{Request: scriptedProviderRequest("expected")})
	if _, err := mismatch.Complete(context.Background(), scriptedProviderRequest("other")); !errors.Is(err, agenttest.ErrUnexpectedProviderRequest) {
		t.Fatalf("mismatch error = %v", err)
	}

	const count = 32
	request := scriptedProviderRequest("same")
	steps := make([]agenttest.ProviderStep, count)
	for index := range steps {
		steps[index] = agenttest.ProviderStep{Request: request, Response: scriptedProviderResponse("ok")}
	}
	fake := agenttest.NewScriptedProvider(steps...)
	var wait sync.WaitGroup
	wait.Add(count)
	for range count {
		go func() {
			defer wait.Done()
			if _, err := fake.Complete(context.Background(), request); err != nil {
				t.Errorf("Complete() error = %v", err)
			}
		}()
	}
	wait.Wait()
	if err := fake.Verify(); err != nil {
		t.Fatal(err)
	}
}

func scriptedProviderRequest(model string) agent.ProviderRequest {
	return agent.ProviderRequest{Version: agent.ProviderRequestVersion, Model: model}
}

func scriptedProviderResponse(content string) agent.ProviderResponse {
	return agent.ProviderResponse{
		Version: agent.ProviderResponseVersion, Model: "fake",
		Message: agent.ProviderMessage{Role: agent.ProviderRoleAssistant, Content: content},
	}
}
