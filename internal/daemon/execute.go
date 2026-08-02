package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/HW-Yue/Memora/internal/assimilation"
	"github.com/HW-Yue/Memora/internal/conversation"
	"github.com/HW-Yue/Memora/internal/dbpackage"
	"github.com/HW-Yue/Memora/internal/feedback"
	"github.com/HW-Yue/Memora/internal/hostinput"
	"github.com/HW-Yue/Memora/internal/ipc"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	msqlservice "github.com/HW-Yue/Memora/internal/msql/service"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/routetrace"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/security"
	"github.com/HW-Yue/Memora/internal/skillwrite"
	"github.com/HW-Yue/Memora/internal/snapshot"
	"github.com/HW-Yue/Memora/internal/store"
	"github.com/HW-Yue/Memora/internal/wikiexport"
)

type executePayload struct {
	Source     string                    `json:"source"`
	Statements []executor.StatementInput `json:"statements,omitempty"`
}

type databaseHandler struct {
	context    context.Context
	dictionary executor.Catalog
	rows       executor.Rows
	store      store.Store
	export     func(context.Context) ([]byte, error)
	security   *security.Service
	traces     *routetrace.Service
	hostInputs *hostinput.Service
	msql       *msqlservice.Service
	closeOnce  sync.Once
	closeErr   error
}

func newDatabaseHandler(
	ctx context.Context,
	dictionary executor.Catalog,
	rows *row.Service,
	database store.Store,
) *databaseHandler {
	return newDatabaseHandlerWithSecurity(
		ctx, dictionary, rows, database, security.New(database, security.Options{}),
	)
}

func newDatabaseHandlerWithSecurity(
	ctx context.Context,
	dictionary executor.Catalog,
	rows *row.Service,
	database store.Store,
	securityService *security.Service,
) *databaseHandler {
	handler := &databaseHandler{
		context: ctx, dictionary: dictionary, rows: rows, store: database,
		export: func(callContext context.Context) ([]byte, error) {
			return snapshot.New(database).Export(callContext)
		},
		security: securityService, hostInputs: hostinput.New(database, hostinput.Options{}),
	}
	handler.msql = msqlservice.New(ctx, msqlservice.Config{
		Catalog: dictionary, Rows: rows,
		Packages: dbpackage.New(database), Wiki: wikiexport.New(database),
	})
	return handler
}

func newNativeDatabaseHandler(
	ctx context.Context,
	dictionary executor.Catalog,
	rows executor.Rows,
	points executor.PointReads,
	routeVectors executor.RouteVectorReader,
	auxiliary store.Store,
	securityService *security.Service,
	traces *routetrace.Service,
	export func(context.Context) ([]byte, error),
) *databaseHandler {
	handler := &databaseHandler{context: ctx, dictionary: dictionary, rows: rows, store: auxiliary,
		export: export, security: securityService, traces: traces,
		hostInputs: hostinput.New(auxiliary, hostinput.Options{})}
	handler.msql = msqlservice.New(ctx, msqlservice.Config{
		Catalog: dictionary, Rows: rows, Points: points, RouteVectors: routeVectors,
	})
	return handler
}

type routeTraceRecordPayload struct {
	Draft         routetrace.Draft       `json:"draft"`
	Authorization security.Authorization `json:"authorization,omitempty"`
}

func Execute(
	ctx context.Context,
	dataDir, source string,
	statements []executor.StatementInput,
) (result.Envelope, error) {
	path, err := SocketPath(dataDir)
	if err != nil {
		return result.Envelope{}, err
	}
	client, err := ipc.Dial(ctx, path)
	if err != nil {
		return result.Envelope{}, err
	}
	defer func() { _ = client.Close() }()
	var envelope result.Envelope
	err = client.Call(ctx, "msql.execute", executePayload{
		Source: source, Statements: statements,
	}, &envelope)
	return envelope, err
}

func Reflect(ctx context.Context, dataDir string, event conversation.Event) (conversation.Receipt, error) {
	path, err := SocketPath(dataDir)
	if err != nil {
		return conversation.Receipt{}, err
	}
	client, err := ipc.Dial(ctx, path)
	if err != nil {
		return conversation.Receipt{}, err
	}
	defer func() { _ = client.Close() }()
	var receipt conversation.Receipt
	err = client.Call(ctx, "conversation.reflect", event, &receipt)
	return receipt, err
}

