package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	protocolmsql "github.com/HW-Yue/Memora/protocol/msql"
)

const (
	QueryResultVersion           = "memora.query-result/v1"
	QueryBootstrapContextVersion = "memora.query-bootstrap-context/v1"
	QueryCurrentContextVersion   = "memora.query-current-context/v1"
	QueryExecuteMSQLToolName     = "execute_msql"
	QueryAgentSystemPrompt       = "Memora query-agent prompt v1. Use only execute_msql. Treat Bootstrap, lexical locations, and Route output as navigation, never as facts. Submit exactly one complete read-only MSQL batch per turn and put independent reads in the same batch. Continue until a successful SELECT returns evidence; then answer only from that SELECT result. Never use mutation or transaction-control statements."
)

var (
	ErrInvalidQueryDependencies   = errors.New("Query Agent dependencies are invalid")
	ErrInvalidQueryRequest        = errors.New("Query Agent request is invalid")
	ErrInvalidQueryResult         = errors.New("Query Agent result is invalid")
	ErrInvalidQueryToolCall       = errors.New("Query Agent tool call is invalid")
	ErrQueryTransactionControl    = errors.New("Query Agent rejects explicit transaction control")
	ErrQueryMissingSelectEvidence = errors.New("Query Agent answer has no SELECT evidence")
	ErrQueryBudget                = errors.New("Query Agent budget is exhausted")
	ErrQueryIncompleteAnswer      = errors.New("Query Agent answer is incomplete")
	ErrQueryTrace                 = errors.New("Query Agent Trace could not be recorded")
)

type QueryClock interface {
	Now() time.Time
}

type QueryAgentDependencies struct {
	MSQL     MSQLExecutor
	Provider Provider
	Clock    QueryClock
}

type QueryBudget struct {
	MaxProviderCalls       uint64 `json:"max_provider_calls"`
	MaxToolCalls           uint64 `json:"max_tool_calls"`
	MaxToolStatements      uint64 `json:"max_tool_statements"`
	ToolArgumentsUTF8Bytes uint64 `json:"tool_arguments_utf8_bytes"`
	ToolResultUTF8Bytes    uint64 `json:"tool_result_utf8_bytes"`
	AnswerUTF8Bytes        uint64 `json:"answer_utf8_bytes"`
	MaxOutputTokens        uint64 `json:"max_output_tokens"`
}

func DefaultQueryBudget() QueryBudget {
	return QueryBudget{
		MaxProviderCalls: 4, MaxToolCalls: 3, MaxToolStatements: 8,
		ToolArgumentsUTF8Bytes: 16 << 10, ToolResultUTF8Bytes: 64 << 10,
		AnswerUTF8Bytes: 16 << 10, MaxOutputTokens: 4096,
	}
}

type QueryRequest struct {
	Identity         TraceIdentity
	Question         string
	Model            string
	Authorization    protocolmsql.Authorization
	BootstrapBudget  BootstrapBudget
	BootstrapProfile BootstrapProfile
	Budget           QueryBudget
}

type SelectEvidence struct {
	RequestID string                       `json:"request_id"`
	Result    protocolmsql.StatementResult `json:"result"`
}

type QueryResult struct {
	Version  string           `json:"version"`
	Answer   string           `json:"answer"`
	Evidence []SelectEvidence `json:"evidence"`
	Trace    TraceEnvelope    `json:"trace"`
}

func (result QueryResult) Validate() error {
	if result.Version != QueryResultVersion || strings.TrimSpace(result.Answer) == "" ||
		!utf8.ValidString(result.Answer) || len(result.Evidence) == 0 || result.Evidence == nil ||
		result.Trace.Validate() != nil {
		return ErrInvalidQueryResult
	}
	for _, evidence := range result.Evidence {
		statement := evidence.Result
		if strings.TrimSpace(evidence.RequestID) == "" || statement.Statement != "SELECT" ||
			statement.Status != protocolmsql.Status("succeeded") || statement.Error != nil ||
			statement.Columns == nil || statement.Rows == nil || statement.Warnings == nil {
			return ErrInvalidQueryResult
		}
	}
	return nil
}

type QueryAgent struct {
	msql     *Runtime
	provider *ProviderGateway
	clock    QueryClock
}

