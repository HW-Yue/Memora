package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/agent"
	"github.com/HW-Yue/Memora/internal/agent/agenttest"
	protocolmsql "github.com/HW-Yue/Memora/protocol/msql"
)

func TestQuerySessionStreamsLiveTraceAndReturnsImmutableResult(t *testing.T) {
	t.Parallel()

	question := "Which decision is current?"
	authorization := queryAuthorization(protocolmsql.LevelStructural)
	readOnly := queryReadOnlyAuthorization(authorization)
	bootstrapBudget := queryBootstrapBudget()
	queryBudget := agent.DefaultQueryBudget()
	selectSource := "SELECT decision FROM `work`.`notes` WHERE row_id = :row LIMIT 1"
	toolCall := agent.ProviderToolCall{
		ID: "call-session-select", Name: agent.QueryExecuteMSQLToolName,
		Arguments: queryArguments(t, selectSource, map[string]any{"row": "row-1"}),
	}
	provider := newGatedSessionProvider(
		queryProviderResponse(agent.ProviderFinishToolCalls, agent.ProviderMessage{
			Role: agent.ProviderRoleAssistant, ToolCalls: []agent.ProviderToolCall{toolCall},
		}),
		queryProviderResponse(agent.ProviderFinishStop, agent.ProviderMessage{
			Role: agent.ProviderRoleAssistant, Content: "Use the thin loop.",
		}),
	)
	provider.releaseFirst = make(chan struct{})
	selectEnvelope := queryEnvelope("session-select", protocolmsql.StatementResult{
		Index: 0, Statement: "SELECT", Source: selectSource, Status: "succeeded",
		Columns: []protocolmsql.Column{{Name: "decision", Type: "TEXT"}},
		Rows:    []protocolmsql.Row{{"decision": "Use the thin loop"}}, Warnings: []protocolmsql.Notice{},
	})
	msql := agenttest.NewScriptedMSQL(
		queryBootstrapStep(question, readOnly, bootstrapBudget),
		agenttest.MSQLStep{
			Request: queryMSQLRequest(selectSource, readOnly, protocolmsql.Parameters{
				Named: map[string]any{"row": "row-1"},
			}),
			Envelope: selectEnvelope,
		},
	)
	session := newQuerySession(t, newQueryAgent(t, msql, provider), authorization, bootstrapBudget,
		queryBudget, agent.QuerySessionBudget{MaxTurns: 2, MaxProviderCalls: 8, MaxToolCalls: 6})
	turn, err := session.Start(context.Background(), question)
	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-provider.firstEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("Provider was not reached")
	}
	preRelease := collectUntilTraceKind(t, turn.Events(), agent.TraceKindBootstrap)
	if len(preRelease) < 3 || preRelease[0].Kind != agent.QuerySessionEventTurnStarted {
		t.Fatalf("events before Provider release = %#v", preRelease)
	}
	close(provider.releaseFirst)
	events := append(preRelease, collectSessionEvents(turn.Events())...)
	result, err := turn.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if result.Answer != "Use the thin loop." || len(result.Evidence) != 1 {
		t.Fatalf("result = %#v", result)
	}
	assertSessionEvents(t, events, agent.QuerySessionEventTurnCompleted)
	encoded, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{question, selectSource, "Use the thin loop"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("session events leaked %q: %s", secret, encoded)
		}
	}

	result.Answer = "tampered"
	result.Evidence[0].Result.Rows[0]["decision"] = "tampered"
	again, err := turn.Wait()
	if err != nil || again.Answer != "Use the thin loop." ||
		again.Evidence[0].Result.Rows[0]["decision"] != "Use the thin loop" {
		t.Fatalf("second Wait() = %#v, %v", again, err)
	}
	if usage := session.Usage(); usage.TurnsStarted != 1 || usage.TurnsCompleted != 1 ||
		usage.ProviderCalls != 2 || usage.ToolCalls != 1 {
		t.Fatalf("usage = %#v", usage)
	}
	if err := msql.Verify(); err != nil {
		t.Fatal(err)
	}
}

