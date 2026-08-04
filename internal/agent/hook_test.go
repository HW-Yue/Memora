package agent_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/agent"
	"github.com/HW-Yue/Memora/internal/agent/agenttest"
	protocolmsql "github.com/HW-Yue/Memora/protocol/msql"
)

func TestExternalAgentHookIsBoundedRedactedAndDeterministic(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	event := agent.TraceEvent{
		Version: agent.TraceEventVersion, RunID: "run-hook", SessionID: "session-trace", Sequence: 1, Turn: 1,
		Kind: agent.TraceKindMSQL, Operation: "msql.execute", StartedAt: base,
		FinishedAt: base.Add(2 * time.Millisecond), DurationMicros: 2_000, Status: agent.TraceStatusSucceeded,
		Input: agent.DigestTracePayload([]byte("secret question")), Output: agent.DigestTracePayload([]byte("secret result")),
	}
	hook, err := agent.NewExternalAgentHook(agent.ExternalAgentHookContext{
		HostSessionID: "host-session-1", Host: "codex", Model: "deepseek-v4-flash",
		SkillVersion: "skill/v1", ProtocolVersion: "msql/v1",
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	hook.Observe(event)
	hook.Observe(event)
	snapshot := hook.Snapshot()
	if snapshot.Validate() != nil || len(snapshot.Events) != 1 || snapshot.Dropped != 1 || snapshot.Events[0].Trace != event {
		t.Fatalf("snapshot=%#v validate=%v", snapshot, snapshot.Validate())
	}
	encoded, _ := json.Marshal(snapshot)
	if strings.Contains(string(encoded), "secret question") || strings.Contains(string(encoded), "secret result") {
		t.Fatalf("hook leaked payload: %s", encoded)
	}
	second := hook.Snapshot()
	if !reflect.DeepEqual(snapshot, second) {
		t.Fatalf("snapshot changed without append: %#v / %#v", snapshot, second)
	}
}

func TestExternalAgentHookConcurrentAppendAndUnknownSession(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	hook, err := agent.NewExternalAgentHook(agent.ExternalAgentHookContext{Host: "claude-code"}, 64)
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for index := 0; index < 32; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			hook.Observe(agent.TraceEvent{
				Version: agent.TraceEventVersion, RunID: "run-hook", SessionID: "trace-session", Sequence: uint64(index + 1), Turn: 1,
				Kind: agent.TraceKindTool, Operation: "tool.execute_msql", StartedAt: base,
				FinishedAt: base.Add(time.Duration(index+1) * time.Microsecond), DurationMicros: uint64(index + 1),
				Status: agent.TraceStatusSucceeded, Input: agent.DigestTracePayload(nil), Output: agent.DigestTracePayload(nil),
			})
		}(index)
	}
	group.Wait()
	snapshot := hook.Snapshot()
	if snapshot.Context.HostSessionID != "unknown" || snapshot.Dropped != 0 || len(snapshot.Events) != 32 || snapshot.Validate() != nil {
		t.Fatalf("snapshot=%#v validate=%v", snapshot, snapshot.Validate())
	}
}

func TestQueryAgentWithHookOnlyPublishesRedactedTrace(t *testing.T) {
	t.Parallel()
	question := "find the hook note"
	authorization := queryAuthorization(protocolmsql.LevelRead)
	readOnly := queryReadOnlyAuthorization(authorization)
	bootstrapBudget := agent.DefaultBootstrapBudget()
	bootstrapBudget.RootTableLimit = 0
	queryBudget := agent.DefaultQueryBudget()
	atlasRows := []protocolmsql.Row{{"kind": "table", "database_id": "db", "database": "work", "table_id": "table", "table": "notes", "purpose": "hook"}}
	bootstrap := bootstrapEnvelope(atlasResult(0, atlasRows, page("atlas-hook", false, "")), lexicalResult(1, []protocolmsql.Row{}, page("lexical-hook", false, "")))
	selectSource := "SELECT value FROM `work`.`notes` WHERE row_id = :row LIMIT 1"
	selectRequest := protocolmsql.Request{Version: protocolmsql.RequestVersion, Source: selectSource, Statements: []protocolmsql.StatementInput{{Parameters: protocolmsql.Parameters{Named: map[string]any{"row": "row-hook"}}, Authorization: readOnly}}}
	selectEnvelope := queryEnvelope("select-hook", protocolmsql.StatementResult{Index: 0, Statement: "SELECT", Source: selectSource, Status: "succeeded", Columns: []protocolmsql.Column{{Name: "value", Type: "TEXT"}}, Rows: []protocolmsql.Row{{"value": "fact-hook"}}, Warnings: []protocolmsql.Notice{}})
	toolCall := agent.ProviderToolCall{ID: "call-hook", Name: agent.QueryExecuteMSQLToolName, Arguments: json.RawMessage(`{"source":"SELECT value FROM ` + "`work`.`notes`" + ` WHERE row_id = :row LIMIT 1","statements":[{"named":{"row":"row-hook"}}]}`)}
	provider := &recordingQueryProvider{responses: []agent.ProviderResponse{
		queryProviderResponse(agent.ProviderFinishToolCalls, agent.ProviderMessage{Role: agent.ProviderRoleAssistant, ToolCalls: []agent.ProviderToolCall{toolCall}}),
		queryProviderResponse(agent.ProviderFinishStop, agent.ProviderMessage{Role: agent.ProviderRoleAssistant, Content: "fact-hook"}),
	}}
	msql := agenttest.NewScriptedMSQL(
		agenttest.MSQLStep{Request: initialBootstrapRequest(question, readOnly, bootstrapBudget), Envelope: bootstrap},
		agenttest.MSQLStep{Request: selectRequest, Envelope: selectEnvelope},
	)
	queryAgent := newQueryAgent(t, msql, provider)
	hook, err := agent.NewExternalAgentHook(agent.ExternalAgentHookContext{HostSessionID: "host-session", Host: "codex", Model: "model-test", SkillVersion: "skill/v1", ProtocolVersion: "msql/v1"}, 16)
	if err != nil {
		t.Fatal(err)
	}
	result, err := queryAgent.QueryWithHook(context.Background(), agent.QueryRequest{Identity: agent.TraceIdentity{RunID: "run-hook-query", SessionID: "session-hook"}, Question: question, Model: "model-test", Authorization: authorization, BootstrapBudget: bootstrapBudget, Budget: queryBudget}, hook)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := hook.Snapshot()
	if result.Answer != "fact-hook" || snapshot.Validate() != nil || len(snapshot.Events) == 0 || snapshot.Context.HostSessionID != "host-session" {
		t.Fatalf("result=%#v hook=%#v validate=%v", result, snapshot, snapshot.Validate())
	}
	encoded, _ := json.Marshal(snapshot)
	if strings.Contains(string(encoded), question) || strings.Contains(string(encoded), "fact-hook") || strings.Contains(string(encoded), selectSource) {
		t.Fatalf("hook leaked host or row body: %s", encoded)
	}
}