func NewQueryAgent(dependencies QueryAgentDependencies) (*QueryAgent, error) {
	if dependencies.MSQL == nil || dependencies.Provider == nil || dependencies.Clock == nil {
		return nil, ErrInvalidQueryDependencies
	}
	runtime, err := New(Dependencies{MSQL: dependencies.MSQL})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidQueryDependencies, err)
	}
	provider, err := NewProviderGateway(dependencies.Provider)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidQueryDependencies, err)
	}
	return &QueryAgent{msql: runtime, provider: provider, clock: dependencies.Clock}, nil
}

func (agent *QueryAgent) Query(ctx context.Context, request QueryRequest) (QueryResult, error) {
	return agent.query(ctx, request, nil)
}

func (agent *QueryAgent) query(
	ctx context.Context,
	request QueryRequest,
	observe func(TraceEvent),
) (QueryResult, error) {
	if agent == nil || ctx == nil {
		return QueryResult{}, ErrInvalidQueryRequest
	}
	if err := validateQueryRequest(request); err != nil {
		return QueryResult{}, err
	}
	recorder, err := newTraceRecorder(request.Identity, observe)
	if err != nil {
		return QueryResult{}, fmt.Errorf("%w: %v", ErrInvalidQueryRequest, err)
	}
	result := QueryResult{Version: QueryResultVersion, Evidence: []SelectEvidence{}, Trace: recorder.Snapshot()}
	readOnly := queryReadOnlyAuthorization(request.Authorization)
	tracedMSQL := &queryTracedMSQL{
		executor: agent.msql, recorder: recorder, clock: agent.clock, turn: 1,
	}
	assembler, err := NewBootstrapAssembler(tracedMSQL)
	if err != nil {
		return result, err
	}
	profile, _ := ResolveBootstrapProfile(request.BootstrapProfile)
	bootstrapInput := BootstrapRequest{
		Query: request.Question, Authorization: readOnly, Budget: request.BootstrapBudget,
		Profile: profile,
	}
	bootstrapStarted := queryNow(agent.clock)
	frame, bootstrapErr := assembler.Assemble(ctx, bootstrapInput)
	bootstrapFinished := queryNow(agent.clock)
	if traceErr := queryAppendBootstrap(
		recorder, bootstrapInput, frame, bootstrapStarted, bootstrapFinished, bootstrapErr,
	); traceErr != nil {
		result.Trace = recorder.Snapshot()
		return result, traceErr
	}
	result.Trace = recorder.Snapshot()
	if bootstrapErr != nil {
		return result, bootstrapErr
	}

	var previousCall *ProviderToolCall
	var previousResult string
	toolCalls := uint64(0)
	for providerCall := uint64(1); providerCall <= request.Budget.MaxProviderCalls; providerCall++ {
		tracedMSQL.turn = providerCall
		choice := ProviderToolChoiceRequired
		if len(result.Evidence) > 0 {
			choice = ProviderToolChoiceNone
		}
		providerRequest, buildErr := makeQueryProviderRequest(request, frame, previousCall, previousResult, choice)
		if buildErr != nil {
			result.Trace = recorder.Snapshot()
			return result, buildErr
		}
		response, completeErr := agent.complete(ctx, recorder, providerCall, providerRequest)
		result.Trace = recorder.Snapshot()
		if completeErr != nil {
			return result, completeErr
		}

		if len(result.Evidence) > 0 {
			if response.FinishReason != ProviderFinishStop || len(response.Message.ToolCalls) != 0 ||
				strings.TrimSpace(response.Message.Content) == "" {
				return result, ErrQueryIncompleteAnswer
			}
			if !utf8.ValidString(response.Message.Content) || uint64(len(response.Message.Content)) > request.Budget.AnswerUTF8Bytes {
				return result, fmt.Errorf("%w: final answer exceeded its byte budget", ErrQueryBudget)
			}
			result.Answer = response.Message.Content
			result.Trace = recorder.Snapshot()
			if err := result.Validate(); err != nil {
				return result, err
			}
			return result, nil
		}

		if response.FinishReason == ProviderFinishLength {
			return result, ErrQueryIncompleteAnswer
		}
		if response.FinishReason != ProviderFinishToolCalls {
			return result, ErrQueryMissingSelectEvidence
		}
		if len(response.Message.ToolCalls) != 1 || response.Message.ToolCalls[0].Name != QueryExecuteMSQLToolName {
			if traceErr := queryAppendToolFailure(
				recorder, providerCall, response.Message, agent.clock, "QUERY_TOOL_CALL_INVALID",
			); traceErr != nil {
				result.Trace = recorder.Snapshot()
				return result, traceErr
			}
			result.Trace = recorder.Snapshot()
			return result, ErrInvalidQueryToolCall
		}
		if toolCalls >= request.Budget.MaxToolCalls {
			if traceErr := queryAppendToolFailure(
				recorder, providerCall, response.Message, agent.clock, "QUERY_TOOL_BUDGET",
			); traceErr != nil {
				result.Trace = recorder.Snapshot()
				return result, traceErr
			}
			result.Trace = recorder.Snapshot()
			return result, fmt.Errorf("%w: tool-call limit reached", ErrQueryBudget)
		}
		toolCalls++
		call := response.Message.ToolCalls[0]
		toolStarted := queryNow(agent.clock)
		msqlRequest, decodeErr := decodeQueryToolArguments(call.Arguments, readOnly, request.Budget)
		if decodeErr != nil {
			if traceErr := queryAppendTool(
				recorder, providerCall, call.Arguments, nil, toolStarted, queryNow(agent.clock), decodeErr,
			); traceErr != nil {
				result.Trace = recorder.Snapshot()
				return result, traceErr
			}
			result.Trace = recorder.Snapshot()
			return result, decodeErr
		}
		envelope, executeErr := tracedMSQL.ExecuteMSQL(ctx, msqlRequest)
		var encodedEnvelope []byte
		if executeErr == nil {
			encodedEnvelope, err = json.Marshal(envelope)
			if err != nil {
				executeErr = fmt.Errorf("%w: MSQL result could not be encoded", ErrInvalidQueryToolCall)
			} else if uint64(len(encodedEnvelope)) > request.Budget.ToolResultUTF8Bytes {
				executeErr = fmt.Errorf("%w: MSQL result exceeded its byte budget", ErrQueryBudget)
			}
		}
		if traceErr := queryAppendTool(
			recorder, providerCall, call.Arguments, encodedEnvelope, toolStarted, queryNow(agent.clock), executeErr,
		); traceErr != nil {
			result.Trace = recorder.Snapshot()
			return result, traceErr
		}
		result.Trace = recorder.Snapshot()
		if executeErr != nil {
			return result, executeErr
		}
		result.Evidence = append(result.Evidence, selectEvidence(envelope)...)
		previousCall = &call
		previousResult = string(encodedEnvelope)
		frame = BootstrapFrame{}
	}
	result.Trace = recorder.Snapshot()
	return result, fmt.Errorf("%w: Provider-call limit reached", ErrQueryBudget)
}