func TestQuerySessionCancelsBusyTurnThenRecoversWithinActualBudget(t *testing.T) {
	t.Parallel()

	firstQuestion := "Cancel this lookup"
	secondQuestion := "Which fact survived?"
	authorization := queryAuthorization(protocolmsql.LevelStructural)
	readOnly := queryReadOnlyAuthorization(authorization)
	bootstrapBudget := queryBootstrapBudget()
	queryBudget := agent.DefaultQueryBudget()
	selectSource := "SELECT fact FROM `work`.`notes` WHERE row_id = :row LIMIT 1"
	toolCall := agent.ProviderToolCall{
		ID: "call-recovered-select", Name: agent.QueryExecuteMSQLToolName,
		Arguments: queryArguments(t, selectSource, map[string]any{"row": "row-2"}),
	}
	provider := newGatedSessionProvider(
		agent.ProviderResponse{},
		queryProviderResponse(agent.ProviderFinishToolCalls, agent.ProviderMessage{
			Role: agent.ProviderRoleAssistant, ToolCalls: []agent.ProviderToolCall{toolCall},
		}),
		queryProviderResponse(agent.ProviderFinishStop, agent.ProviderMessage{
			Role: agent.ProviderRoleAssistant, Content: "The recovered fact is intact.",
		}),
	)
	selectEnvelope := queryEnvelope("recovered-select", protocolmsql.StatementResult{
		Index: 0, Statement: "SELECT", Source: selectSource, Status: "succeeded",
		Columns: []protocolmsql.Column{{Name: "fact", Type: "TEXT"}},
		Rows:    []protocolmsql.Row{{"fact": "intact"}}, Warnings: []protocolmsql.Notice{},
	})
	msql := agenttest.NewScriptedMSQL(
		queryBootstrapStep(firstQuestion, readOnly, bootstrapBudget),
		queryBootstrapStep(secondQuestion, readOnly, bootstrapBudget),
		agenttest.MSQLStep{
			Request: queryMSQLRequest(selectSource, readOnly, protocolmsql.Parameters{
				Named: map[string]any{"row": "row-2"},
			}),
			Envelope: selectEnvelope,
		},
	)
	session := newQuerySession(t, newQueryAgent(t, msql, provider), authorization, bootstrapBudget,
		queryBudget, agent.QuerySessionBudget{MaxTurns: 2, MaxProviderCalls: 3, MaxToolCalls: 1})
	first, err := session.Start(context.Background(), firstQuestion)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-provider.firstEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("Provider was not reached")
	}
	if _, err := session.Start(context.Background(), "must be rejected while busy"); !errors.Is(err, agent.ErrQuerySessionBusy) {
		t.Fatalf("concurrent Start() error = %v", err)
	}
	first.Cancel()
	if _, err := first.Wait(); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Wait() error = %v", err)
	}
	assertSessionEvents(t, collectSessionEvents(first.Events()), agent.QuerySessionEventTurnCancelled)

	second, err := session.Start(context.Background(), secondQuestion)
	if err != nil {
		t.Fatal(err)
	}
	secondEvents := collectSessionEvents(second.Events())
	secondResult, err := second.Wait()
	if err != nil || secondResult.Answer != "The recovered fact is intact." {
		t.Fatalf("recovered Wait() = %#v, %v", secondResult, err)
	}
	assertSessionEvents(t, secondEvents, agent.QuerySessionEventTurnCompleted)
	if usage := session.Usage(); usage.TurnsStarted != 2 || usage.TurnsCompleted != 1 ||
		usage.TurnsCancelled != 1 || usage.ProviderCalls != 3 || usage.ToolCalls != 1 {
		t.Fatalf("usage = %#v", usage)
	}
	if _, err := session.Start(context.Background(), "budget exhausted"); !errors.Is(err, agent.ErrQuerySessionBudget) {
		t.Fatalf("exhausted Start() error = %v", err)
	}
	if provider.Calls() != 3 || len(msql.Calls()) != 3 {
		t.Fatalf("calls after rejected Start = Provider %d, MSQL %d", provider.Calls(), len(msql.Calls()))
	}
	if err := msql.Verify(); err != nil {
		t.Fatal(err)
	}
}

