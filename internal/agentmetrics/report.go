package agentmetrics

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"math"
	"reflect"
	"sort"
	"strings"

	"github.com/HW-Yue/Memora/internal/agent"
)

const Version = "memora.agent-metrics/v1"

var (
	ErrInvalidReport   = errors.New("agent metrics report is invalid")
	ErrMetricsOverflow = errors.New("agent metrics counter overflow")
)

type Metrics struct {
	Events         uint64            `json:"events"`
	ProviderCalls  uint64            `json:"provider_calls"`
	MSQLCalls      uint64            `json:"msql_calls"`
	ToolCalls      uint64            `json:"tool_calls"`
	BootstrapCalls uint64            `json:"bootstrap_calls"`
	Fallbacks      uint64            `json:"fallbacks"`
	InputBytes     uint64            `json:"input_bytes"`
	OutputBytes    uint64            `json:"output_bytes"`
	InputTokens    uint64            `json:"input_tokens"`
	CachedTokens   uint64            `json:"cached_input_tokens"`
	OutputTokens   uint64            `json:"output_tokens"`
	TotalTokens    uint64            `json:"total_tokens"`
	DurationMicros uint64            `json:"duration_micros"`
	StatusCounts   map[string]uint64 `json:"status_counts"`
	FailureCounts  map[string]uint64 `json:"failure_counts"`
	CostMicros     map[string]uint64 `json:"cost_micros"`
}

type Turn struct {
	Turn    uint64  `json:"turn"`
	Metrics Metrics `json:"metrics"`
}

type Session struct {
	Context   agent.ExternalAgentHookContext `json:"context"`
	RunID     string                         `json:"run_id"`
	SessionID string                         `json:"session_id"`
	Metrics   Metrics                        `json:"metrics"`
	Turns     []Turn                         `json:"turns"`
}

type Report struct {
	Version       string    `json:"version"`
	EnvelopeCount uint64    `json:"envelope_count"`
	EventCount    uint64    `json:"event_count"`
	Sessions      []Session `json:"sessions"`
}

type sessionKey struct {
	Context   agent.ExternalAgentHookContext
	RunID     string
	SessionID string
}

type sessionAccumulator struct {
	key     sessionKey
	metrics Metrics
	turns   map[uint64]*Metrics
}

// Aggregate consumes Hook snapshots. A Hook snapshot is allowed to be passed
// repeatedly: event identity is stable and duplicates are ignored, while a
// duplicate identity with different content is rejected.
func Aggregate(envelopes []agent.ExternalAgentHookEnvelope) (Report, error) {
	accumulators := make(map[sessionKey]*sessionAccumulator)
	seen := make(map[string]agent.TraceEvent)
	report := Report{Version: Version, EnvelopeCount: uint64(len(envelopes)), Sessions: []Session{}}
	for _, envelope := range envelopes {
		if err := envelope.Validate(); err != nil {
			return Report{}, fmt.Errorf("%w: hook envelope: %v", ErrInvalidReport, err)
		}
		for _, wrapped := range envelope.Events {
			event := wrapped.Trace
			key := eventKey(envelope.Context, event)
			if previous, exists := seen[key]; exists {
				if !reflect.DeepEqual(previous, event) {
					return Report{}, fmt.Errorf("%w: event %s changed across snapshots", ErrInvalidReport, key)
				}
				continue
			}
			seen[key] = event
			keyValue := sessionKey{Context: envelope.Context, RunID: event.RunID, SessionID: event.SessionID}
			bucket := accumulators[keyValue]
			if bucket == nil {
				bucket = &sessionAccumulator{key: keyValue, metrics: newMetrics(), turns: make(map[uint64]*Metrics)}
				accumulators[keyValue] = bucket
			}
			if err := bucket.metrics.add(event); err != nil {
				return Report{}, err
			}
			turn := bucket.turns[event.Turn]
			if turn == nil {
				value := newMetrics()
				turn = &value
				bucket.turns[event.Turn] = turn
			}
			if err := turn.add(event); err != nil {
				return Report{}, err
			}
		}
	}
	report.EventCount = uint64(len(seen))
	for _, bucket := range accumulators {
		session := Session{Context: bucket.key.Context, RunID: bucket.key.RunID, SessionID: bucket.key.SessionID,
			Metrics: bucket.metrics, Turns: make([]Turn, 0, len(bucket.turns))}
		for turnID, metrics := range bucket.turns {
			session.Turns = append(session.Turns, Turn{Turn: turnID, Metrics: *metrics})
		}
		sort.Slice(session.Turns, func(left, right int) bool { return session.Turns[left].Turn < session.Turns[right].Turn })
		report.Sessions = append(report.Sessions, session)
	}
	sort.Slice(report.Sessions, func(left, right int) bool { return sessionLess(report.Sessions[left], report.Sessions[right]) })
	if err := report.Validate(); err != nil {
		return Report{}, err
	}
	return report, nil
}