func Assimilate(ctx context.Context, dataDir string, event assimilation.Event) (assimilation.Receipt, error) {
	path, err := SocketPath(dataDir)
	if err != nil {
		return assimilation.Receipt{}, err
	}
	client, err := ipc.Dial(ctx, path)
	if err != nil {
		return assimilation.Receipt{}, err
	}
	defer func() { _ = client.Close() }()
	var receipt assimilation.Receipt
	err = client.Call(ctx, "assimilation.record", event, &receipt)
	return receipt, err
}

func SubmitAssimilation(
	ctx context.Context,
	dataDir string,
	submission assimilation.Submission,
) (assimilation.SourceReceipt, error) {
	path, err := SocketPath(dataDir)
	if err != nil {
		return assimilation.SourceReceipt{}, err
	}
	client, err := ipc.Dial(ctx, path)
	if err != nil {
		return assimilation.SourceReceipt{}, err
	}
	defer func() { _ = client.Close() }()
	var receipt assimilation.SourceReceipt
	err = client.Call(ctx, "assimilation.submit", submission, &receipt)
	return receipt, err
}

func SourceReceipt(ctx context.Context, dataDir, submissionID string) (assimilation.SourceReceipt, error) {
	path, err := SocketPath(dataDir)
	if err != nil {
		return assimilation.SourceReceipt{}, err
	}
	client, err := ipc.Dial(ctx, path)
	if err != nil {
		return assimilation.SourceReceipt{}, err
	}
	defer func() { _ = client.Close() }()
	var receipt assimilation.SourceReceipt
	err = client.Call(ctx, "assimilation.receipt", struct {
		SubmissionID string `json:"submission_id"`
	}{SubmissionID: submissionID}, &receipt)
	return receipt, err
}

func CaptureHostInput(ctx context.Context, dataDir string, input hostinput.Input) (hostinput.Receipt, error) {
	path, err := SocketPath(dataDir)
	if err != nil {
		return hostinput.Receipt{}, err
	}
	client, err := ipc.Dial(ctx, path)
	if err != nil {
		return hostinput.Receipt{}, err
	}
	defer func() { _ = client.Close() }()
	var receipt hostinput.Receipt
	err = client.Call(ctx, "host_input.capture", input, &receipt)
	return receipt, err
}

func GetHostInput(ctx context.Context, dataDir, inputID, workspace string) (hostinput.Pending, error) {
	path, err := SocketPath(dataDir)
	if err != nil {
		return hostinput.Pending{}, err
	}
	client, err := ipc.Dial(ctx, path)
	if err != nil {
		return hostinput.Pending{}, err
	}
	defer func() { _ = client.Close() }()
	var pending hostinput.Pending
	err = client.Call(ctx, "host_input.get", struct {
		InputID   string `json:"input_id"`
		Workspace string `json:"workspace"`
	}{InputID: inputID, Workspace: workspace}, &pending)
	return pending, err
}

func DecideHostInput(
	ctx context.Context,
	dataDir string,
	decision hostinput.WorthinessDecision,
) (hostinput.WorthinessReceipt, error) {
	path, err := SocketPath(dataDir)
	if err != nil {
		return hostinput.WorthinessReceipt{}, err
	}
	client, err := ipc.Dial(ctx, path)
	if err != nil {
		return hostinput.WorthinessReceipt{}, err
	}
	defer func() { _ = client.Close() }()
	var receipt hostinput.WorthinessReceipt
	err = client.Call(ctx, "worthiness.decide", decision, &receipt)
	return receipt, err
}

func GetWorthinessDecision(
	ctx context.Context,
	dataDir, decisionID, workspace string,
) (hostinput.WorthinessResult, error) {
	path, err := SocketPath(dataDir)
	if err != nil {
		return hostinput.WorthinessResult{}, err
	}
	client, err := ipc.Dial(ctx, path)
	if err != nil {
		return hostinput.WorthinessResult{}, err
	}
	defer func() { _ = client.Close() }()
	var outcome hostinput.WorthinessResult
	err = client.Call(ctx, "worthiness.get", struct {
		DecisionID string `json:"decision_id"`
		Workspace  string `json:"workspace"`
	}{DecisionID: decisionID, Workspace: workspace}, &outcome)
	return outcome, err
}

