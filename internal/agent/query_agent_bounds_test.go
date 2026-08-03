package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/agent"
	"github.com/HW-Yue/Memora/internal/agent/agenttest"
	protocolmsql "github.com/HW-Yue/Memora/protocol/msql"
)

func TestQueryAgentCollectsEverySuccessfulSelectFromOneBatch(t *testing.T) {
	t.Parallel()

	question := "Compare the two decisions"
	authorization := queryAuthorization(protocolmsql.LevelWrite)
	readOnly := queryReadOnlyAuthorization(authorization)
	bootstrapBudget := queryBootstrapBudget()
	source := "SELECT decision FROM work.notes WHERE row_id = :first LIMIT 1; " +
		"SHOW ROUTES FROM TABLE work.notes AT ROOT LIMIT 1; " +
		"SELECT decision FROM work.notes WHERE row_id = :second LIMIT 1"
	arguments := queryArguments(t, source,
		map[string]any{"first": "row-1"},
		map[string]any{},
		map[string]any{"second": "row-2"},
	)
	call := agent.ProviderToolCall{ID: "call-batch", Name: agent.QueryExecuteMSQLToolName, Arguments: arguments}
	first := protocolmsql.StatementResult{
		Index: 0, Statement: "SELECT", Source: "first", Status: "succeeded",
		Columns: []protocolmsql.Column{}, Rows: []protocolmsql.Row{{"decision": "one"}},
		Warnings: []protocolmsql.Notice{},
	}
	navigation := protocolmsql.StatementResult{
		Index: 1, Statement: "SHOW_ROUTES", Source: "routes", Status: "succeeded",
		Columns: []protocolmsql.Column{}, Rows: []protocolmsql.Row{{"route_id": "route"}},
		Warnings: []protocolmsql.Notice{},
	}
	second := protocolmsql.StatementResult{
		Index: 2, Statement: "SELECT", Source: "second", Status: "succeeded",
		Columns: []protocolmsql.Column{}, Rows: []protocolmsql.Row{{"decision": "two"}},
		Warnings: []protocolmsql.Notice{},
	}
	envelope := queryEnvelope("batch-select", first, navigation, second)
	provider := &recordingQueryProvider{responses: []agent.ProviderResponse{
		queryProviderResponse(agent.ProviderFinishToolCalls, agent.ProviderMessage{
			Role: agent.ProviderRoleAssistant, ToolCalls: []agent.ProviderToolCall{call},
		}),
		queryProviderResponse(agent.ProviderFinishStop, agent.ProviderMessage{
			Role: agent.ProviderRoleAssistant, Content: "The decisions are one and two.",
		}),
	}}
	msql := agenttest.NewScriptedMSQL(
		queryBootstrapStep(question, readOnly, bootstrapBudget),
		agenttest.MSQLStep{
			Request: queryMSQLRequest(source, readOnly,
				protocolmsql.Parameters{Named: map[string]any{"first": "row-1"}},
				protocolmsql.Parameters{Named: map[string]any{}},
				protocolmsql.Parameters{Named: map[string]any{"second": "row-2"}},
			),
			Envelope: envelope,
		},
	)
	result, err := newQueryAgent(t, msql, provider).Query(context.Background(), queryRequest(
		"run-batch", "session-batch", question, authorization, bootstrapBudget, agent.DefaultQueryBudget(),
	))
	if err != nil {
		t.Fatal(err)
	}
	want := []agent.SelectEvidence{{RequestID: "batch-select", Result: first}, {RequestID: "batch-select", Result: second}}
	if !reflect.DeepEqual(result.Evidence, want) {
		t.Fatalf("evidence = %#v, want %#v", result.Evidence, want)
	}
	first.Rows[0]["decision"] = "mutated after Query"
	if result.Evidence[0].Result.Rows[0]["decision"] != "one" {
		t.Fatalf("evidence retained executor aliases: %#v", result.Evidence)
	}
	if err := msql.Verify(); err != nil {
		t.Fatal(err)
	}
}