func (metrics *Metrics) add(event agent.TraceEvent) error {
	if metrics == nil {
		return ErrInvalidReport
	}
	for target, value := range map[*uint64]uint64{
		&metrics.Events: 1, &metrics.InputBytes: event.Input.Bytes, &metrics.OutputBytes: event.Output.Bytes,
		&metrics.DurationMicros: event.DurationMicros,
	} {
		if !checkedAdd(target, value) {
			return ErrMetricsOverflow
		}
	}
	switch event.Kind {
	case agent.TraceKindProvider:
		if !checkedAdd(&metrics.ProviderCalls, 1) || event.Usage == nil ||
			!checkedAdd(&metrics.InputTokens, event.Usage.InputTokens) || !checkedAdd(&metrics.CachedTokens, event.Usage.CachedInputTokens) ||
			!checkedAdd(&metrics.OutputTokens, event.Usage.OutputTokens) || !checkedAdd(&metrics.TotalTokens, event.Usage.TotalTokens) {
			return ErrMetricsOverflow
		}
	case agent.TraceKindMSQL:
		if !checkedAdd(&metrics.MSQLCalls, 1) {
			return ErrMetricsOverflow
		}
	case agent.TraceKindTool:
		if !checkedAdd(&metrics.ToolCalls, 1) {
			return ErrMetricsOverflow
		}
	case agent.TraceKindBootstrap:
		if !checkedAdd(&metrics.BootstrapCalls, 1) {
			return ErrMetricsOverflow
		}
	}
	if !checkedMapAdd(metrics.StatusCounts, string(event.Status), 1) {
		return ErrMetricsOverflow
	}
	if event.ErrorCode != "" && !checkedMapAdd(metrics.FailureCounts, event.ErrorCode, 1) {
		return ErrMetricsOverflow
	}
	if strings.Contains(strings.ToLower(event.Operation), "fallback") || strings.Contains(strings.ToLower(event.ErrorCode), "fallback") {
		if !checkedAdd(&metrics.Fallbacks, 1) {
			return ErrMetricsOverflow
		}
	}
	if event.Cost != nil && !checkedMapAdd(metrics.CostMicros, event.Cost.Currency, event.Cost.Micros) {
		return ErrMetricsOverflow
	}
	return nil
}

func newMetrics() Metrics {
	return Metrics{StatusCounts: map[string]uint64{}, FailureCounts: map[string]uint64{}, CostMicros: map[string]uint64{}}
}

func checkedAdd(target *uint64, value uint64) bool {
	if value > math.MaxUint64-*target {
		return false
	}
	*target += value
	return true
}

func checkedMapAdd(target map[string]uint64, key string, value uint64) bool {
	current := target[key]
	if !checkedAdd(&current, value) {
		return false
	}
	target[key] = current
	return true
}

func eventKey(context agent.ExternalAgentHookContext, event agent.TraceEvent) string {
	return strings.Join([]string{context.HostSessionID, event.RunID, event.SessionID, fmt.Sprintf("%020d", event.Sequence)}, "\x00")
}

func sessionLess(left, right Session) bool {
	for _, pair := range [][2]string{{left.Context.HostSessionID, right.Context.HostSessionID}, {left.Context.Host, right.Context.Host},
		{left.Context.Model, right.Context.Model}, {left.Context.SkillVersion, right.Context.SkillVersion},
		{left.Context.ProtocolVersion, right.Context.ProtocolVersion}, {left.RunID, right.RunID}, {left.SessionID, right.SessionID}} {
		if pair[0] != pair[1] {
			return pair[0] < pair[1]
		}
	}
	return false
}

