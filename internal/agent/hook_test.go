package agent_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/agent"
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