func RecordRouteTrace(
	ctx context.Context,
	dataDir string,
	draft routetrace.Draft,
	authorization security.Authorization,
) (routetrace.Trace, error) {
	path, err := SocketPath(dataDir)
	if err != nil {
		return routetrace.Trace{}, err
	}
	client, err := ipc.Dial(ctx, path)
	if err != nil {
		return routetrace.Trace{}, err
	}
	defer func() { _ = client.Close() }()
	var receipt routetrace.Trace
	err = client.Call(ctx, "route_trace.record", routeTraceRecordPayload{
		Draft: draft, Authorization: authorization,
	}, &receipt)
	return receipt, err
}

func PruneRouteTraces(ctx context.Context, dataDir string, now time.Time) (uint64, error) {
	path, err := SocketPath(dataDir)
	if err != nil {
		return 0, err
	}
	client, err := ipc.Dial(ctx, path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = client.Close() }()
	var receipt struct {
		Deleted uint64 `json:"deleted"`
	}
	err = client.Call(ctx, "route_trace.prune", struct {
		Now time.Time `json:"now"`
	}{Now: now}, &receipt)
	return receipt.Deleted, err
}

func (handler *databaseHandler) Handle(
	ctx context.Context,
	session ipc.Session,
	request ipc.Request,
) (response json.RawMessage, responseErr error) {
	defer func() {
		if handler.security == nil {
			return
		}
		input := auditInput(request, response, responseErr)
		if err := handler.security.Record(ctx, input); err != nil && responseErr == nil {
			response, responseErr = nil, err
		}
	}()
	if request.Method == "doctor" {
		report, err := handler.doctor(ctx)
		if err != nil {
			return nil, err
		}
		return json.Marshal(report)
	}
	if request.Method == "assimilation.record" {
		return handler.handleAssimilation(ctx, request)
	}
	if request.Method == "assimilation.submit" {
		return handler.handleAssimilationSubmission(ctx, session, request)
	}
	if request.Method == "assimilation.receipt" {
		return handler.handleSourceReceipt(ctx, request)
	}
	if request.Method == "host_input.capture" || request.Method == "host_input.get" {
		return handler.handleHostInput(ctx, request)
	}
	if request.Method == "worthiness.decide" || request.Method == "worthiness.get" {
		return handler.handleWorthiness(ctx, request)
	}
	if request.Method == "semantic_health.report" || request.Method == "semantic_health.maintain" {
		return handler.handleSemanticHealth(ctx, request)
	}
	if request.Method == "conversation.reflect" {
		return handler.handleReflect(ctx, session, request)
	}
	if request.Method == "feedback.record" || request.Method == "feedback.confirm" {
		return handler.handleFeedback(ctx, session, request)
	}
	if request.Method == "route_trace.record" {
		return handler.handleRouteTraceRecord(ctx, request)
	}
	if request.Method == "route_trace.prune" {
		return handler.handleRouteTracePrune(ctx, request)
	}
	if request.Method != "msql.execute" {
		return handleRequest(ctx, session, request)
	}
	var payload executePayload
	decoder := json.NewDecoder(bytes.NewReader(request.Payload))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return json.Marshal(result.FailedRequest(
			request.RequestID, result.CodeInvalidRequest, "MSQL execute payload is invalid", false,
		))
	}
	batch, ok := handler.session(session.ID)
	if !ok {
		return json.Marshal(result.FailedRequest(
			request.RequestID, result.CodeInvalidRequest, "MSQL daemon session is closed", false,
		))
	}
	envelope := batch.ExecuteBatch(ctx, executor.BatchRequest{
		RequestID: request.RequestID, Source: payload.Source, Statements: payload.Statements,
	})
	return json.Marshal(envelope)
}