func TestQueryAgentTransactionScannerIgnoresQuotedAndCommentedWords(t *testing.T) {
	t.Parallel()

	question := "Read the label"
	authorization := queryAuthorization(protocolmsql.LevelRead)
	readOnly := queryReadOnlyAuthorization(authorization)
	bootstrapBudget := queryBootstrapBudget()
	source := "SELECT 'BEGIN', \"COMMIT\", `ROLLBACK` FROM work.notes LIMIT 1 /* START TRANSACTION */"
	arguments := queryArguments(t, source, map[string]any{})
	call := agent.ProviderToolCall{ID: "call-quoted", Name: agent.QueryExecuteMSQLToolName, Arguments: arguments}
	envelope := queryEnvelope("quoted-select", protocolmsql.StatementResult{
		Index: 0, Statement: "SELECT", Source: source, Status: "succeeded",
		Columns: []protocolmsql.Column{}, Rows: []protocolmsql.Row{{"value": "BEGIN"}},
		Warnings: []protocolmsql.Notice{},
	})
	provider := &recordingQueryProvider{responses: []agent.ProviderResponse{
		queryProviderResponse(agent.ProviderFinishToolCalls, agent.ProviderMessage{
			Role: agent.ProviderRoleAssistant, ToolCalls: []agent.ProviderToolCall{call},
		}),
		queryProviderResponse(agent.ProviderFinishStop, agent.ProviderMessage{
			Role: agent.ProviderRoleAssistant, Content: "The label is BEGIN.",
		}),
	}}
	msql := agenttest.NewScriptedMSQL(
		queryBootstrapStep(question, readOnly, bootstrapBudget),
		agenttest.MSQLStep{
			Request:  queryMSQLRequest(source, readOnly, protocolmsql.Parameters{Named: map[string]any{}}),
			Envelope: envelope,
		},
	)
	result, err := newQueryAgent(t, msql, provider).Query(context.Background(), queryRequest(
		"run-quoted", "session-quoted", question, authorization, bootstrapBudget, agent.DefaultQueryBudget(),
	))
	if err != nil || result.Answer == "" {
		t.Fatalf("Query() = %#v, %v", result, err)
	}
	if err := msql.Verify(); err != nil {
		t.Fatal(err)
	}
}

func TestQueryAgentDecodesJSONNumbersWithoutFloatCoercion(t *testing.T) {
	t.Parallel()

	question := "Read revision seven"
	authorization := queryAuthorization(protocolmsql.LevelRead)
	readOnly := queryReadOnlyAuthorization(authorization)
	bootstrapBudget := queryBootstrapBudget()
	source := "SELECT revision FROM work.notes WHERE revision = :revision LIMIT 1"
	arguments := queryArguments(t, source, map[string]any{"revision": 7})
	call := agent.ProviderToolCall{ID: "call-number", Name: agent.QueryExecuteMSQLToolName, Arguments: arguments}
	provider := &recordingQueryProvider{responses: []agent.ProviderResponse{
		queryProviderResponse(agent.ProviderFinishToolCalls, agent.ProviderMessage{
			Role: agent.ProviderRoleAssistant, ToolCalls: []agent.ProviderToolCall{call},
		}),
		queryProviderResponse(agent.ProviderFinishStop, agent.ProviderMessage{
			Role: agent.ProviderRoleAssistant, Content: "Revision seven exists.",
		}),
	}}
	msql := agenttest.NewScriptedMSQL(
		queryBootstrapStep(question, readOnly, bootstrapBudget),
		agenttest.MSQLStep{
			Request: queryMSQLRequest(source, readOnly, protocolmsql.Parameters{Named: map[string]any{"revision": int64(7)}}),
			Envelope: queryEnvelope("number", protocolmsql.StatementResult{
				Index: 0, Statement: "SELECT", Source: source, Status: "succeeded",
				Columns: []protocolmsql.Column{}, Rows: []protocolmsql.Row{{"revision": int64(7)}},
				Warnings: []protocolmsql.Notice{},
			}),
		},
	)
	result, err := newQueryAgent(t, msql, provider).Query(context.Background(), queryRequest(
		"run-number", "session-number", question, authorization, bootstrapBudget, agent.DefaultQueryBudget(),
	))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result.Evidence[0].Result.Rows[0]["revision"].(int64); !ok {
		t.Fatalf("evidence number type = %T", result.Evidence[0].Result.Rows[0]["revision"])
	}
	if err := msql.Verify(); err != nil {
		t.Fatal(err)
	}
}

