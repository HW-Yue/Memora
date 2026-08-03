package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/agent"
	"github.com/HW-Yue/Memora/internal/agent/agenttest"
	protocolmsql "github.com/HW-Yue/Memora/protocol/msql"
)

func TestQueryAgentRunsBootstrapSelectFinalWithEvidenceAndTrace(t *testing.T) {
	t.Parallel()

	question := "Which note contains the private launch decision?"
	authorization := queryAuthorization(protocolmsql.LevelStructural)
	readOnly := queryReadOnlyAuthorization(authorization)
	bootstrapBudget := agent.DefaultBootstrapBudget()
	bootstrapBudget.RootTableLimit = 0
	queryBudget := agent.DefaultQueryBudget()
	atlasRows := []protocolmsql.Row{{
		"kind": "table", "database_id": "db-work", "database": "work",
		"table_id": "table-notes", "table": "notes", "purpose": "private notes",
	}}
	lexicalRows := []protocolmsql.Row{{
		"kind": "row", "database_id": "db-work", "table_id": "table-notes", "row_id": "row-1",
	}}
	bootstrapResult := bootstrapEnvelope(
		atlasResult(0, atlasRows, page("atlas-1", false, "")),
		lexicalResult(1, lexicalRows, page("lexical-1", false, "")),
	)
	frame := expectedBootstrapFrame(t, atlasRows, lexicalRows)

	selectSource := "SELECT title, decision FROM `work`.`notes` WHERE row_id = :row LIMIT 1"
	toolArguments := json.RawMessage(`{"source":"SELECT title, decision FROM ` +
		"`work`.`notes`" + ` WHERE row_id = :row LIMIT 1","statements":[{"named":{"row":"row-1"}}]}`)
	selectRequest := protocolmsql.Request{
		Version: protocolmsql.RequestVersion, Source: selectSource,
		Statements: []protocolmsql.StatementInput{{
			Parameters:    protocolmsql.Parameters{Named: map[string]any{"row": "row-1"}},
			Authorization: readOnly,
		}},
	}
	selectResult := protocolmsql.StatementResult{
		Index: 0, Statement: "SELECT", Source: selectSource, Status: "succeeded",
		Columns:  []protocolmsql.Column{{Name: "title", Type: "TEXT"}, {Name: "decision", Type: "TEXT"}},
		Rows:     []protocolmsql.Row{{"title": "Launch", "decision": "Use the thin loop"}},
		Warnings: []protocolmsql.Notice{},
	}
	selectEnvelope := protocolmsql.Envelope{
		Version: protocolmsql.EnvelopeVersion, RequestID: "select-1", OK: true,
		Results: []protocolmsql.StatementResult{selectResult}, Warnings: []protocolmsql.Notice{},
	}
	toolCall := agent.ProviderToolCall{ID: "call-select", Name: agent.QueryExecuteMSQLToolName, Arguments: toolArguments}
	firstResponse := queryProviderResponse(agent.ProviderFinishToolCalls, agent.ProviderMessage{
		Role: agent.ProviderRoleAssistant, ToolCalls: []agent.ProviderToolCall{toolCall},
	})
	answer := "The launch decision was to use the thin loop."
	finalResponse := queryProviderResponse(agent.ProviderFinishStop, agent.ProviderMessage{
		Role: agent.ProviderRoleAssistant, Content: answer,
	})
	toolResultJSON := mustJSON(t, selectEnvelope)
	provider := agenttest.NewScriptedProvider(
		agenttest.ProviderStep{
			Request:  queryProviderRequest(t, question, frame, nil, "", agent.ProviderToolChoiceRequired, queryBudget),
			Response: firstResponse,
		},
		agenttest.ProviderStep{
			Request:  queryProviderRequest(t, question, agent.BootstrapFrame{}, &toolCall, string(toolResultJSON), agent.ProviderToolChoiceNone, queryBudget),
			Response: finalResponse,
		},
	)
	msql := agenttest.NewScriptedMSQL(
		agenttest.MSQLStep{
			Request: initialBootstrapRequest(question, readOnly, bootstrapBudget), Envelope: bootstrapResult,
		},
		agenttest.MSQLStep{Request: selectRequest, Envelope: selectEnvelope},
	)
	queryAgent := newQueryAgent(t, msql, provider)
	result, err := queryAgent.Query(context.Background(), agent.QueryRequest{
		Identity: agent.TraceIdentity{RunID: "run-query-1", SessionID: "session-query-1"},
		Question: question, Model: "model-test", Authorization: authorization,
		BootstrapBudget: bootstrapBudget, Budget: queryBudget,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Version != agent.QueryResultVersion || result.Answer != answer || len(result.Evidence) != 1 {
		t.Fatalf("Query() result = %#v", result)
	}
	wantEvidence := agent.SelectEvidence{RequestID: "select-1", Result: selectResult}
	if !reflect.DeepEqual(result.Evidence[0], wantEvidence) {
		t.Fatalf("evidence = %#v, want %#v", result.Evidence[0], wantEvidence)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("result Validate() = %v", err)
	}
	if err := result.Trace.Validate(); err != nil {
		t.Fatalf("Trace Validate() = %v", err)
	}
	if result.Trace.Summary.Events != 6 || result.Trace.Summary.KindCounts[agent.TraceKindBootstrap] != 1 ||
		result.Trace.Summary.KindCounts[agent.TraceKindProvider] != 2 ||
		result.Trace.Summary.KindCounts[agent.TraceKindMSQL] != 2 ||
		result.Trace.Summary.KindCounts[agent.TraceKindTool] != 1 {
		t.Fatalf("Trace summary = %#v", result.Trace.Summary)
	}
	traceJSON := string(mustJSON(t, result.Trace))
	for _, secret := range []string{question, selectSource, "Use the thin loop", answer} {
		if strings.Contains(traceJSON, secret) {
			t.Fatalf("Trace leaked %q: %s", secret, traceJSON)
		}
	}
	if err := msql.Verify(); err != nil {
		t.Fatal(err)
	}
	if err := provider.Verify(); err != nil {
		t.Fatal(err)
	}
}

func TestQueryAgentCompactsBootstrapAndKeepsOnlyCurrentToolFrame(t *testing.T) {
	t.Parallel()

	question := "Find the current architecture decision"
	authorization := queryAuthorization(protocolmsql.LevelRead)
	bootstrapBudget := agent.DefaultBootstrapBudget()
	bootstrapBudget.RootTableLimit = 0
	atlasRows := []protocolmsql.Row{{
		"kind": "table", "database_id": "db", "database": "work", "table_id": "table", "table": "notes",
		"purpose": "ATLAS_MARKER",
	}}
	bootstrapResult := bootstrapEnvelope(
		atlasResult(0, atlasRows, page("atlas", false, "")),
		lexicalResult(1, []protocolmsql.Row{}, page("lexical", false, "")),
	)
	navigateArguments := json.RawMessage(`{"source":"SHOW ROUTES FROM TABLE work.notes AT ROOT LIMIT 4","statements":[{}]}`)
	navigateCall := agent.ProviderToolCall{ID: "call-route", Name: agent.QueryExecuteMSQLToolName, Arguments: navigateArguments}
	navigateEnvelope := queryEnvelope("route-1", protocolmsql.StatementResult{
		Index: 0, Statement: "SHOW_ROUTES", Source: "SHOW ROUTES FROM TABLE work.notes AT ROOT LIMIT 4",
		Status: "succeeded", Columns: []protocolmsql.Column{},
		Rows:     []protocolmsql.Row{{"route_id": "route-1", "purpose": "ROUTE_MARKER"}},
		Warnings: []protocolmsql.Notice{},
	})
	selectArguments := json.RawMessage(`{"source":"SELECT decision FROM work.notes WHERE row_id = :row LIMIT 1","statements":[{"named":{"row":"row-1"}}]}`)
	selectCall := agent.ProviderToolCall{ID: "call-row", Name: agent.QueryExecuteMSQLToolName, Arguments: selectArguments}
	selectEnvelope := queryEnvelope("select-2", protocolmsql.StatementResult{
		Index: 0, Statement: "SELECT", Source: "SELECT decision FROM work.notes WHERE row_id = :row LIMIT 1",
		Status: "succeeded", Columns: []protocolmsql.Column{},
		Rows: []protocolmsql.Row{{"decision": "FACT_MARKER"}}, Warnings: []protocolmsql.Notice{},
	})
	provider := &recordingQueryProvider{responses: []agent.ProviderResponse{
		queryProviderResponse(agent.ProviderFinishToolCalls, agent.ProviderMessage{
			Role: agent.ProviderRoleAssistant, ToolCalls: []agent.ProviderToolCall{navigateCall},
		}),
		queryProviderResponse(agent.ProviderFinishToolCalls, agent.ProviderMessage{
			Role: agent.ProviderRoleAssistant, ToolCalls: []agent.ProviderToolCall{selectCall},
		}),
		queryProviderResponse(agent.ProviderFinishStop, agent.ProviderMessage{
			Role: agent.ProviderRoleAssistant, Content: "The current decision is FACT_MARKER.",
		}),
	}}
	readOnly := queryReadOnlyAuthorization(authorization)
	msql := agenttest.NewScriptedMSQL(
		agenttest.MSQLStep{Request: initialBootstrapRequest(question, readOnly, bootstrapBudget), Envelope: bootstrapResult},
		agenttest.MSQLStep{
			Request:  queryMSQLRequest("SHOW ROUTES FROM TABLE work.notes AT ROOT LIMIT 4", readOnly, protocolmsql.Parameters{}),
			Envelope: navigateEnvelope,
		},
		agenttest.MSQLStep{
			Request: queryMSQLRequest(
				"SELECT decision FROM work.notes WHERE row_id = :row LIMIT 1", readOnly,
				protocolmsql.Parameters{Named: map[string]any{"row": "row-1"}},
			),
			Envelope: selectEnvelope,
		},
	)
	queryAgent := newQueryAgent(t, msql, provider)
	result, err := queryAgent.Query(context.Background(), agent.QueryRequest{
		Identity: agent.TraceIdentity{RunID: "run-compact", SessionID: "session-compact"},
		Question: question, Model: "model-test", Authorization: authorization,
		BootstrapBudget: bootstrapBudget, Budget: agent.DefaultQueryBudget(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 3 || result.Answer == "" {
		t.Fatalf("provider calls=%d result=%#v", len(provider.requests), result)
	}
	first := string(mustJSON(t, provider.requests[0]))
	second := string(mustJSON(t, provider.requests[1]))
	third := string(mustJSON(t, provider.requests[2]))
	if !strings.Contains(first, "ATLAS_MARKER") || strings.Contains(second, "ATLAS_MARKER") ||
		!strings.Contains(second, "ROUTE_MARKER") || strings.Contains(third, "ATLAS_MARKER") ||
		strings.Contains(third, "ROUTE_MARKER") || !strings.Contains(third, "FACT_MARKER") {
		t.Fatalf("context was not compacted\nfirst=%s\nsecond=%s\nthird=%s", first, second, third)
	}
	if provider.requests[0].ToolChoice != agent.ProviderToolChoiceRequired ||
		provider.requests[1].ToolChoice != agent.ProviderToolChoiceRequired ||
		provider.requests[2].ToolChoice != agent.ProviderToolChoiceNone {
		t.Fatalf("tool choices = %q, %q, %q", provider.requests[0].ToolChoice, provider.requests[1].ToolChoice, provider.requests[2].ToolChoice)
	}
	if err := msql.Verify(); err != nil {
		t.Fatal(err)
	}
}

func TestQueryAgentRejectsTransactionControlBeforeModelMSQLExecution(t *testing.T) {
	t.Parallel()

	question := "Find a note"
	authorization := queryAuthorization(protocolmsql.LevelIrreversible)
	readOnly := queryReadOnlyAuthorization(authorization)
	bootstrapBudget := agent.DefaultBootstrapBudget()
	bootstrapBudget.RootTableLimit = 0
	msql := agenttest.NewScriptedMSQL(agenttest.MSQLStep{
		Request: initialBootstrapRequest(question, readOnly, bootstrapBudget),
		Envelope: bootstrapEnvelope(
			atlasResult(0, []protocolmsql.Row{}, page("atlas", false, "")),
			lexicalResult(1, []protocolmsql.Row{}, page("lexical", false, "")),
		),
	})
	provider := &recordingQueryProvider{responses: []agent.ProviderResponse{
		queryProviderResponse(agent.ProviderFinishToolCalls, agent.ProviderMessage{
			Role: agent.ProviderRoleAssistant,
			ToolCalls: []agent.ProviderToolCall{{
				ID: "call-transaction", Name: agent.QueryExecuteMSQLToolName,
				Arguments: json.RawMessage(`{"source":"BEGIN; SELECT * FROM work.notes LIMIT 1","statements":[{},{}]}`),
			}},
		}),
	}}
	queryAgent := newQueryAgent(t, msql, provider)
	result, err := queryAgent.Query(context.Background(), agent.QueryRequest{
		Identity: agent.TraceIdentity{RunID: "run-transaction", SessionID: "session-transaction"},
		Question: question, Model: "model-test", Authorization: authorization,
		BootstrapBudget: bootstrapBudget, Budget: agent.DefaultQueryBudget(),
	})
	if !errors.Is(err, agent.ErrQueryTransactionControl) {
		t.Fatalf("Query() error = %v, want ErrQueryTransactionControl", err)
	}
	if len(msql.Calls()) != 1 || len(provider.requests) != 1 {
		t.Fatalf("MSQL calls=%d Provider calls=%d", len(msql.Calls()), len(provider.requests))
	}
	if result.Trace.Summary.KindCounts[agent.TraceKindTool] != 1 ||
		result.Trace.Summary.StatusCounts[agent.TraceStatusFailed] != 1 {
		t.Fatalf("failed Trace = %#v", result.Trace)
	}
	if validationErr := result.Trace.Validate(); validationErr != nil {
		t.Fatalf("Trace Validate() = %v", validationErr)
	}
}

func TestQueryAgentRejectsAnswerWithoutSelectAndMultipleToolCalls(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		response agent.ProviderResponse
		want     error
	}{
		{
			name: "answer before SELECT",
			response: queryProviderResponse(agent.ProviderFinishStop, agent.ProviderMessage{
				Role: agent.ProviderRoleAssistant, Content: "I guessed the answer.",
			}),
			want: agent.ErrQueryMissingSelectEvidence,
		},
		{
			name: "multiple tool calls",
			response: queryProviderResponse(agent.ProviderFinishToolCalls, agent.ProviderMessage{
				Role: agent.ProviderRoleAssistant,
				ToolCalls: []agent.ProviderToolCall{
					{ID: "call-1", Name: agent.QueryExecuteMSQLToolName, Arguments: json.RawMessage(`{"source":"SELECT 1","statements":[{}]}`)},
					{ID: "call-2", Name: agent.QueryExecuteMSQLToolName, Arguments: json.RawMessage(`{"source":"SELECT 2","statements":[{}]}`)},
				},
			}),
			want: agent.ErrInvalidQueryToolCall,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			question := "question"
			authorization := queryAuthorization(protocolmsql.LevelRead)
			readOnly := queryReadOnlyAuthorization(authorization)
			bootstrapBudget := agent.DefaultBootstrapBudget()
			bootstrapBudget.RootTableLimit = 0
			msql := agenttest.NewScriptedMSQL(agenttest.MSQLStep{
				Request: initialBootstrapRequest(question, readOnly, bootstrapBudget),
				Envelope: bootstrapEnvelope(
					atlasResult(0, []protocolmsql.Row{}, page("atlas", false, "")),
					lexicalResult(1, []protocolmsql.Row{}, page("lexical", false, "")),
				),
			})
			provider := &recordingQueryProvider{responses: []agent.ProviderResponse{test.response}}
			queryAgent := newQueryAgent(t, msql, provider)
			result, err := queryAgent.Query(context.Background(), agent.QueryRequest{
				Identity: agent.TraceIdentity{RunID: "run-error", SessionID: "session-error"},
				Question: question, Model: "model-test", Authorization: authorization,
				BootstrapBudget: bootstrapBudget, Budget: agent.DefaultQueryBudget(),
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("Query() error = %v, want %v", err, test.want)
			}
			if validationErr := result.Trace.Validate(); validationErr != nil {
				t.Fatalf("Trace Validate() = %v", validationErr)
			}
			if len(msql.Calls()) != 1 || len(provider.requests) != 1 {
				t.Fatalf("MSQL calls=%d Provider calls=%d", len(msql.Calls()), len(provider.requests))
			}
		})
	}
}

type queryClock struct {
	mu   sync.Mutex
	next time.Time
}

func (clock *queryClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	value := clock.next
	clock.next = clock.next.Add(time.Millisecond)
	return value
}

type recordingQueryProvider struct {
	requests  []agent.ProviderRequest
	responses []agent.ProviderResponse
	errors    []error
}

func (provider *recordingQueryProvider) Complete(
	_ context.Context,
	request agent.ProviderRequest,
) (agent.ProviderResponse, error) {
	provider.requests = append(provider.requests, request)
	index := len(provider.requests) - 1
	if index < len(provider.errors) && provider.errors[index] != nil {
		return agent.ProviderResponse{}, provider.errors[index]
	}
	if index >= len(provider.responses) {
		return agent.ProviderResponse{}, errors.New("unexpected Provider call")
	}
	return provider.responses[index], nil
}

func newQueryAgent(t *testing.T, msql agent.MSQLExecutor, provider agent.Provider) *agent.QueryAgent {
	t.Helper()
	queryAgent, err := agent.NewQueryAgent(agent.QueryAgentDependencies{
		MSQL: msql, Provider: provider,
		Clock: &queryClock{next: time.Date(2026, 8, 3, 15, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return queryAgent
}

func queryAuthorization(level protocolmsql.RiskLevel) protocolmsql.Authorization {
	return protocolmsql.Authorization{
		Version: protocolmsql.AuthorizationVersion, Actor: "agent:query-test",
		AuthorizedDatabases: []string{"work"}, DefaultLevel: level,
		DatabaseLevels: map[string]protocolmsql.RiskLevel{"work": level},
	}
}

func queryReadOnlyAuthorization(value protocolmsql.Authorization) protocolmsql.Authorization {
	value.DefaultLevel = protocolmsql.LevelRead
	value.Approval = nil
	value.AuthorizedDatabases = append([]string(nil), value.AuthorizedDatabases...)
	value.DatabaseLevels = map[string]protocolmsql.RiskLevel{"work": protocolmsql.LevelRead}
	return value
}

func expectedBootstrapFrame(
	t *testing.T,
	atlasRows []protocolmsql.Row,
	lexicalRows []protocolmsql.Row,
) agent.BootstrapFrame {
	t.Helper()
	frame := agent.BootstrapFrame{
		Version: agent.BootstrapFrameVersion, Usage: agent.BootstrapUsageNavigation,
		Catalog: agent.CatalogBootstrap{
			Entries: atlasRows, Snapshot: "atlas-1", Pages: 1, Complete: true,
		},
		Lexical: agent.LexicalBootstrap{Locations: lexicalRows, Snapshot: "lexical-1"},
		Roots:   []agent.RootPrefetch{},
	}
	for range 8 {
		encoded := mustJSON(t, frame)
		if frame.UTF8Bytes == len(encoded) {
			return frame
		}
		frame.UTF8Bytes = len(encoded)
	}
	t.Fatal("expected Bootstrap Frame byte receipt did not converge")
	return agent.BootstrapFrame{}
}

func queryProviderRequest(
	t *testing.T,
	question string,
	frame agent.BootstrapFrame,
	call *agent.ProviderToolCall,
	toolResult string,
	choice agent.ProviderToolChoice,
	budget agent.QueryBudget,
) agent.ProviderRequest {
	t.Helper()
	messages := []agent.ProviderMessage{{Role: agent.ProviderRoleSystem, Content: agent.QueryAgentSystemPrompt}}
	if call == nil {
		content := mustJSON(t, struct {
			Version   string               `json:"version"`
			Question  string               `json:"question"`
			Bootstrap agent.BootstrapFrame `json:"bootstrap"`
		}{Version: agent.QueryBootstrapContextVersion, Question: question, Bootstrap: frame})
		messages = append(messages, agent.ProviderMessage{Role: agent.ProviderRoleUser, Content: string(content)})
	} else {
		content := mustJSON(t, struct {
			Version  string `json:"version"`
			Question string `json:"question"`
		}{Version: agent.QueryCurrentContextVersion, Question: question})
		messages = append(messages,
			agent.ProviderMessage{Role: agent.ProviderRoleUser, Content: string(content)},
			agent.ProviderMessage{Role: agent.ProviderRoleAssistant, ToolCalls: []agent.ProviderToolCall{*call}},
			agent.ProviderMessage{Role: agent.ProviderRoleTool, ToolCallID: call.ID, Content: toolResult},
		)
	}
	return agent.ProviderRequest{
		Version: agent.ProviderRequestVersion, Model: "model-test", Messages: messages,
		Tools: []agent.ProviderTool{queryProviderTool()}, ToolChoice: choice,
		MaxOutputTokens: budget.MaxOutputTokens,
	}
}

func queryProviderTool() agent.ProviderTool {
	return agent.ProviderTool{
		Name:        agent.QueryExecuteMSQLToolName,
		Description: "Execute one complete read-only MSQL batch. Put independent reads in one batch.",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["source","statements"],"properties":{"source":{"type":"string","minLength":1,"maxLength":1048576},"statements":{"type":"array","minItems":1,"maxItems":32,"items":{"type":"object","additionalProperties":false,"properties":{"named":{"type":"object"},"positional":{"type":"array"}}}}}}`),
	}
}

func queryProviderResponse(reason agent.ProviderFinishReason, message agent.ProviderMessage) agent.ProviderResponse {
	return agent.ProviderResponse{
		Version: agent.ProviderResponseVersion, Model: "model-test", Message: message,
		FinishReason: reason,
		Usage:        agent.ProviderUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
	}
}

func queryEnvelope(requestID string, results ...protocolmsql.StatementResult) protocolmsql.Envelope {
	return protocolmsql.Envelope{
		Version: protocolmsql.EnvelopeVersion, RequestID: requestID, OK: true,
		Results: results, Warnings: []protocolmsql.Notice{},
	}
}

func queryMSQLRequest(
	source string,
	authorization protocolmsql.Authorization,
	parameters ...protocolmsql.Parameters,
) protocolmsql.Request {
	statements := make([]protocolmsql.StatementInput, len(parameters))
	for index := range parameters {
		statements[index] = protocolmsql.StatementInput{Parameters: parameters[index], Authorization: authorization}
	}
	return protocolmsql.Request{Version: protocolmsql.RequestVersion, Source: source, Statements: statements}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