func (handler *databaseHandler) handleHostInput(ctx context.Context, request ipc.Request) (json.RawMessage, error) {
	processor := handler.hostInputs
	if processor == nil {
		return nil, &hostinput.Error{Code: result.CodeInternal, Message: "capture service is unavailable"}
	}
	decoder := json.NewDecoder(bytes.NewReader(request.Payload))
	decoder.DisallowUnknownFields()
	if request.Method == "host_input.capture" {
		var input hostinput.Input
		if err := decoder.Decode(&input); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
			return nil, &hostinput.Error{Code: result.CodeInvalidRequest, Message: "capture payload is invalid"}
		}
		receipt, err := processor.Capture(ctx, input)
		if err != nil {
			return nil, err
		}
		return json.Marshal(receipt)
	}
	var lookup struct {
		InputID   string `json:"input_id"`
		Workspace string `json:"workspace"`
	}
	if err := decoder.Decode(&lookup); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return nil, &hostinput.Error{Code: result.CodeInvalidRequest, Message: "capture lookup payload is invalid"}
	}
	input, receipt, err := processor.Get(ctx, lookup.InputID, lookup.Workspace)
	if err != nil {
		return nil, err
	}
	return json.Marshal(hostinput.Pending{Input: input, Receipt: receipt})
}

func (handler *databaseHandler) handleWorthiness(ctx context.Context, request ipc.Request) (json.RawMessage, error) {
	processor := handler.hostInputs
	if processor == nil {
		return nil, &hostinput.Error{Code: result.CodeInternal, Message: "worthiness service is unavailable"}
	}
	decoder := json.NewDecoder(bytes.NewReader(request.Payload))
	decoder.DisallowUnknownFields()
	if request.Method == "worthiness.decide" {
		var decision hostinput.WorthinessDecision
		if err := decoder.Decode(&decision); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
			return nil, &hostinput.Error{Code: result.CodeInvalidRequest, Message: "worthiness payload is invalid"}
		}
		receipt, err := processor.Decide(ctx, decision)
		if err != nil {
			return nil, err
		}
		return json.Marshal(receipt)
	}
	var lookup struct {
		DecisionID string `json:"decision_id"`
		Workspace  string `json:"workspace"`
	}
	if err := decoder.Decode(&lookup); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return nil, &hostinput.Error{Code: result.CodeInvalidRequest, Message: "worthiness lookup payload is invalid"}
	}
	decision, receipt, err := processor.GetDecision(ctx, lookup.DecisionID, lookup.Workspace)
	if err != nil {
		return nil, err
	}
	return json.Marshal(hostinput.WorthinessResult{Decision: decision, Receipt: receipt})
}