func (report Report) Validate() error {
	if report.Version != Version || report.EnvelopeCount < 1 && report.EventCount > 0 || report.Sessions == nil {
		return ErrInvalidReport
	}
	var events uint64
	for index, session := range report.Sessions {
		if session.RunID == "" || session.SessionID == "" || index > 0 && sessionLess(session, report.Sessions[index-1]) {
			return ErrInvalidReport
		}
		if (agent.ExternalAgentHookEnvelope{Version: agent.ExternalAgentHookVersion, Context: session.Context}).Validate() != nil || !validMetrics(session.Metrics) {
			return ErrInvalidReport
		}
		if len(session.Turns) == 0 {
			return ErrInvalidReport
		}
		var sessionEvents uint64
		for turnIndex, turn := range session.Turns {
			if turn.Turn == 0 || turnIndex > 0 && turn.Turn <= session.Turns[turnIndex-1].Turn || !validMetrics(turn.Metrics) {
				return ErrInvalidReport
			}
			if !checkedAdd(&sessionEvents, turn.Metrics.Events) {
				return ErrInvalidReport
			}
		}
		if sessionEvents != session.Metrics.Events || !checkedAdd(&events, session.Metrics.Events) {
			return ErrInvalidReport
		}
	}
	if events != report.EventCount {
		return ErrInvalidReport
	}
	return nil
}

func validMetrics(metrics Metrics) bool {
	if metrics.StatusCounts == nil || metrics.FailureCounts == nil || metrics.CostMicros == nil {
		return false
	}
	kinds := metrics.ProviderCalls
	if !checkedAdd(&kinds, metrics.MSQLCalls) || !checkedAdd(&kinds, metrics.ToolCalls) || !checkedAdd(&kinds, metrics.BootstrapCalls) {
		return false
	}
	return metrics.Events == kinds
}

func EncodeJSON(report Report) ([]byte, error) {
	if err := report.Validate(); err != nil {
		return nil, err
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func DecodeJSON(encoded []byte) (Report, error) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var report Report
	if err := decoder.Decode(&report); err != nil {
		return Report{}, fmt.Errorf("%w: %v", ErrInvalidReport, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Report{}, ErrInvalidReport
	}
	if err := report.Validate(); err != nil {
		return Report{}, err
	}
	return report, nil
}

// DecodeHookJSON strictly decodes one F204 Hook snapshot. Keeping this
// boundary here makes report builders unable to accidentally accept an
// incompatible or concatenated artifact.
func DecodeHookJSON(encoded []byte) (agent.ExternalAgentHookEnvelope, error) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var envelope agent.ExternalAgentHookEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return agent.ExternalAgentHookEnvelope{}, fmt.Errorf("%w: %v", ErrInvalidReport, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return agent.ExternalAgentHookEnvelope{}, ErrInvalidReport
	}
	if err := envelope.Validate(); err != nil {
		return agent.ExternalAgentHookEnvelope{}, fmt.Errorf("%w: hook envelope: %v", ErrInvalidReport, err)
	}
	return envelope, nil
}

var htmlTemplate = template.Must(template.New("agent-metrics").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Memora Agent Metrics</title>
<style>body{font:14px system-ui,sans-serif;margin:2rem}table{border-collapse:collapse}th,td{border:1px solid #ccc;padding:.35rem .6rem;text-align:left}code{white-space:pre-wrap}</style>
</head><body><h1>Memora Agent Metrics</h1><p>Events: {{.EventCount}} · Envelopes: {{.EnvelopeCount}}</p>
{{range .Sessions}}<h2>{{.Context.Host}} / {{.Context.Model}} / {{.RunID}} / {{.SessionID}}</h2>
<p>Host session: <code>{{.Context.HostSessionID}}</code> · Events: {{.Metrics.Events}} · Duration μs: {{.Metrics.DurationMicros}} · Input bytes: {{.Metrics.InputBytes}} · Output bytes: {{.Metrics.OutputBytes}}</p>
<table><thead><tr><th>Turn</th><th>Events</th><th>Provider</th><th>MSQL</th><th>Tool</th><th>Duration μs</th><th>Failures</th><th>Fallbacks</th></tr></thead><tbody>
{{range .Turns}}<tr><td>{{.Turn}}</td><td>{{.Metrics.Events}}</td><td>{{.Metrics.ProviderCalls}}</td><td>{{.Metrics.MSQLCalls}}</td><td>{{.Metrics.ToolCalls}}</td><td>{{.Metrics.DurationMicros}}</td><td>{{.Metrics.FailureCounts}}</td><td>{{.Metrics.Fallbacks}}</td></tr>{{end}}
</tbody></table>{{end}}</body></html>`))

func RenderHTML(report Report) ([]byte, error) {
	if err := report.Validate(); err != nil {
		return nil, err
	}
	var output bytes.Buffer
	if err := htmlTemplate.Execute(&output, report); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}
