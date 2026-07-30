package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"sync"

	"github.com/HW-Yue/Memora/internal/assimilation"
	"github.com/HW-Yue/Memora/internal/conversation"
	"github.com/HW-Yue/Memora/internal/ipc"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/skillwrite"
	"github.com/HW-Yue/Memora/internal/store"
)

type executePayload struct {
	Source     string                    `json:"source"`
	Statements []executor.StatementInput `json:"statements,omitempty"`
}

type databaseHandler struct {
	mu         sync.Mutex
	context    context.Context
	dictionary executor.Catalog
	rows       *row.Service
	store      store.Store
	sessions   map[string]*executor.BatchSession
	closed     bool
}

func newDatabaseHandler(
	ctx context.Context,
	dictionary executor.Catalog,
	rows *row.Service,
	database store.Store,
) *databaseHandler {
	return &databaseHandler{
		context: ctx, dictionary: dictionary, rows: rows, store: database,
		sessions: make(map[string]*executor.BatchSession),
	}
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

func (handler *databaseHandler) Handle(ctx context.Context, session ipc.Session, request ipc.Request) (json.RawMessage, error) {
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
	if request.Method == "conversation.reflect" {
		return handler.handleReflect(ctx, session, request)
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
		return nil
	}
	return batch.Close()
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
	session := executor.NewBatchSession(handler.context, handler.dictionary, handler.rows)
	handler.sessions[id] = session
	return session, true
}