func (handler *databaseHandler) handleRouteTraceRecord(
	ctx context.Context, request ipc.Request,
) (json.RawMessage, error) {
	if handler.traces == nil {
		return nil, errors.New("Route Trace observation store is unavailable")
	}
	var payload routeTraceRecordPayload
	decoder := json.NewDecoder(bytes.NewReader(request.Payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return nil, routetrace.ErrInvalid
	}
	if payload.Authorization.Version != "" {
		if err := payload.Authorization.Validate(); err != nil {
			return nil, err
		}
		authorized := security.WithAuthorization(ctx, payload.Authorization)
		if err := security.RequireAnyDatabase(authorized, payload.Draft.DatabaseID); err != nil {
			return nil, err
		}
	}
	value, err := handler.traces.Record(ctx, payload.Draft)
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func (handler *databaseHandler) handleRouteTracePrune(
	ctx context.Context, request ipc.Request,
) (json.RawMessage, error) {
	if handler.traces == nil {
		return nil, errors.New("Route Trace observation store is unavailable")
	}
	var payload struct {
		Now time.Time `json:"now"`
	}
	decoder := json.NewDecoder(bytes.NewReader(request.Payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return nil, routetrace.ErrInvalid
	}
	deleted, err := handler.traces.PruneExpired(ctx, payload.Now)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Deleted uint64 `json:"deleted"`
	}{Deleted: deleted})
}

func auditInput(request ipc.Request, response json.RawMessage, responseErr error) security.AuditInput {
	actor, databases := auditIdentity(request)
	status, errorCode := security.AuditSucceeded, ""
	if responseErr != nil {
		status, errorCode = security.AuditFailed, stableCode(responseErr)
	} else {
		var envelope struct {
			OK    *bool `json:"ok"`
			Error *struct {
				Code string `json:"code"`
			} `json:"error"`
			Results []struct {
				Error *struct {
					Code string `json:"code"`
				} `json:"error"`
			} `json:"results"`
		}
		if json.Unmarshal(response, &envelope) == nil && envelope.OK != nil && !*envelope.OK {
			status = security.AuditFailed
			if envelope.Error != nil {
				errorCode = envelope.Error.Code
			}
			if errorCode == "" {
				for _, statement := range envelope.Results {
					if statement.Error != nil {
						errorCode = statement.Error.Code
						break
					}
				}
			}
			if errorCode == "" {
				errorCode = string(result.CodeInternal)
			}
		}
	}
	return security.AuditInput{
		RequestID: request.RequestID, Method: request.Method, Actor: actor,
		AuthorizedDatabases: databases, PayloadSHA256: security.HashPayload(request.Payload),
		Status: status, ErrorCode: errorCode,
	}
}

func auditIdentity(request ipc.Request) (string, []string) {
	actor := "local:" + strconv.Itoa(os.Getuid())
	databases := []string{}
	if request.Method == "route_trace.record" {
		var payload routeTraceRecordPayload
		if json.Unmarshal(request.Payload, &payload) == nil {
			if payload.Authorization.Actor != "" {
				actor = payload.Authorization.Actor
			} else if payload.Draft.Actor != "" {
				actor = payload.Draft.Actor
			}
			databases = append(databases, payload.Authorization.AuthorizedDatabases...)
			if len(databases) == 0 && payload.Draft.DatabaseID != "" {
				databases = append(databases, payload.Draft.DatabaseID)
			}
			return sanitizeAuditIdentity(actor, databases)
		}
	}
	if request.Method == "msql.execute" {
		var payload executePayload
		if json.Unmarshal(request.Payload, &payload) == nil {
			actors := map[string]bool{}
			scopes := map[string]bool{}
			for _, statement := range payload.Statements {
				if value := strings.TrimSpace(statement.Authorization.Actor); value != "" {
					actors[value] = true
				}
				for _, database := range statement.Authorization.AuthorizedDatabases {
					if value := strings.TrimSpace(database); value != "" {
						scopes[value] = true
					}
				}
			}
			if len(actors) == 1 {
				for value := range actors {
					actor = value
				}
			} else if len(actors) > 1 {
				actor = "multiple"
			}
			for value := range scopes {
				databases = append(databases, value)
			}
			sort.Strings(databases)
			return sanitizeAuditIdentity(actor, databases)
		}
	}
	var metadata struct {
		Actor               string   `json:"actor"`
		AuthorizedDatabases []string `json:"authorized_databases"`
	}
	if json.Unmarshal(request.Payload, &metadata) == nil {
		if value := strings.TrimSpace(metadata.Actor); value != "" {
			actor = value
		}
		databases = append(databases, metadata.AuthorizedDatabases...)
	}
	return sanitizeAuditIdentity(actor, databases)
}

func sanitizeAuditIdentity(actor string, databases []string) (string, []string) {
	fallbackActor := "local:" + strconv.Itoa(os.Getuid())
	actor = strings.TrimSpace(actor)
	if security.ValidateMetadataText(actor, 160, true) != nil {
		actor = fallbackActor
	}
	scopes := make([]string, 0, len(databases))
	seen := map[string]bool{}
	for _, database := range databases {
		database = strings.TrimSpace(database)
		key := strings.ToLower(database)
		if seen[key] || security.ValidateMetadataText(database, 200, true) != nil {
			continue
		}
		seen[key] = true
		scopes = append(scopes, database)
		if len(scopes) == 32 {
			break
		}
	}
	sort.Strings(scopes)
	return actor, scopes
}

func stableCode(err error) string {
	var stable interface{ StableCode() string }
	if errors.As(err, &stable) {
		return stable.StableCode()
	}
	return string(result.CodeInternal)
}

func (handler *databaseHandler) handleFeedback(
	ctx context.Context,
	session ipc.Session,
	request ipc.Request,
) (json.RawMessage, error) {
	batch, ok := handler.session(session.ID)
	if !ok {
		return nil, &feedback.Error{Code: result.CodeInvalidRequest, Message: "MSQL daemon session is closed"}
	}
	tool := skillwrite.ToolFunc(func(callContext context.Context, call skillwrite.Call) (result.Envelope, error) {
		return batch.ExecuteBatch(callContext, call.Request), nil
	})
	processor := feedback.New(handler.store, handler.rows, tool)
	decoder := json.NewDecoder(bytes.NewReader(request.Payload))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if request.Method == "feedback.record" {
		var event feedback.Event
		if err := decoder.Decode(&event); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
			return nil, &feedback.Error{Code: result.CodeInvalidRequest, Message: "feedback event payload is invalid"}
		}
		receipt, err := processor.Record(ctx, event)
		if err != nil {
			return nil, err
		}
		return json.Marshal(receipt)
	}
	var confirmation feedback.Confirmation
	if err := decoder.Decode(&confirmation); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return nil, &feedback.Error{Code: result.CodeInvalidRequest, Message: "feedback confirmation payload is invalid"}
	}
	receipt, err := processor.Confirm(ctx, confirmation)
	if err != nil {
		return nil, err
	}
	return json.Marshal(receipt)
}

