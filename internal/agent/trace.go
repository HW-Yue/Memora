package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"reflect"
	"strings"
	"sync"
	"time"
)

const (
	TraceEventVersion    = "memora.agent-trace-event/v1"
	TraceEnvelopeVersion = "memora.agent-trace/v1"
)

var (
	ErrInvalidTraceIdentity = errors.New("Agent Trace identity is invalid")
	ErrInvalidTraceEvent    = errors.New("Agent Trace event is invalid")
	ErrInvalidTraceEnvelope = errors.New("Agent Trace envelope is invalid")
)

type TraceKind string

const (
	TraceKindBootstrap TraceKind = "bootstrap"
	TraceKindProvider  TraceKind = "provider"
	TraceKindMSQL      TraceKind = "msql"
	TraceKindTool      TraceKind = "tool"
)

type TraceStatus string

const (
	TraceStatusSucceeded TraceStatus = "succeeded"
	TraceStatusFailed    TraceStatus = "failed"
	TraceStatusCancelled TraceStatus = "cancelled"
)

type TraceIdentity struct {
	RunID     string
	SessionID string
}

type TraceDigest struct {
	Bytes  uint64 `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type TraceCost struct {
	Currency        string `json:"currency"`
	Micros          uint64 `json:"micros"`
	PriceSnapshotID string `json:"price_snapshot_id"`
}

// TraceDraft contains only caller-computed metadata. The recorder deliberately
// has no clock, pricing, or raw request/response input.
type TraceDraft struct {
	Turn       uint64
	Kind       TraceKind
	Operation  string
	StartedAt  time.Time
	FinishedAt time.Time
	Status     TraceStatus
	ErrorCode  string
	Input      TraceDigest
	Output     TraceDigest
	Model      string
	Usage      *ProviderUsage
	Cost       *TraceCost
}

type TraceEvent struct {
	Version        string         `json:"version"`
	RunID          string         `json:"run_id"`
	SessionID      string         `json:"session_id"`
	Sequence       uint64         `json:"sequence"`
	Turn           uint64         `json:"turn"`
	Kind           TraceKind      `json:"kind"`
	Operation      string         `json:"operation"`
	StartedAt      time.Time      `json:"started_at"`
	FinishedAt     time.Time      `json:"finished_at"`
	DurationMicros uint64         `json:"duration_micros"`
	Status         TraceStatus    `json:"status"`
	ErrorCode      string         `json:"error_code,omitempty"`
	Input          TraceDigest    `json:"input"`
	Output         TraceDigest    `json:"output"`
	Model          string         `json:"model,omitempty"`
	Usage          *ProviderUsage `json:"usage,omitempty"`
	Cost           *TraceCost     `json:"cost,omitempty"`
}

type TraceSummary struct {
	Events               uint64                 `json:"events"`
	StatusCounts         map[TraceStatus]uint64 `json:"status_counts"`
	KindCounts           map[TraceKind]uint64   `json:"kind_counts"`
	DurationMicros       map[TraceKind]uint64   `json:"duration_micros"`
	InputTokens          uint64                 `json:"input_tokens"`
	CachedInputTokens    uint64                 `json:"cached_input_tokens"`
	OutputTokens         uint64                 `json:"output_tokens"`
	TotalTokens          uint64                 `json:"total_tokens"`
	CostMicrosByCurrency map[string]uint64      `json:"cost_micros_by_currency"`
}

type TraceEnvelope struct {
	Version   string       `json:"version"`
	RunID     string       `json:"run_id"`
	SessionID string       `json:"session_id"`
	Events    []TraceEvent `json:"events"`
	Summary   TraceSummary `json:"summary"`
}

type TraceRecorder struct {
	mu       sync.RWMutex
	identity TraceIdentity
	events   []TraceEvent
	summary  TraceSummary
	observe  func(TraceEvent)
}

func NewTraceRecorder(identity TraceIdentity) (*TraceRecorder, error) {
	return newTraceRecorder(identity, nil)
}

func newTraceRecorder(identity TraceIdentity, observe func(TraceEvent)) (*TraceRecorder, error) {
	if !validTraceIdentity(identity) {
		return nil, ErrInvalidTraceIdentity
	}
	return &TraceRecorder{identity: identity, summary: newTraceSummary(), observe: observe}, nil
}

// DigestTracePayload turns an opaque payload into the only body evidence the
// Trace contract accepts.
func DigestTracePayload(payload []byte) TraceDigest {
	digest := sha256.Sum256(payload)
	return TraceDigest{Bytes: uint64(len(payload)), SHA256: "sha256:" + hex.EncodeToString(digest[:])}
}

func (recorder *TraceRecorder) Append(draft TraceDraft) (TraceEvent, error) {
	if recorder == nil {
		return TraceEvent{}, ErrInvalidTraceIdentity
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if recorder.identity.RunID == "" {
		return TraceEvent{}, ErrInvalidTraceEvent
	}
	event := sealTraceEvent(recorder.identity, uint64(len(recorder.events))+1, draft)
	if err := event.Validate(); err != nil {
		return TraceEvent{}, err
	}
	nextSummary := cloneTraceSummary(recorder.summary)
	if !addTraceEventToSummary(&nextSummary, event) {
		return TraceEvent{}, ErrInvalidTraceEvent
	}
	recorder.events = append(recorder.events, cloneTraceEvent(event))
	recorder.summary = nextSummary
	sealed := cloneTraceEvent(event)
	if recorder.observe != nil {
		recorder.observe(cloneTraceEvent(sealed))
	}
	return sealed, nil
}

func (recorder *TraceRecorder) Snapshot() TraceEnvelope {
	if recorder == nil {
		return TraceEnvelope{}
	}
	recorder.mu.RLock()
	defer recorder.mu.RUnlock()
	return TraceEnvelope{
		Version: TraceEnvelopeVersion, RunID: recorder.identity.RunID, SessionID: recorder.identity.SessionID,
		Events: cloneTraceEvents(recorder.events), Summary: cloneTraceSummary(recorder.summary),
	}
}

func (event TraceEvent) Validate() error {
	if event.Version != TraceEventVersion || !validTraceIdentity(TraceIdentity{RunID: event.RunID, SessionID: event.SessionID}) ||
		event.Sequence == 0 || event.Turn == 0 || !validTraceKind(event.Kind) || !validTraceLabel(event.Operation, 80) ||
		!validTraceTimes(event.StartedAt, event.FinishedAt, event.DurationMicros) || !validTraceStatus(event.Status) ||
		!validTraceDigest(event.Input) || !validTraceDigest(event.Output) || !validTraceError(event.Status, event.ErrorCode) {
		return ErrInvalidTraceEvent
	}
	if event.Kind == TraceKindProvider {
		if invalidProviderText(event.Model, 200, true) || event.Usage == nil || !validProviderUsage(*event.Usage) ||
			(event.Cost != nil && !validTraceCost(*event.Cost)) {
			return ErrInvalidTraceEvent
		}
		return nil
	}
	if event.Model != "" || event.Usage != nil || event.Cost != nil {
		return ErrInvalidTraceEvent
	}
	return nil
}

func (envelope TraceEnvelope) Validate() error {
	if envelope.Version != TraceEnvelopeVersion ||
		!validTraceIdentity(TraceIdentity{RunID: envelope.RunID, SessionID: envelope.SessionID}) {
		return ErrInvalidTraceEnvelope
	}
	for index, event := range envelope.Events {
		if event.Validate() != nil || event.RunID != envelope.RunID || event.SessionID != envelope.SessionID ||
			event.Sequence != uint64(index)+1 {
			return ErrInvalidTraceEnvelope
		}
	}
	summary, err := summarizeTraceEvents(envelope.Events)
	if err != nil || !reflect.DeepEqual(summary, envelope.Summary) {
		return ErrInvalidTraceEnvelope
	}
	return nil
}

func sealTraceEvent(identity TraceIdentity, sequence uint64, draft TraceDraft) TraceEvent {
	duration := uint64(0)
	if !draft.StartedAt.IsZero() && !draft.FinishedAt.Before(draft.StartedAt) {
		duration = uint64(draft.FinishedAt.Sub(draft.StartedAt) / time.Microsecond)
	}
	return TraceEvent{
		Version: TraceEventVersion, RunID: identity.RunID, SessionID: identity.SessionID,
		Sequence: sequence, Turn: draft.Turn, Kind: draft.Kind, Operation: draft.Operation,
		StartedAt: draft.StartedAt, FinishedAt: draft.FinishedAt, DurationMicros: duration,
		Status: draft.Status, ErrorCode: draft.ErrorCode, Input: draft.Input, Output: draft.Output,
		Model: draft.Model, Usage: cloneProviderUsage(draft.Usage), Cost: cloneTraceCost(draft.Cost),
	}
}

func summarizeTraceEvents(events []TraceEvent) (TraceSummary, error) {
	summary := newTraceSummary()
	for _, event := range events {
		if !addTraceEventToSummary(&summary, event) {
			return TraceSummary{}, ErrInvalidTraceEnvelope
		}
	}
	return summary, nil
}

func addTraceEventToSummary(summary *TraceSummary, event TraceEvent) bool {
	var ok bool
	if summary.Events, ok = checkedTraceAdd(summary.Events, 1); !ok {
		return false
	}
	if summary.StatusCounts[event.Status], ok = checkedTraceAdd(summary.StatusCounts[event.Status], 1); !ok {
		return false
	}
	if summary.KindCounts[event.Kind], ok = checkedTraceAdd(summary.KindCounts[event.Kind], 1); !ok {
		return false
	}
	if summary.DurationMicros[event.Kind], ok = checkedTraceAdd(summary.DurationMicros[event.Kind], event.DurationMicros); !ok {
		return false
	}
	if event.Usage != nil {
		if summary.InputTokens, ok = checkedTraceAdd(summary.InputTokens, event.Usage.InputTokens); !ok {
			return false
		}
		if summary.CachedInputTokens, ok = checkedTraceAdd(summary.CachedInputTokens, event.Usage.CachedInputTokens); !ok {
			return false
		}
		if summary.OutputTokens, ok = checkedTraceAdd(summary.OutputTokens, event.Usage.OutputTokens); !ok {
			return false
		}
		if summary.TotalTokens, ok = checkedTraceAdd(summary.TotalTokens, event.Usage.TotalTokens); !ok {
			return false
		}
	}
	if event.Cost != nil {
		currency := event.Cost.Currency
		if summary.CostMicrosByCurrency[currency], ok = checkedTraceAdd(summary.CostMicrosByCurrency[currency], event.Cost.Micros); !ok {
			return false
		}
	}
	return true
}

func newTraceSummary() TraceSummary {
	return TraceSummary{
		StatusCounts: make(map[TraceStatus]uint64), KindCounts: make(map[TraceKind]uint64),
		DurationMicros: make(map[TraceKind]uint64), CostMicrosByCurrency: make(map[string]uint64),
	}
}

func cloneTraceSummary(value TraceSummary) TraceSummary {
	cloned := value
	cloned.StatusCounts = make(map[TraceStatus]uint64, len(value.StatusCounts))
	for key, count := range value.StatusCounts {
		cloned.StatusCounts[key] = count
	}
	cloned.KindCounts = make(map[TraceKind]uint64, len(value.KindCounts))
	for key, count := range value.KindCounts {
		cloned.KindCounts[key] = count
	}
	cloned.DurationMicros = make(map[TraceKind]uint64, len(value.DurationMicros))
	for key, duration := range value.DurationMicros {
		cloned.DurationMicros[key] = duration
	}
	cloned.CostMicrosByCurrency = make(map[string]uint64, len(value.CostMicrosByCurrency))
	for currency, cost := range value.CostMicrosByCurrency {
		cloned.CostMicrosByCurrency[currency] = cost
	}
	return cloned
}

func cloneTraceEvents(events []TraceEvent) []TraceEvent {
	cloned := make([]TraceEvent, len(events))
	for index, event := range events {
		cloned[index] = cloneTraceEvent(event)
	}
	return cloned
}

func cloneTraceEvent(event TraceEvent) TraceEvent {
	event.Usage = cloneProviderUsage(event.Usage)
	event.Cost = cloneTraceCost(event.Cost)
	return event
}

func cloneProviderUsage(usage *ProviderUsage) *ProviderUsage {
	if usage == nil {
		return nil
	}
	cloned := *usage
	return &cloned
}

func cloneTraceCost(cost *TraceCost) *TraceCost {
	if cost == nil {
		return nil
	}
	cloned := *cost
	return &cloned
}

func validTraceIdentity(identity TraceIdentity) bool {
	return validTraceText(identity.RunID, 160) && validTraceText(identity.SessionID, 160)
}

func validTraceText(value string, limit int) bool {
	return strings.TrimSpace(value) != "" && len(value) <= limit && !strings.ContainsRune(value, '\x00')
}

func validTraceLabel(value string, limit int) bool {
	if len(value) == 0 || len(value) > limit {
		return false
	}
	for _, character := range []byte(value) {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || strings.ContainsRune("_.:-", rune(character)) {
			continue
		}
		return false
	}
	return true
}

func validTraceKind(kind TraceKind) bool {
	return kind == TraceKindBootstrap || kind == TraceKindProvider || kind == TraceKindMSQL || kind == TraceKindTool
}

func validTraceStatus(status TraceStatus) bool {
	return status == TraceStatusSucceeded || status == TraceStatusFailed || status == TraceStatusCancelled
}

func validTraceTimes(started, finished time.Time, durationMicros uint64) bool {
	if started.IsZero() || finished.IsZero() || started.Location() != time.UTC || finished.Location() != time.UTC ||
		finished.Before(started) {
		return false
	}
	return uint64(finished.Sub(started)/time.Microsecond) == durationMicros
}

func validTraceDigest(digest TraceDigest) bool {
	const prefix = "sha256:"
	if !strings.HasPrefix(digest.SHA256, prefix) || len(digest.SHA256) != len(prefix)+sha256.Size*2 {
		return false
	}
	hexValue := digest.SHA256[len(prefix):]
	if strings.ToLower(hexValue) != hexValue {
		return false
	}
	decoded, err := hex.DecodeString(hexValue)
	return err == nil && len(decoded) == sha256.Size
}

func validTraceError(status TraceStatus, code string) bool {
	if status == TraceStatusSucceeded {
		return code == ""
	}
	return validTraceLabel(code, 80)
}

func validProviderUsage(usage ProviderUsage) bool {
	return usage.CachedInputTokens <= usage.InputTokens && usage.InputTokens <= math.MaxUint64-usage.OutputTokens &&
		usage.TotalTokens == usage.InputTokens+usage.OutputTokens
}

func validTraceCost(cost TraceCost) bool {
	if len(cost.Currency) != 3 || !validTraceLabel(cost.PriceSnapshotID, 80) {
		return false
	}
	for _, character := range []byte(cost.Currency) {
		if character < 'A' || character > 'Z' {
			return false
		}
	}
	return true
}

func checkedTraceAdd(left, right uint64) (uint64, bool) {
	if left > math.MaxUint64-right {
		return 0, false
	}
	return left + right, true
}