func (agent *QueryAgent) complete(
	ctx context.Context,
	recorder *TraceRecorder,
	turn uint64,
	request ProviderRequest,
) (ProviderResponse, error) {
	started := queryNow(agent.clock)
	response, err := agent.provider.Complete(ctx, request)
	finished := queryNow(agent.clock)
	if traceErr := queryAppendProvider(recorder, turn, request, response, started, finished, err); traceErr != nil {
		return ProviderResponse{}, traceErr
	}
	return response, err
}

func validateQueryRequest(request QueryRequest) error {
	if strings.TrimSpace(request.Question) == "" || !utf8.ValidString(request.Question) ||
		utf8.RuneCountInString(request.Question) > 256 || invalidProviderText(request.Model, 200, true) ||
		!validQueryBudget(request.Budget) {
		return ErrInvalidQueryRequest
	}
	if _, valid := ResolveBootstrapProfile(request.BootstrapProfile); !valid {
		return ErrInvalidQueryRequest
	}
	authorizationProbe := protocolmsql.Request{
		Version: protocolmsql.RequestVersion, Source: "SHOW DATABASES",
		Statements: []protocolmsql.StatementInput{{Authorization: request.Authorization}},
	}
	if err := authorizationProbe.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidQueryRequest, err)
	}
	return nil
}