func (handler *databaseHandler) handleAssimilationSubmission(
	ctx context.Context,
	session ipc.Session,
	request ipc.Request,
) (json.RawMessage, error) {
	var submission assimilation.Submission
	decoder := json.NewDecoder(bytes.NewReader(request.Payload))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(&submission); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return nil, &assimilation.SubmissionError{Code: result.CodeInvalidRequest, Message: "assimilation submission payload is invalid"}
	}
	batch, ok := handler.session(session.ID)
	if !ok {
		return nil, &assimilation.SubmissionError{Code: result.CodeInvalidRequest, Message: "MSQL daemon session is closed"}
	}
	tool := skillwrite.ToolFunc(func(callContext context.Context, call skillwrite.Call) (result.Envelope, error) {
		return batch.ExecuteBatch(callContext, call.Request), nil
	})
	receipt, err := assimilation.New(handler.store).Submit(ctx, submission, tool)
	if err != nil {
		return nil, err
	}
	return json.Marshal(receipt)
}

func (handler *databaseHandler) handleSourceReceipt(
	ctx context.Context,
	request ipc.Request,
) (json.RawMessage, error) {
	var payload struct {
		SubmissionID string `json:"submission_id"`
	}
	decoder := json.NewDecoder(bytes.NewReader(request.Payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return nil, &assimilation.SubmissionError{Code: result.CodeInvalidRequest, Message: "Source Receipt request is invalid"}
	}
	receipt, err := assimilation.New(handler.store).SourceReceipt(ctx, payload.SubmissionID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(receipt)
}

func (handler *databaseHandler) handleAssimilation(
	ctx context.Context,
	request ipc.Request,
) (json.RawMessage, error) {
	var event assimilation.Event
	decoder := json.NewDecoder(bytes.NewReader(request.Payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&event); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return nil, &assimilation.Error{Code: result.CodeInvalidRequest, Message: "assimilation event payload is invalid"}
	}
	receipt, err := assimilation.New(handler.store).Process(ctx, event)
	if err != nil {
		return nil, err
	}
	return json.Marshal(receipt)
}

func (handler *databaseHandler) handleReflect(
	ctx context.Context,
	session ipc.Session,
	request ipc.Request,
) (json.RawMessage, error) {
	var event conversation.Event
	decoder := json.NewDecoder(bytes.NewReader(request.Payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&event); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return nil, &conversation.Error{Code: result.CodeInvalidRequest, Message: "conversation event payload is invalid"}
	}
	batch, ok := handler.session(session.ID)
	if !ok {
		return nil, &conversation.Error{Code: result.CodeInvalidRequest, Message: "MSQL daemon session is closed"}
	}
	tool := skillwrite.ToolFunc(func(callContext context.Context, call skillwrite.Call) (result.Envelope, error) {
		return batch.ExecuteBatch(callContext, call.Request), nil
	})
	receipt, err := conversation.New(conversation.NewJournal(handler.store), tool).Process(ctx, event)
	if err != nil {
		return nil, err
	}
	return json.Marshal(receipt)
}

func (handler *databaseHandler) SessionClosed(_ context.Context, session ipc.Session) error {
	closeErr := handler.msql.CloseSession(session.ID)
	flushErr := handler.security.Flush(handler.context)
	return errors.Join(closeErr, flushErr)
}

func (handler *databaseHandler) Close() error {
	handler.closeOnce.Do(func() {
		handler.closeErr = errors.Join(
			handler.msql.Close(),
			handler.security.Flush(handler.context),
		)
	})
	return handler.closeErr
}

func (handler *databaseHandler) session(id string) (*msqlservice.Session, bool) {
	session, err := handler.msql.OpenSession(id)
	return session, err == nil
}
