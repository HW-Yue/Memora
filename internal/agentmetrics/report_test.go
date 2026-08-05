package agentmetrics_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/agent"
	"github.com/HW-Yue/Memora/internal/agentmetrics"
)

func TestAggregateHookSnapshotsBySessionAndTurn(t *testing.T) {
	base := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	first := hookEnvelope("host-a", "run-2", "session-2", []agent.TraceEvent{
		traceEvent(base, "run-2", "session-2", 1, 1, agent.TraceKindProvider, "provider.complete", agent.TraceStatusSucceeded, 3, 7, 10),
		traceEvent(base, "run-2", "session-2", 2, 1, agent.TraceKindMSQL, "msql.execute", agent.TraceStatusFailed, 4, 0, 0),
	})
	duplicate := first
	second := hookEnvelope("host-b", "run-1", "session-1", []agent.TraceEvent{
		traceEvent(base, "run-1", "session-1", 1, 2, agent.TraceKindTool, "tool.execute_msql_fallback", agent.TraceStatusSucceeded, 5, 0, 0),
	})
	report, err := agentmetrics.Aggregate([]agent.ExternalAgentHookEnvelope{second, first, duplicate})
	if err != nil {
		t.Fatal(err)
	}
	if report.Validate() != nil || report.EnvelopeCount != 3 || report.EventCount != 3 || len(report.Sessions) != 2 {
		t.Fatalf("report=%#v validate=%v", report, report.Validate())
	}
	if report.Sessions[0].RunID != "run-2" || report.Sessions[1].RunID != "run-1" || len(report.Sessions[0].Turns) != 1 {
		t.Fatalf("session ordering=%#v", report.Sessions)
	}
	metrics := report.Sessions[0].Metrics
	if metrics.ProviderCalls != 1 || metrics.MSQLCalls != 1 || metrics.InputBytes != 7 || metrics.OutputBytes != 10 ||
		metrics.InputTokens != 3 || metrics.OutputTokens != 2 || metrics.DurationMicros != 7 || metrics.FailureCounts["MSQL_FAILED"] != 1 {
		t.Fatalf("session metrics=%#v", metrics)
	}
	if report.Sessions[1].Metrics.Fallbacks != 1 {
		t.Fatalf("fallback metrics=%#v", report.Sessions[1].Metrics)
	}
	encoded, err := json.Marshal(report)
	if err != nil || strings.Contains(string(encoded), "provider.complete") {
		t.Fatalf("report JSON=%s err=%v", encoded, err)
	}
}

func TestAggregateRejectsConflictingDuplicateAndEscapesHTML(t *testing.T) {
	base := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	event := traceEvent(base, "run", "session", 1, 1, agent.TraceKindTool, "tool.unsafe", agent.TraceStatusSucceeded, 1, 0, 0)
	first := hookEnvelope("<host>", "run", "session", []agent.TraceEvent{event})
	conflict := event
	conflict.Operation = "tool.changed"
	if _, err := agentmetrics.Aggregate([]agent.ExternalAgentHookEnvelope{first, hookEnvelope("<host>", "run", "session", []agent.TraceEvent{conflict})}); err == nil {
		t.Fatal("conflicting duplicate unexpectedly accepted")
	}
	report, err := agentmetrics.Aggregate([]agent.ExternalAgentHookEnvelope{first})
	if err != nil {
		t.Fatal(err)
	}
	html, err := agentmetrics.RenderHTML(report)
	if err != nil || strings.Contains(string(html), "<host>") || !strings.Contains(string(html), "&lt;host&gt;") {
		t.Fatalf("HTML=%s err=%v", html, err)
	}
}

func TestEmptyReportHasDeterministicJSONRoundTrip(t *testing.T) {
	report, err := agentmetrics.Aggregate(nil)
	if err != nil {
		t.Fatal(err)
	}
	first, err := agentmetrics.EncodeJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	second, err := agentmetrics.EncodeJSON(report)
	if err != nil || !bytes.Equal(first, second) {
		t.Fatalf("JSON is not deterministic: %q / %q, err=%v", first, second, err)
	}
	decoded, err := agentmetrics.DecodeJSON(first)
	if err != nil || decoded.EventCount != 0 || len(decoded.Sessions) != 0 {
		t.Fatalf("decoded=%#v err=%v", decoded, err)
	}
}

func TestDecodeHookJSONRejectsTrailingAndUnknownFields(t *testing.T) {
	valid := hookEnvelope("host", "run", "session", nil)
	encoded, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agentmetrics.DecodeHookJSON(append(encoded, []byte("\n{}")...)); err == nil {
		t.Fatal("trailing JSON unexpectedly accepted")
	}
	unknown := append([]byte(`{"version":"memora.external-agent-hook/v1","context":{"host_session_id":"h","host":"h","model":"m","skill_version":"s","protocol_version":"p"},"events":[],"dropped":0,"extra":true}`), '\n')
	if _, err := agentmetrics.DecodeHookJSON(unknown); err == nil {
		t.Fatal("unknown field unexpectedly accepted")
	}
}

func hookEnvelope(host, run, session string, events []agent.TraceEvent) agent.ExternalAgentHookEnvelope {
	hookEvents := make([]agent.ExternalAgentHookEvent, 0, len(events))
	for _, event := range events {
		hookEvents = append(hookEvents, agent.ExternalAgentHookEvent{Version: agent.ExternalAgentHookVersion,
			Context: agent.ExternalAgentHookContext{HostSessionID: host, Host: host, Model: "model", SkillVersion: "skill", ProtocolVersion: "msql"}, Trace: event})
	}
	return agent.ExternalAgentHookEnvelope{Version: agent.ExternalAgentHookVersion,
		Context: agent.ExternalAgentHookContext{HostSessionID: host, Host: host, Model: "model", SkillVersion: "skill", ProtocolVersion: "msql"}, Events: hookEvents}
}

func traceEvent(base time.Time, run, session string, sequence, turn uint64, kind agent.TraceKind, operation string, status agent.TraceStatus, duration, input, output uint64) agent.TraceEvent {
	event := agent.TraceEvent{Version: agent.TraceEventVersion, RunID: run, SessionID: session, Sequence: sequence, Turn: turn,
		Kind: kind, Operation: operation, StartedAt: base, FinishedAt: base.Add(time.Duration(duration) * time.Microsecond), DurationMicros: duration,
		Status: status, Input: agent.TraceDigest{Bytes: input, SHA256: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		Output: agent.TraceDigest{Bytes: output, SHA256: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}
	if kind == agent.TraceKindProvider {
		event.Model = "model"
		event.Usage = &agent.ProviderUsage{InputTokens: 3, CachedInputTokens: 1, OutputTokens: 2, TotalTokens: 5}
		event.Cost = &agent.TraceCost{Currency: "CNY", Micros: 7, PriceSnapshotID: "price"}
	}
	if status == agent.TraceStatusFailed {
		event.ErrorCode = "MSQL_FAILED"
	}
	return event
}