func TestQueryAgentRejectsEachExplicitTransactionForm(t *testing.T) {
	t.Parallel()

	for _, source := range []string{
		"BEGIN; SELECT * FROM work.notes LIMIT 1",
		"START /* gap */ TRANSACTION; SELECT * FROM work.notes LIMIT 1",
		"SELECT * FROM work.notes LIMIT 1; COMMIT",
		"ROLLBACK",
	} {
		t.Run(source, func(t *testing.T) {
			question := "question"
			authorization := queryAuthorization(protocolmsql.LevelRead)
			readOnly := queryReadOnlyAuthorization(authorization)
			bootstrapBudget := queryBootstrapBudget()
			call := agent.ProviderToolCall{
				ID: "call-transaction", Name: agent.QueryExecuteMSQLToolName,
				Arguments: queryArguments(t, source, map[string]any{}),
			}
			provider := &recordingQueryProvider{responses: []agent.ProviderResponse{
				queryProviderResponse(agent.ProviderFinishToolCalls, agent.ProviderMessage{
					Role: agent.ProviderRoleAssistant, ToolCalls: []agent.ProviderToolCall{call},
				}),
			}}
			msql := agenttest.NewScriptedMSQL(queryBootstrapStep(question, readOnly, bootstrapBudget))
			result, err := newQueryAgent(t, msql, provider).Query(context.Background(), queryRequest(
				"run-transaction-form", "session-transaction-form", question, authorization,
				bootstrapBudget, agent.DefaultQueryBudget(),
			))
			if !errors.Is(err, agent.ErrQueryTransactionControl) {
				t.Fatalf("Query() error = %v", err)
			}
			if result.Trace.Summary.StatusCounts[agent.TraceStatusFailed] != 1 || len(msql.Calls()) != 1 {
				t.Fatalf("Trace=%#v MSQL calls=%d", result.Trace, len(msql.Calls()))
			}
		})
	}
}

func TestQueryAgentRejectsWrongToolAndMalformedArguments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		toolName  string
		arguments json.RawMessage
		want      error
	}{
		{
			name: "wrong tool", toolName: "direct_database_read",
			arguments: queryArguments(t, "SELECT * FROM work.notes LIMIT 1", map[string]any{}),
			want:      agent.ErrInvalidQueryToolCall,
		},
		{
			name: "unknown field", toolName: agent.QueryExecuteMSQLToolName,
			arguments: json.RawMessage(`{"source":"SELECT * FROM work.notes LIMIT 1","statements":[{}],"authorization":"model-owned"}`),
			want:      agent.ErrInvalidQueryToolCall,
		},
		{
			name: "missing statements", toolName: agent.QueryExecuteMSQLToolName,
			arguments: json.RawMessage(`{"source":"SELECT * FROM work.notes LIMIT 1"}`),
			want:      agent.ErrInvalidQueryToolCall,
		},
		{
			name: "trailing JSON", toolName: agent.QueryExecuteMSQLToolName,
			arguments: json.RawMessage(`{"source":"SELECT * FROM work.notes LIMIT 1","statements":[{}]} {}`),
			want:      agent.ErrInvalidProviderResponse,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			question := "question"
			authorization := queryAuthorization(protocolmsql.LevelRead)
			readOnly := queryReadOnlyAuthorization(authorization)
			bootstrapBudget := queryBootstrapBudget()
			provider := &recordingQueryProvider{responses: []agent.ProviderResponse{
				queryProviderResponse(agent.ProviderFinishToolCalls, agent.ProviderMessage{
					Role: agent.ProviderRoleAssistant, ToolCalls: []agent.ProviderToolCall{{
						ID: "call-invalid", Name: test.toolName, Arguments: test.arguments,
					}},
				}),
			}}
			msql := agenttest.NewScriptedMSQL(queryBootstrapStep(question, readOnly, bootstrapBudget))
			result, err := newQueryAgent(t, msql, provider).Query(context.Background(), queryRequest(
				"run-invalid-wire", "session-invalid-wire", question, authorization,
				bootstrapBudget, agent.DefaultQueryBudget(),
			))
			if !errors.Is(err, test.want) {
				t.Fatalf("Query() error = %v, want %v", err, test.want)
			}
			if len(msql.Calls()) != 1 || len(provider.requests) != 1 {
				t.Fatalf("calls=%d/%d Trace=%#v", len(msql.Calls()), len(provider.requests), result.Trace)
			}
			if validationErr := result.Trace.Validate(); validationErr != nil {
				t.Fatalf("Trace Validate() = %v", validationErr)
			}
		})
	}
}