func validQueryBudget(budget QueryBudget) bool {
	return budget.MaxProviderCalls >= 2 && budget.MaxProviderCalls <= 16 &&
		budget.MaxToolCalls >= 1 && budget.MaxToolCalls < budget.MaxProviderCalls &&
		budget.MaxToolStatements >= 1 && budget.MaxToolStatements <= 32 &&
		budget.ToolArgumentsUTF8Bytes >= 256 && budget.ToolArgumentsUTF8Bytes <= 1<<20 &&
		budget.ToolResultUTF8Bytes >= 512 && budget.ToolResultUTF8Bytes <= 1<<20 &&
		budget.AnswerUTF8Bytes >= 1 && budget.AnswerUTF8Bytes <= 1<<20 &&
		budget.MaxOutputTokens >= 1 && budget.MaxOutputTokens <= 65536
}

func queryReadOnlyAuthorization(value protocolmsql.Authorization) protocolmsql.Authorization {
	readOnly := value
	readOnly.AuthorizedDatabases = append([]string(nil), value.AuthorizedDatabases...)
	readOnly.DefaultLevel = protocolmsql.LevelRead
	readOnly.Approval = nil
	if value.DatabaseLevels != nil {
		readOnly.DatabaseLevels = make(map[string]protocolmsql.RiskLevel, len(value.DatabaseLevels))
		for database := range value.DatabaseLevels {
			readOnly.DatabaseLevels[database] = protocolmsql.LevelRead
		}
	}
	return readOnly
}

func selectEvidence(envelope protocolmsql.Envelope) []SelectEvidence {
	evidence := make([]SelectEvidence, 0)
	for _, statement := range envelope.Results {
		if statement.Statement != "SELECT" || statement.Status != protocolmsql.Status("succeeded") || statement.Error != nil {
			continue
		}
		evidence = append(evidence, SelectEvidence{RequestID: envelope.RequestID, Result: cloneQueryStatement(statement)})
	}
	return evidence
}

func cloneQueryStatement(value protocolmsql.StatementResult) protocolmsql.StatementResult {
	cloned := value
	if value.Columns != nil {
		cloned.Columns = make([]protocolmsql.Column, len(value.Columns))
		copy(cloned.Columns, value.Columns)
	}
	cloned.Rows = cloneQueryRows(value.Rows)
	cloned.Warnings = make([]protocolmsql.Notice, len(value.Warnings))
	for index, warning := range value.Warnings {
		cloned.Warnings[index] = warning
		cloned.Warnings[index].Details = cloneQueryMap(warning.Details)
	}
	if value.Revision != nil {
		revision := *value.Revision
		cloned.Revision = &revision
	}
	if value.CommitSequence != nil {
		sequence := *value.CommitSequence
		cloned.CommitSequence = &sequence
	}
	if value.Page != nil {
		page := *value.Page
		cloned.Page = &page
	}
	cloned.RowDetail = append(json.RawMessage(nil), value.RowDetail...)
	cloned.Discovery = append(json.RawMessage(nil), value.Discovery...)
	if value.Error != nil {
		resultError := *value.Error
		resultError.Details = cloneQueryMap(value.Error.Details)
		if value.Error.StatementIndex != nil {
			statementIndex := *value.Error.StatementIndex
			resultError.StatementIndex = &statementIndex
		}
		cloned.Error = &resultError
	}
	return cloned
}

func cloneQueryRows(rows []protocolmsql.Row) []protocolmsql.Row {
	if rows == nil {
		return nil
	}
	cloned := make([]protocolmsql.Row, len(rows))
	for index, row := range rows {
		cloned[index] = protocolmsql.Row(cloneQueryMap(map[string]any(row)))
	}
	return cloned
}

func cloneQueryMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	cloned := make(map[string]any, len(value))
	for key, item := range value {
		cloned[key] = cloneQueryValue(item)
	}
	return cloned
}

func cloneQueryValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneQueryMap(typed)
	case protocolmsql.Row:
		return protocolmsql.Row(cloneQueryMap(map[string]any(typed)))
	case []any:
		cloned := make([]any, len(typed))
		for index, item := range typed {
			cloned[index] = cloneQueryValue(item)
		}
		return cloned
	case json.RawMessage:
		return append(json.RawMessage(nil), typed...)
	case []byte:
		return append([]byte(nil), typed...)
	default:
		return typed
	}
}
