package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"

	protocolmsql "github.com/HW-Yue/Memora/protocol/msql"
)

const (
	QuerySessionEventVersion = "memora.query-session-event/v1"
	// Existing hard limits produce at most 58 lifecycle and Trace events:
	// 2 lifecycle + 9 Bootstrap MSQL + 1 Bootstrap + 16 Provider + 15 Tool MSQL + 15 Tool.
	querySessionEventBuffer = 64
)

var (
	ErrInvalidQuerySession        = errors.New("Query Session is invalid")
	ErrInvalidQuerySessionRequest = errors.New("Query Session request is invalid")
	ErrQuerySessionBusy           = errors.New("Query Session already has an active turn")
	ErrQuerySessionBudget         = errors.New("Query Session budget is exhausted")
)

type QuerySessionEventKind string

const (
	QuerySessionEventTurnStarted   QuerySessionEventKind = "turn_started"
	QuerySessionEventTrace         QuerySessionEventKind = "trace"
	QuerySessionEventTurnCompleted QuerySessionEventKind = "turn_completed"
	QuerySessionEventTurnFailed    QuerySessionEventKind = "turn_failed"
	QuerySessionEventTurnCancelled QuerySessionEventKind = "turn_cancelled"
)

type QuerySessionEvent struct {
	Version   string                `json:"version"`
	SessionID string                `json:"session_id"`
	RunID     string                `json:"run_id"`
	Turn      uint64                `json:"turn"`
	Sequence  uint64                `json:"sequence"`
	Kind      QuerySessionEventKind `json:"kind"`
	Trace     *TraceEvent           `json:"trace,omitempty"`
	ErrorCode string                `json:"error_code,omitempty"`
}

func (event QuerySessionEvent) Validate() error {
	if event.Version != QuerySessionEventVersion ||
		!validTraceIdentity(TraceIdentity{RunID: event.RunID, SessionID: event.SessionID}) ||
		event.Turn == 0 || event.Sequence == 0 {
		return ErrInvalidQuerySessionRequest
	}
	switch event.Kind {
	case QuerySessionEventTurnStarted, QuerySessionEventTurnCompleted:
		if event.Trace != nil || event.ErrorCode != "" {
			return ErrInvalidQuerySessionRequest
		}
	case QuerySessionEventTrace:
		if event.Trace == nil || event.ErrorCode != "" || event.Trace.Validate() != nil ||
			event.Trace.RunID != event.RunID || event.Trace.SessionID != event.SessionID {
			return ErrInvalidQuerySessionRequest
		}
	case QuerySessionEventTurnFailed, QuerySessionEventTurnCancelled:
		if event.Trace != nil || !validTraceLabel(event.ErrorCode, 80) {
			return ErrInvalidQuerySessionRequest
		}
	default:
		return ErrInvalidQuerySessionRequest
	}
	return nil
}

type QuerySessionBudget struct {
	MaxTurns         uint64 `json:"max_turns"`
	MaxProviderCalls uint64 `json:"max_provider_calls"`
	MaxToolCalls     uint64 `json:"max_tool_calls"`
}

func DefaultQuerySessionBudget() QuerySessionBudget {
	return QuerySessionBudget{MaxTurns: 8, MaxProviderCalls: 32, MaxToolCalls: 24}
}

type QuerySessionUsage struct {
	TurnsStarted   uint64 `json:"turns_started"`
	TurnsCompleted uint64 `json:"turns_completed"`
	TurnsFailed    uint64 `json:"turns_failed"`
	TurnsCancelled uint64 `json:"turns_cancelled"`
	ProviderCalls  uint64 `json:"provider_calls"`
	ToolCalls      uint64 `json:"tool_calls"`
}

type QuerySessionConfig struct {
	SessionID        string
	Model            string
	Authorization    protocolmsql.Authorization
	BootstrapBudget  BootstrapBudget
	BootstrapProfile BootstrapProfile
	QueryBudget      QueryBudget
	Budget           QuerySessionBudget
}

type QuerySession struct {
	mu     sync.Mutex
	agent  *QueryAgent
	config QuerySessionConfig
	usage  QuerySessionUsage
	active bool
}