func TestQueryAgentEnforcesToolResultAnswerAndStepBudgets(t *testing.T) {
	t.Parallel()

	t.Run("tool arguments", func(t *testing.T) {
		budget := agent.DefaultQueryBudget()
		budget.ToolArgumentsUTF8Bytes = 256
		arguments := queryArguments(t, "SELECT '"+strings.Repeat("x", 300)+"' FROM work.notes LIMIT 1", map[string]any{})
		result, err, msql, provider := queryOneToolFailure(t, budget, arguments, protocolmsql.Envelope{}, nil)
		if !errors.Is(err, agent.ErrQueryBudget) || len(msql.Calls()) != 1 || len(provider.requests) != 1 {
			t.Fatalf("Query() = %#v, %v calls=%d/%d", result, err, len(msql.Calls()), len(provider.requests))
		}
	})

	t.Run("tool result", func(t *testing.T) {
		budget := agent.DefaultQueryBudget()
		budget.ToolResultUTF8Bytes = 512
		source := "SELECT body FROM work.notes LIMIT 1"
		envelope := queryEnvelope("large-result", protocolmsql.StatementResult{
			Index: 0, Statement: "SELECT", Source: source, Status: "succeeded",
			Columns: []protocolmsql.Column{}, Rows: []protocolmsql.Row{{"body": strings.Repeat("x", 600)}},
			Warnings: []protocolmsql.Notice{},
		})
		result, err, msql, provider := queryOneToolFailure(
			t, budget, queryArguments(t, source, map[string]any{}), envelope, nil,
		)
		if !errors.Is(err, agent.ErrQueryBudget) || len(msql.Calls()) != 2 || len(provider.requests) != 1 ||
			result.Trace.Summary.StatusCounts[agent.TraceStatusFailed] != 1 {
			t.Fatalf("Query() = %#v, %v calls=%d/%d", result, err, len(msql.Calls()), len(provider.requests))
		}
	})

	t.Run("final answer", func(t *testing.T) {
		budget := agent.DefaultQueryBudget()
		budget.AnswerUTF8Bytes = 4
		result, err, _, provider := querySuccessfulToolThenFinal(t, budget,
			queryProviderResponse(agent.ProviderFinishStop, agent.ProviderMessage{
				Role: agent.ProviderRoleAssistant, Content: "answer exceeds four bytes",
			}),
		)
		if !errors.Is(err, agent.ErrQueryBudget) || len(provider.requests) != 2 || result.Answer != "" {
			t.Fatalf("Query() = %#v, %v calls=%d", result, err, len(provider.requests))
		}
	})

	t.Run("length finish", func(t *testing.T) {
		result, err, _, provider := querySuccessfulToolThenFinal(t, agent.DefaultQueryBudget(),
			queryProviderResponse(agent.ProviderFinishLength, agent.ProviderMessage{
				Role: agent.ProviderRoleAssistant, Content: "partial",
			}),
		)
		if !errors.Is(err, agent.ErrQueryIncompleteAnswer) || len(provider.requests) != 2 || result.Answer != "" {
			t.Fatalf("Query() = %#v, %v calls=%d", result, err, len(provider.requests))
		}
	})

	t.Run("tool call limit", func(t *testing.T) {
		question := "question"
		authorization := queryAuthorization(protocolmsql.LevelRead)
		readOnly := queryReadOnlyAuthorization(authorization)
		bootstrapBudget := queryBootstrapBudget()
		budget := agent.DefaultQueryBudget()
		budget.MaxProviderCalls = 2
		budget.MaxToolCalls = 1
		navigateSource := "SHOW ROUTES FROM TABLE work.notes AT ROOT LIMIT 1"
		provider := &recordingQueryProvider{responses: []agent.ProviderResponse{
			queryProviderResponse(agent.ProviderFinishToolCalls, agent.ProviderMessage{
				Role: agent.ProviderRoleAssistant, ToolCalls: []agent.ProviderToolCall{{
					ID: "call-one", Name: agent.QueryExecuteMSQLToolName,
					Arguments: queryArguments(t, navigateSource, map[string]any{}),
				}},
			}),
			queryProviderResponse(agent.ProviderFinishToolCalls, agent.ProviderMessage{
				Role: agent.ProviderRoleAssistant, ToolCalls: []agent.ProviderToolCall{{
					ID: "call-two", Name: agent.QueryExecuteMSQLToolName,
					Arguments: queryArguments(t, navigateSource, map[string]any{}),
				}},
			}),
		}}
		msql := agenttest.NewScriptedMSQL(
			queryBootstrapStep(question, readOnly, bootstrapBudget),
			agenttest.MSQLStep{
				Request: queryMSQLRequest(navigateSource, readOnly, protocolmsql.Parameters{Named: map[string]any{}}),
				Envelope: queryEnvelope("navigate", protocolmsql.StatementResult{
					Index: 0, Statement: "SHOW_ROUTES", Source: navigateSource, Status: "succeeded",
					Columns: []protocolmsql.Column{}, Rows: []protocolmsql.Row{}, Warnings: []protocolmsql.Notice{},
				}),
			},
		)
		result, err := newQueryAgent(t, msql, provider).Query(context.Background(), queryRequest(
			"run-limit", "session-limit", question, authorization, bootstrapBudget, budget,
		))
		if !errors.Is(err, agent.ErrQueryBudget) || len(provider.requests) != 2 || len(msql.Calls()) != 2 ||
			result.Trace.Summary.StatusCounts[agent.TraceStatusFailed] != 1 {
			t.Fatalf("Query() = %#v, %v calls=%d/%d", result, err, len(msql.Calls()), len(provider.requests))
		}
	})
}

