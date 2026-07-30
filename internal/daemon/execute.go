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

	"github.com/HW-Yue/Memora/internal/assimilation"
	"github.com/HW-Yue/Memora/internal/conversation"
	"github.com/HW-Yue/Memora/internal/dbpackage"
	"github.com/HW-Yue/Memora/internal/feedback"
	"github.com/HW-Yue/Memora/internal/ipc"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
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
	mu         sync.Mutex
	context    context.Context
	dictionary executor.Catalog
	rows       executor.Rows
	legacyRows *row.Service
	store      store.Store
	export     func(context.Context) ([]byte, error)
	security   *security.Service
	sessions   map[string]*executor.BatchSession
	closed     bool
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
	return &databaseHandler{
		context: ctx, dictionary: dictionary, rows: rows, legacyRows: rows, store: database,
		export: func(callContext context.Context) ([]byte, error) {
			return snapshot.New(database).Export(callContext)
		},
		security: securityService, sessions: make(map[string]*executor.BatchSession),
	}
}

func newNativeDatabaseHandler(
	ctx context.Context,
	dictionary executor.Catalog,
	rows executor.Rows,
	auxiliary store.Store,
	securityService *security.Service,
	export func(context.Context) ([]byte, error),
) *databaseHandler {
	return &databaseHandler{context: ctx, dictionary: dictionary, rows: rows, store: auxiliary,
		export: export, security: securityService, sessions: make(map[string]*executor.BatchSession)}
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
	if request.Method == "semantic_health.report" || request.Method == "semantic_health.maintain" {
		return handler.handleSemanticHealth(ctx, request)
	}
	if request.Method == "conversation.reflect" {
		return handler.handleReflect(ctx, session, request)
	}
	if request.Method == "feedback.record" || request.Method == "feedback.confirm" {
		return handler.handleFeedback(ctx, session, request)
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
	envelope := batch.Execute(ctx, executor.BatchRequest{
		RequestID: request.RequestID, Source: payload.Source, Statements: payload.Statements,
	})
	return json.Marshal(envelope)
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
		return batch.Execute(callContext, call.Request), nil
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
		return batch.Execute(callContext, call.Request), nil
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
		return batch.Execute(callContext, call.Request), nil
	})
	receipt, err := conversation.New(conversation.NewJournal(handler.store), tool).Process(ctx, event)
	if err != nil {
		return nil, err
	}
	return json.Marshal(receipt)
}

func (handler *databaseHandler) SessionClosed(_ context.Context, session ipc.Session) error {
	handler.mu.Lock()
	batch := handler.sessions[session.ID]
	delete(handler.sessions, session.ID)
	handler.mu.Unlock()
	if batch == nil {
		return handler.security.Flush(handler.context)
	}
	closeErr := batch.Close()
	flushErr := handler.security.Flush(handler.context)
	return errors.Join(closeErr, flushErr)
}

func (handler *databaseHandler) Close() error {
	handler.mu.Lock()
	if handler.closed {
		handler.mu.Unlock()
		return nil
	}
	handler.closed = true
	sessions := make([]*executor.BatchSession, 0, len(handler.sessions))
	for id, session := range handler.sessions {
		sessions = append(sessions, session)
		delete(handler.sessions, id)
	}
	handler.mu.Unlock()

	var first error
	for _, session := range sessions {
		if err := session.Close(); err != nil && first == nil {
			first = err
		}
	}
	if err := handler.security.Flush(handler.context); err != nil && first == nil {
		first = err
	}
	return first
}

func (handler *databaseHandler) session(id string) (*executor.BatchSession, bool) {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if handler.closed {
		return nil, false
	}
	if session := handler.sessions[id]; session != nil {
		return session, true
	}
	session := executor.NewBatchSessionWithManagement(
		handler.context, handler.dictionary, handler.rows, nil, nil,
	)
	if handler.legacyRows != nil {
		session = executor.NewBatchSessionWithManagement(
			handler.context, handler.dictionary, handler.rows,
			dbpackage.New(handler.store), wikiexport.New(handler.store),
		)
	}
	handler.sessions[id] = session
	return session, true
}