func NewQuerySession(agent *QueryAgent, config QuerySessionConfig) (*QuerySession, error) {
	if agent == nil || !validTraceText(config.SessionID, 128) ||
		!validQuerySessionBudget(config.Budget) {
		return nil, ErrInvalidQuerySession
	}
	probe := QueryRequest{
		Identity: TraceIdentity{RunID: config.SessionID + ":turn:1", SessionID: config.SessionID},
		Question: "query-session-construction-probe", Model: config.Model,
		Authorization: config.Authorization, BootstrapBudget: config.BootstrapBudget,
		BootstrapProfile: config.BootstrapProfile, Budget: config.QueryBudget,
	}
	if err := validateQueryRequest(probe); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidQuerySession, err)
	}
	if err := validateBootstrapRequest(context.Background(), BootstrapRequest{
		Query: probe.Question, Authorization: queryReadOnlyAuthorization(config.Authorization),
		Budget: config.BootstrapBudget, Profile: config.BootstrapProfile,
	}); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidQuerySession, err)
	}
	return &QuerySession{agent: agent, config: cloneQuerySessionConfig(config)}, nil
}

func (session *QuerySession) Start(ctx context.Context, question string) (*QueryTurn, error) {
	if session == nil || ctx == nil {
		return nil, ErrInvalidQuerySessionRequest
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	session.mu.Lock()
	defer session.mu.Unlock()
	if session.active {
		return nil, ErrQuerySessionBusy
	}
	turnNumber := session.usage.TurnsStarted + 1
	budget, ok := session.remainingTurnBudgetLocked()
	if !ok {
		return nil, ErrQuerySessionBudget
	}
	runID := fmt.Sprintf("%s:turn:%d", session.config.SessionID, turnNumber)
	request := QueryRequest{
		Identity: TraceIdentity{RunID: runID, SessionID: session.config.SessionID},
		Question: question, Model: session.config.Model, Authorization: session.config.Authorization,
		BootstrapBudget:  session.config.BootstrapBudget,
		BootstrapProfile: session.config.BootstrapProfile, Budget: budget,
	}
	if err := validateQueryRequest(request); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidQuerySessionRequest, err)
	}
	turnContext, cancel := context.WithCancel(ctx)
	turn := &QueryTurn{
		events: make(chan QuerySessionEvent, querySessionEventBuffer), done: make(chan struct{}),
		cancel: cancel, sessionID: session.config.SessionID, runID: runID, number: turnNumber,
	}
	turn.emit(QuerySessionEventTurnStarted, nil, "")
	session.active = true
	session.usage.TurnsStarted++
	go session.runTurn(turnContext, turn, request)
	return turn, nil
}