func TestQueryAgentDoesNotRetryProviderMSQLOrCancellation(t *testing.T) {
	t.Parallel()

	t.Run("Provider", func(t *testing.T) {
		wantErr := errors.New("provider unavailable with secret body")
		question := "question"
		authorization := queryAuthorization(protocolmsql.LevelRead)
		readOnly := queryReadOnlyAuthorization(authorization)
		bootstrapBudget := queryBootstrapBudget()
		provider := &recordingQueryProvider{errors: []error{wantErr}}
		msql := agenttest.NewScriptedMSQL(queryBootstrapStep(question, readOnly, bootstrapBudget))
		result, err := newQueryAgent(t, msql, provider).Query(context.Background(), queryRequest(
			"run-provider-error", "session-provider-error", question, authorization,
			bootstrapBudget, agent.DefaultQueryBudget(),
		))
		if !errors.Is(err, wantErr) || len(provider.requests) != 1 || len(msql.Calls()) != 1 ||
			result.Trace.Summary.StatusCounts[agent.TraceStatusFailed] != 1 {
			t.Fatalf("Query() = %#v, %v calls=%d/%d", result, err, len(msql.Calls()), len(provider.requests))
		}
		if strings.Contains(string(mustJSON(t, result.Trace)), "secret body") {
			t.Fatalf("Trace leaked Provider error: %s", mustJSON(t, result.Trace))
		}
	})

	t.Run("MSQL", func(t *testing.T) {
		wantErr := errors.New("MSQL unavailable with row body")
		result, err, msql, provider := queryOneToolFailure(
			t, agent.DefaultQueryBudget(),
			queryArguments(t, "SELECT body FROM work.notes LIMIT 1", map[string]any{}),
			protocolmsql.Envelope{}, wantErr,
		)
		if !errors.Is(err, wantErr) || len(provider.requests) != 1 || len(msql.Calls()) != 2 ||
			result.Trace.Summary.StatusCounts[agent.TraceStatusFailed] != 2 {
			t.Fatalf("Query() = %#v, %v calls=%d/%d", result, err, len(msql.Calls()), len(provider.requests))
		}
		if strings.Contains(string(mustJSON(t, result.Trace)), "row body") {
			t.Fatalf("Trace leaked MSQL error: %s", mustJSON(t, result.Trace))
		}
	})

	t.Run("cancellation", func(t *testing.T) {
		result, err, msql, provider := queryOneToolFailure(
			t, agent.DefaultQueryBudget(),
			queryArguments(t, "SELECT body FROM work.notes LIMIT 1", map[string]any{}),
			protocolmsql.Envelope{}, context.Canceled,
		)
		if !errors.Is(err, context.Canceled) || len(provider.requests) != 1 || len(msql.Calls()) != 2 ||
			result.Trace.Summary.StatusCounts[agent.TraceStatusCancelled] != 2 {
			t.Fatalf("Query() = %#v, %v calls=%d/%d", result, err, len(msql.Calls()), len(provider.requests))
		}
	})
}

