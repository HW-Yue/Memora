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

	"github.com/HW-Yue/Memora/internal/ipc"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	msqlservice "github.com/HW-Yue/Memora/internal/msql/service"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/routetrace"
	"github.com/HW-Yue/Memora/internal/security"
	"github.com/HW-Yue/Memora/internal/sqlstore"
)

type executePayload struct {
	Source     string                    `json:"source"`
	Statements []executor.StatementInput `json:"statements,omitempty"`
}

type databaseHandler struct {
	context   context.Context
	database  *sqlstore.DB
	security  *security.Service
	msql      *msqlservice.Service
	closeOnce sync.Once
	closeErr  error
}

func newHandler(ctx context.Context, database *sqlstore.DB) *databaseHandler {
	rows := database.Rows()
	handler := &databaseHandler{
		context:  ctx,
		database: database,
		security: security.New(database.KV("security"), security.Options{}),
	}
	handler.msql = msqlservice.New(ctx, msqlservice.Config{
		Catalog: database, Rows: rows,
		Transactions: func(callContext context.Context) (executor.ExplicitTransaction, error) {
			return database.BeginTransaction(callContext)
		},
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
	if request.Method == "route_trace.record" {
		return handler.handleRouteTraceRecord(ctx, request)
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

func (handler *databaseHandler) handleRouteTraceRecord(
	ctx context.Context, request ipc.Request,
) (json.RawMessage, error) {
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
	value, err := handler.database.RecordRouteTrace(ctx, payload.Draft)
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
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
