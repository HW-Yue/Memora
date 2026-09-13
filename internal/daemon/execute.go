package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"

	"github.com/HW-Yue/Memora/internal/ipc"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	msqlservice "github.com/HW-Yue/Memora/internal/msql/service"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/routetrace"
	"github.com/HW-Yue/Memora/internal/security"
	"github.com/HW-Yue/Memora/internal/store"
)

type executePayload struct {
	Source     string                    `json:"source"`
	Statements []executor.StatementInput `json:"statements,omitempty"`
}

type routeTraceRecordPayload struct {
	Draft         routetrace.Draft       `json:"draft"`
	Authorization security.Authorization `json:"authorization,omitempty"`
}

type handler struct {
	context   context.Context
	database  *store.DB
	msql      *msqlservice.Service
	closeOnce sync.Once
	closeErr  error
}

func newHandler(ctx context.Context, database *store.DB) *handler {
	rows := database.Rows()
	return &handler{
		context: ctx, database: database,
		msql: msqlservice.New(ctx, msqlservice.Config{
			Catalog: database, Rows: rows,
			Transactions: func(callContext context.Context) (executor.ExplicitTransaction, error) {
				return database.BeginTransaction(callContext)
			},
		}),
	}
}

// Execute runs MSQL against the daemon serving dataDir.
func Execute(ctx context.Context, dataDir, source string, statements []executor.StatementInput) (result.Envelope, error) {
	client, err := dial(ctx, dataDir)
	if err != nil {
		return result.Envelope{}, err
	}
	defer func() { _ = client.Close() }()
	var envelope result.Envelope
	err = client.Call(ctx, "msql.execute", executePayload{Source: source, Statements: statements}, &envelope)
	return envelope, err
}

// RecordRouteTrace stores an Agent's navigation receipt.
func RecordRouteTrace(ctx context.Context, dataDir string, draft routetrace.Draft, authorization security.Authorization) (routetrace.Trace, error) {
	client, err := dial(ctx, dataDir)
	if err != nil {
		return routetrace.Trace{}, err
	}
	defer func() { _ = client.Close() }()
	var receipt routetrace.Trace
	err = client.Call(ctx, "route_trace.record", routeTraceRecordPayload{Draft: draft, Authorization: authorization}, &receipt)
	return receipt, err
}

// Reindex rebuilds the lexical postings and refills the vector index.
func Reindex(ctx context.Context, dataDir string) error {
	client, err := dial(ctx, dataDir)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	var receipt struct{}
	return client.Call(ctx, "reindex", nil, &receipt)
}

func dial(ctx context.Context, dataDir string) (*ipc.Client, error) {
	path, err := SocketPath(dataDir)
	if err != nil {
		return nil, err
	}
	return ipc.Dial(ctx, path)
}

func (h *handler) Handle(ctx context.Context, session ipc.Session, request ipc.Request) (json.RawMessage, error) {
	switch request.Method {
	case "doctor":
		report, err := h.doctor(ctx)
		if err != nil {
			return nil, err
		}
		return json.Marshal(report)
	case "reindex":
		if err := h.database.Reindex(ctx); err != nil {
			return nil, err
		}
		return json.Marshal(struct{}{})
	case "route_trace.record":
		return h.recordRouteTrace(ctx, request)
	case "msql.execute":
	default:
		return handleRequest(ctx, session, request)
	}
	var payload executePayload
	decoder := json.NewDecoder(bytes.NewReader(request.Payload))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return json.Marshal(result.FailedRequest(request.RequestID, result.CodeInvalidRequest, "MSQL execute payload is invalid", false))
	}
	batch, err := h.msql.OpenSession(session.ID)
	if err != nil {
		return json.Marshal(result.FailedRequest(request.RequestID, result.CodeInvalidRequest, "MSQL daemon session is closed", false))
	}
	envelope := batch.ExecuteBatch(ctx, executor.BatchRequest{
		RequestID: request.RequestID, Source: payload.Source, Statements: payload.Statements,
	})
	return json.Marshal(envelope)
}

func (h *handler) recordRouteTrace(ctx context.Context, request ipc.Request) (json.RawMessage, error) {
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
	value, err := h.database.RecordRouteTrace(ctx, payload.Draft)
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func (h *handler) SessionClosed(_ context.Context, session ipc.Session) error {
	return h.msql.CloseSession(session.ID)
}

func (h *handler) Close() error {
	h.closeOnce.Do(func() { h.closeErr = errors.Join(h.msql.Close()) })
	return h.closeErr
}