func TestQueryAgentValidatesDependenciesAndRequestBeforeExternalCalls(t *testing.T) {
	t.Parallel()

	clock := &queryClock{next: time.Date(2026, 8, 3, 16, 0, 0, 0, time.UTC)}
	provider := &recordingQueryProvider{}
	msql := agenttest.NewScriptedMSQL()
	for _, dependencies := range []agent.QueryAgentDependencies{
		{},
		{MSQL: msql, Clock: clock},
		{Provider: provider, Clock: clock},
		{MSQL: msql, Provider: provider},
	} {
		if _, err := agent.NewQueryAgent(dependencies); !errors.Is(err, agent.ErrInvalidQueryDependencies) {
			t.Fatalf("NewQueryAgent(%#v) error = %v", dependencies, err)
		}
	}
	queryAgent := newQueryAgent(t, msql, provider)
	_, err := queryAgent.Query(context.Background(), agent.QueryRequest{})
	if !errors.Is(err, agent.ErrInvalidQueryRequest) || len(msql.Calls()) != 0 || len(provider.requests) != 0 {
		t.Fatalf("Query() error=%v calls=%d/%d", err, len(msql.Calls()), len(provider.requests))
	}
	request := queryRequest(
		"run-invalid-profile", "session-invalid-profile", "question",
		queryAuthorization(protocolmsql.LevelRead), queryBootstrapBudget(), agent.DefaultQueryBudget(),
	)
	request.BootstrapProfile = agent.BootstrapProfile("unknown-v1")
	_, err = queryAgent.Query(context.Background(), request)
	if !errors.Is(err, agent.ErrInvalidQueryRequest) || len(msql.Calls()) != 0 || len(provider.requests) != 0 {
		t.Fatalf("unknown profile error=%v calls=%d/%d", err, len(msql.Calls()), len(provider.requests))
	}
}

func queryOneToolFailure(
	t *testing.T,
	budget agent.QueryBudget,
	arguments json.RawMessage,
	envelope protocolmsql.Envelope,
	msqlErr error,
) (agent.QueryResult, error, *agenttest.ScriptedMSQL, *recordingQueryProvider) {
	t.Helper()
	question := "question"
	authorization := queryAuthorization(protocolmsql.LevelRead)
	readOnly := queryReadOnlyAuthorization(authorization)
	bootstrapBudget := queryBootstrapBudget()
	call := agent.ProviderToolCall{ID: "call-one", Name: agent.QueryExecuteMSQLToolName, Arguments: arguments}
	provider := &recordingQueryProvider{responses: []agent.ProviderResponse{
		queryProviderResponse(agent.ProviderFinishToolCalls, agent.ProviderMessage{
			Role: agent.ProviderRoleAssistant, ToolCalls: []agent.ProviderToolCall{call},
		}),
	}}
	steps := []agenttest.MSQLStep{queryBootstrapStep(question, readOnly, bootstrapBudget)}
	if uint64(len(arguments)) <= budget.ToolArgumentsUTF8Bytes {
		var decoded struct {
			Source string `json:"source"`
		}
		_ = json.Unmarshal(arguments, &decoded)
		steps = append(steps, agenttest.MSQLStep{
			Request:  queryMSQLRequest(decoded.Source, readOnly, protocolmsql.Parameters{Named: map[string]any{}}),
			Envelope: envelope, Error: msqlErr,
		})
	}
	msql := agenttest.NewScriptedMSQL(steps...)
	result, err := newQueryAgent(t, msql, provider).Query(context.Background(), queryRequest(
		"run-one-tool", "session-one-tool", question, authorization, bootstrapBudget, budget,
	))
	return result, err, msql, provider
}