func TestQuerySessionRejectsInvalidConstructionAndRequests(t *testing.T) {
	t.Parallel()

	authorization := queryAuthorization(protocolmsql.LevelRead)
	bootstrapBudget := queryBootstrapBudget()
	queryBudget := agent.DefaultQueryBudget()
	queryAgent := newQueryAgent(t, agenttest.NewScriptedMSQL(), agenttest.NewScriptedProvider())
	config := querySessionConfig(authorization, bootstrapBudget, queryBudget,
		agent.QuerySessionBudget{MaxTurns: 1, MaxProviderCalls: 4, MaxToolCalls: 3})
	invalid := []agent.QuerySessionConfig{
		{},
		func() agent.QuerySessionConfig { value := config; value.SessionID = " "; return value }(),
		func() agent.QuerySessionConfig { value := config; value.Budget.MaxTurns = 0; return value }(),
		func() agent.QuerySessionConfig { value := config; value.Budget.MaxProviderCalls = 1; return value }(),
	}
	for _, candidate := range invalid {
		if _, err := agent.NewQuerySession(queryAgent, candidate); !errors.Is(err, agent.ErrInvalidQuerySession) {
			t.Fatalf("NewQuerySession(%#v) error = %v", candidate, err)
		}
	}
	if _, err := agent.NewQuerySession(nil, config); !errors.Is(err, agent.ErrInvalidQuerySession) {
		t.Fatalf("nil Agent error = %v", err)
	}
	session, err := agent.NewQuerySession(queryAgent, config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Start(nil, "question"); !errors.Is(err, agent.ErrInvalidQuerySessionRequest) {
		t.Fatalf("nil context error = %v", err)
	}
	if _, err := session.Start(context.Background(), " "); !errors.Is(err, agent.ErrInvalidQuerySessionRequest) {
		t.Fatalf("blank question error = %v", err)
	}
}

func newQuerySession(
	t *testing.T,
	queryAgent *agent.QueryAgent,
	authorization protocolmsql.Authorization,
	bootstrapBudget agent.BootstrapBudget,
	queryBudget agent.QueryBudget,
	budget agent.QuerySessionBudget,
) *agent.QuerySession {
	t.Helper()
	session, err := agent.NewQuerySession(queryAgent, querySessionConfig(
		authorization, bootstrapBudget, queryBudget, budget,
	))
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func querySessionConfig(
	authorization protocolmsql.Authorization,
	bootstrapBudget agent.BootstrapBudget,
	queryBudget agent.QueryBudget,
	budget agent.QuerySessionBudget,
) agent.QuerySessionConfig {
	return agent.QuerySessionConfig{
		SessionID: "session-interactive", Model: "model-test", Authorization: authorization,
		BootstrapBudget: bootstrapBudget, BootstrapProfile: agent.DefaultBootstrapProfile(),
		QueryBudget: queryBudget, Budget: budget,
	}
}

func collectUntilTraceKind(
	t *testing.T,
	events <-chan agent.QuerySessionEvent,
	kind agent.TraceKind,
) []agent.QuerySessionEvent {
	t.Helper()
	collected := make([]agent.QuerySessionEvent, 0)
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatal("event stream closed before expected live Trace")
			}
			collected = append(collected, event)
			if event.Trace != nil && event.Trace.Kind == kind {
				return collected
			}
		case <-deadline.C:
			t.Fatal("timed out waiting for live Trace")
		}
	}
}

func collectSessionEvents(events <-chan agent.QuerySessionEvent) []agent.QuerySessionEvent {
	collected := make([]agent.QuerySessionEvent, 0)
	for event := range events {
		collected = append(collected, event)
	}
	return collected
}

func assertSessionEvents(
	t *testing.T,
	events []agent.QuerySessionEvent,
	terminal agent.QuerySessionEventKind,
) {
	t.Helper()
	if len(events) < 2 || events[0].Kind != agent.QuerySessionEventTurnStarted ||
		events[len(events)-1].Kind != terminal {
		t.Fatalf("events = %#v", events)
	}
	terminalCount := 0
	for index, event := range events {
		if err := event.Validate(); err != nil {
			t.Fatalf("event[%d] Validate() = %v: %#v", index, err, event)
		}
		if event.Sequence != uint64(index)+1 {
			t.Fatalf("event[%d] sequence = %d", index, event.Sequence)
		}
		if index > 0 && (event.SessionID != events[0].SessionID || event.RunID != events[0].RunID ||
			event.Turn != events[0].Turn) {
			t.Fatalf("event[%d] identity changed: %#v", index, event)
		}
		if event.Kind == agent.QuerySessionEventTurnCompleted || event.Kind == agent.QuerySessionEventTurnFailed ||
			event.Kind == agent.QuerySessionEventTurnCancelled {
			terminalCount++
		}
	}
	if terminalCount != 1 {
		t.Fatalf("terminal events = %d in %#v", terminalCount, events)
	}
}

type gatedSessionProvider struct {
	mu           sync.Mutex
	responses    []agent.ProviderResponse
	calls        int
	firstEntered chan struct{}
	releaseFirst chan struct{}
}

func newGatedSessionProvider(responses ...agent.ProviderResponse) *gatedSessionProvider {
	return &gatedSessionProvider{responses: responses, firstEntered: make(chan struct{})}
}

func (provider *gatedSessionProvider) Complete(
	ctx context.Context,
	_ agent.ProviderRequest,
) (agent.ProviderResponse, error) {
	provider.mu.Lock()
	index := provider.calls
	provider.calls++
	if index >= len(provider.responses) {
		provider.mu.Unlock()
		return agent.ProviderResponse{}, errors.New("unexpected Provider call")
	}
	response := provider.responses[index]
	provider.mu.Unlock()
	if index == 0 {
		close(provider.firstEntered)
		select {
		case <-provider.releaseFirst:
			return response, nil
		case <-ctx.Done():
			return agent.ProviderResponse{}, ctx.Err()
		}
	}
	return response, nil
}

func (provider *gatedSessionProvider) Calls() int {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.calls
}
