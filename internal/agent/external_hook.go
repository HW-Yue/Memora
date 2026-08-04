package agent

import (
	"errors"
	"sort"
	"sync"
)

const ExternalAgentHookVersion = "memora.external-agent-hook/v1"

var (
	ErrInvalidExternalAgentHook = errors.New("External Agent Hook is invalid")
	ErrExternalAgentHookBudget  = errors.New("External Agent Hook budget is exhausted")
)

type ExternalAgentHookContext struct {
	HostSessionID   string `json:"host_session_id"`
	Host            string `json:"host"`
	Model           string `json:"model"`
	SkillVersion    string `json:"skill_version"`
	ProtocolVersion string `json:"protocol_version"`
}

type ExternalAgentHookEvent struct {
	Version string                   `json:"version"`
	Context ExternalAgentHookContext `json:"context"`
	Trace   TraceEvent               `json:"trace"`
}

type ExternalAgentHookEnvelope struct {
	Version string                   `json:"version"`
	Context ExternalAgentHookContext `json:"context"`
	Events  []ExternalAgentHookEvent `json:"events"`
	Dropped uint64                   `json:"dropped"`
}

type ExternalAgentHook struct {
	mu      sync.RWMutex
	context ExternalAgentHookContext
	max     uint64
	events  []ExternalAgentHookEvent
	dropped uint64
}

func NewExternalAgentHook(context ExternalAgentHookContext, maxEvents uint64) (*ExternalAgentHook, error) {
	if context.HostSessionID == "" {
		context.HostSessionID = "unknown"
	}
	if context.Host == "" {
		context.Host = "unknown"
	}
	if context.Model == "" {
		context.Model = "unknown"
	}
	if context.SkillVersion == "" {
		context.SkillVersion = "unknown"
	}
	if context.ProtocolVersion == "" {
		context.ProtocolVersion = "unknown"
	}
	if maxEvents == 0 || maxEvents > 100_000 || !validExternalAgentHookContext(context) {
		return nil, ErrInvalidExternalAgentHook
	}
	return &ExternalAgentHook{context: context, max: maxEvents, events: make([]ExternalAgentHookEvent, 0, minHookCapacity(maxEvents))}, nil
}

func minHookCapacity(max uint64) int {
	if max > 1024 {
		return 1024
	}
	return int(max)
}

func (hook *ExternalAgentHook) Observe(trace TraceEvent) {
	if hook == nil || trace.Validate() != nil {
		return
	}
	hook.mu.Lock()
	defer hook.mu.Unlock()
	if uint64(len(hook.events)) >= hook.max {
		hook.dropped++
		return
	}
	hook.events = append(hook.events, ExternalAgentHookEvent{
		Version: ExternalAgentHookVersion, Context: hook.context, Trace: cloneTraceEvent(trace),
	})
}

func (hook *ExternalAgentHook) Snapshot() ExternalAgentHookEnvelope {
	if hook == nil {
		return ExternalAgentHookEnvelope{}
	}
	hook.mu.RLock()
	defer hook.mu.RUnlock()
	events := append([]ExternalAgentHookEvent(nil), hook.events...)
	sort.SliceStable(events, func(left, right int) bool {
		lhs, rhs := events[left].Trace, events[right].Trace
		if lhs.RunID != rhs.RunID {
			return lhs.RunID < rhs.RunID
		}
		if lhs.Sequence != rhs.Sequence {
			return lhs.Sequence < rhs.Sequence
		}
		return lhs.Operation < rhs.Operation
	})
	return ExternalAgentHookEnvelope{Version: ExternalAgentHookVersion, Context: hook.context,
		Events: events, Dropped: hook.dropped}
}

func (envelope ExternalAgentHookEnvelope) Validate() error {
	if envelope.Version != ExternalAgentHookVersion || !validExternalAgentHookContext(envelope.Context) {
		return ErrInvalidExternalAgentHook
	}
	for _, event := range envelope.Events {
		if event.Version != ExternalAgentHookVersion || event.Context != envelope.Context || event.Trace.Validate() != nil {
			return ErrInvalidExternalAgentHook
		}
	}
	return nil
}

func validExternalAgentHookContext(context ExternalAgentHookContext) bool {
	return validTraceText(context.HostSessionID, 160) && validTraceText(context.Host, 160) &&
		validTraceText(context.Model, 200) && validTraceText(context.SkillVersion, 160) &&
		validTraceText(context.ProtocolVersion, 160)
}