func querySuccessfulToolThenFinal(
	t *testing.T,
	budget agent.QueryBudget,
	final agent.ProviderResponse,
) (agent.QueryResult, error, *agenttest.ScriptedMSQL, *recordingQueryProvider) {
	t.Helper()
	question := "question"
	authorization := queryAuthorization(protocolmsql.LevelRead)
	readOnly := queryReadOnlyAuthorization(authorization)
	bootstrapBudget := queryBootstrapBudget()
	source := "SELECT body FROM work.notes LIMIT 1"
	call := agent.ProviderToolCall{
		ID: "call-select", Name: agent.QueryExecuteMSQLToolName,
		Arguments: queryArguments(t, source, map[string]any{}),
	}
	provider := &recordingQueryProvider{responses: []agent.ProviderResponse{
		queryProviderResponse(agent.ProviderFinishToolCalls, agent.ProviderMessage{
			Role: agent.ProviderRoleAssistant, ToolCalls: []agent.ProviderToolCall{call},
		}),
		final,
	}}
	msql := agenttest.NewScriptedMSQL(
		queryBootstrapStep(question, readOnly, bootstrapBudget),
		agenttest.MSQLStep{
			Request: queryMSQLRequest(source, readOnly, protocolmsql.Parameters{Named: map[string]any{}}),
			Envelope: queryEnvelope("select", protocolmsql.StatementResult{
				Index: 0, Statement: "SELECT", Source: source, Status: "succeeded",
				Columns: []protocolmsql.Column{}, Rows: []protocolmsql.Row{{"body": "fact"}},
				Warnings: []protocolmsql.Notice{},
			}),
		},
	)
	result, err := newQueryAgent(t, msql, provider).Query(context.Background(), queryRequest(
		"run-final", "session-final", question, authorization, bootstrapBudget, budget,
	))
	return result, err, msql, provider
}

func queryRequest(
	runID string,
	sessionID string,
	question string,
	authorization protocolmsql.Authorization,
	bootstrapBudget agent.BootstrapBudget,
	budget agent.QueryBudget,
) agent.QueryRequest {
	return agent.QueryRequest{
		Identity: agent.TraceIdentity{RunID: runID, SessionID: sessionID}, Question: question,
		Model: "model-test", Authorization: authorization, BootstrapBudget: bootstrapBudget, Budget: budget,
	}
}

func queryBootstrapBudget() agent.BootstrapBudget {
	budget := agent.DefaultBootstrapBudget()
	budget.RootTableLimit = 0
	return budget
}

func queryBootstrapStep(
	question string,
	authorization protocolmsql.Authorization,
	budget agent.BootstrapBudget,
) agenttest.MSQLStep {
	return agenttest.MSQLStep{
		Request: initialBootstrapRequest(question, authorization, budget),
		Envelope: bootstrapEnvelope(
			atlasResult(0, []protocolmsql.Row{}, page("atlas", false, "")),
			lexicalResult(1, []protocolmsql.Row{}, page("lexical", false, "")),
		),
	}
}

func queryArguments(t *testing.T, source string, named ...map[string]any) json.RawMessage {
	t.Helper()
	statements := make([]map[string]any, len(named))
	for index, parameters := range named {
		statements[index] = map[string]any{"named": parameters}
	}
	return mustJSON(t, map[string]any{"source": source, "statements": statements})
}