func (session *QuerySession) Usage() QuerySessionUsage {
	if session == nil {
		return QuerySessionUsage{}
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.usage
}

func (session *QuerySession) remainingTurnBudgetLocked() (QueryBudget, bool) {
	if session.usage.TurnsStarted >= session.config.Budget.MaxTurns ||
		session.usage.ProviderCalls >= session.config.Budget.MaxProviderCalls ||
		session.usage.ToolCalls >= session.config.Budget.MaxToolCalls {
		return QueryBudget{}, false
	}
	providerRemaining := session.config.Budget.MaxProviderCalls - session.usage.ProviderCalls
	toolRemaining := session.config.Budget.MaxToolCalls - session.usage.ToolCalls
	if providerRemaining < 2 || toolRemaining < 1 {
		return QueryBudget{}, false
	}
	budget := session.config.QueryBudget
	budget.MaxProviderCalls = minQuerySessionBudget(budget.MaxProviderCalls, providerRemaining)
	budget.MaxToolCalls = minQuerySessionBudget(budget.MaxToolCalls, toolRemaining)
	if budget.MaxToolCalls >= budget.MaxProviderCalls {
		budget.MaxToolCalls = budget.MaxProviderCalls - 1
	}
	return budget, validQueryBudget(budget)
}

func (session *QuerySession) runTurn(ctx context.Context, turn *QueryTurn, request QueryRequest) {
	result, err := session.agent.query(ctx, request, func(trace TraceEvent) {
		turn.emit(QuerySessionEventTrace, &trace, "")
	})
	kind, code := querySessionTerminal(err)

	session.mu.Lock()
	session.usage.ProviderCalls += result.Trace.Summary.KindCounts[TraceKindProvider]
	session.usage.ToolCalls += result.Trace.Summary.KindCounts[TraceKindTool]
	switch kind {
	case QuerySessionEventTurnCompleted:
		session.usage.TurnsCompleted++
	case QuerySessionEventTurnCancelled:
		session.usage.TurnsCancelled++
	default:
		session.usage.TurnsFailed++
	}
	session.active = false
	session.mu.Unlock()

	turn.emit(kind, nil, code)
	close(turn.events)
	turn.finish(result, err)
	turn.cancel()
}

func querySessionTerminal(err error) (QuerySessionEventKind, string) {
	switch {
	case err == nil:
		return QuerySessionEventTurnCompleted, ""
	case errors.Is(err, context.Canceled):
		return QuerySessionEventTurnCancelled, "CONTEXT_CANCELLED"
	case errors.Is(err, context.DeadlineExceeded):
		return QuerySessionEventTurnCancelled, "CONTEXT_DEADLINE"
	case errors.Is(err, ErrQueryBudget):
		return QuerySessionEventTurnFailed, "QUERY_BUDGET"
	case errors.Is(err, ErrInvalidQueryToolCall):
		return QuerySessionEventTurnFailed, "QUERY_TOOL_CALL_INVALID"
	default:
		return QuerySessionEventTurnFailed, "QUERY_FAILED"
	}
}

func validQuerySessionBudget(budget QuerySessionBudget) bool {
	return budget.MaxTurns >= 1 && budget.MaxTurns <= 64 &&
		budget.MaxProviderCalls >= 2 && budget.MaxProviderCalls <= 1024 &&
		budget.MaxToolCalls >= 1 && budget.MaxToolCalls < budget.MaxProviderCalls
}

func minQuerySessionBudget(left, right uint64) uint64 {
	if left < right {
		return left
	}
	return right
}

func cloneQuerySessionConfig(config QuerySessionConfig) QuerySessionConfig {
	config.Authorization.AuthorizedDatabases = append([]string(nil), config.Authorization.AuthorizedDatabases...)
	if config.Authorization.DatabaseLevels != nil {
		levels := make(map[string]protocolmsql.RiskLevel, len(config.Authorization.DatabaseLevels))
		for database, level := range config.Authorization.DatabaseLevels {
			levels[database] = level
		}
		config.Authorization.DatabaseLevels = levels
	}
	if config.Authorization.Approval != nil {
		approval := *config.Authorization.Approval
		config.Authorization.Approval = &approval
	}
	return config
}

type queryTurnOutcome struct {
	result QueryResult
	err    error
}

type QueryTurn struct {
	mu        sync.RWMutex
	events    chan QuerySessionEvent
	done      chan struct{}
	cancel    context.CancelFunc
	sessionID string
	runID     string
	number    uint64
	sequence  uint64
	outcome   queryTurnOutcome
}

func (turn *QueryTurn) Events() <-chan QuerySessionEvent {
	if turn == nil {
		return nil
	}
	return turn.events
}

func (turn *QueryTurn) Cancel() {
	if turn != nil && turn.cancel != nil {
		turn.cancel()
	}
}

func (turn *QueryTurn) Wait() (QueryResult, error) {
	if turn == nil {
		return QueryResult{}, ErrInvalidQuerySessionRequest
	}
	<-turn.done
	turn.mu.RLock()
	defer turn.mu.RUnlock()
	return cloneQueryResult(turn.outcome.result), turn.outcome.err
}

func (turn *QueryTurn) emit(kind QuerySessionEventKind, trace *TraceEvent, errorCode string) {
	turn.sequence++
	event := QuerySessionEvent{
		Version: QuerySessionEventVersion, SessionID: turn.sessionID, RunID: turn.runID,
		Turn: turn.number, Sequence: turn.sequence, Kind: kind, ErrorCode: errorCode,
	}
	if trace != nil {
		cloned := cloneTraceEvent(*trace)
		event.Trace = &cloned
	}
	turn.events <- event
}

func (turn *QueryTurn) finish(result QueryResult, err error) {
	turn.mu.Lock()
	turn.outcome = queryTurnOutcome{result: cloneQueryResult(result), err: err}
	turn.mu.Unlock()
	close(turn.done)
}

func cloneQueryResult(result QueryResult) QueryResult {
	cloned := result
	cloned.Evidence = make([]SelectEvidence, len(result.Evidence))
	for index, evidence := range result.Evidence {
		cloned.Evidence[index] = SelectEvidence{
			RequestID: evidence.RequestID, Result: cloneQueryStatement(evidence.Result),
		}
	}
	cloned.Trace.Events = cloneTraceEvents(result.Trace.Events)
	cloned.Trace.Summary = cloneTraceSummary(result.Trace.Summary)
	return cloned
}
