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

	"github.com/HW-Yue/Memora/internal/ipc"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/msql/readquery"
	msqlservice "github.com/HW-Yue/Memora/internal/msql/service"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/routetrace"
	"github.com/HW-Yue/Memora/internal/security"
	"github.com/HW-Yue/Memora/internal/sqlstore"
	"github.com/HW-Yue/Memora/internal/store"
)

type executePayload struct {
	Source     string                    `json:"source"`
	Statements []executor.StatementInput `json:"statements,omitempty"`
	// ReadOnly marks a request that must not mutate anything. The transport that
	// owns the policy is the daemon: a client that kept its own list of allowed
	// statements would refuse a statement it had never heard of, which reads as
	// "this feature does not exist" rather than "your client is older".
	ReadOnly bool `json:"read_only,omitempty"`
}

// securityNamespace is the KV namespace the audit trail lives in. The audit
// joins an open transaction by name, so the name is stated once and used by
// both the service that owns the trail and the transaction that takes it.
const securityNamespace = "security"

// auditBudget bounds how long an audit write may wait for the write lock. The
// audit is never the reason a request misses its deadline: while somebody else
// holds the lock the event stays pending and lands at the next flush, which is
// what "best effort" already meant everywhere else in this path.
const auditBudget = 250 * time.Millisecond

// shutdownAuditBudget is the last flush's budget. Nothing holds the write lock
// against it by then, so it is long only to survive a slow disk.
const shutdownAuditBudget = 5 * time.Second

// kvTransaction is an explicit transaction that can lend its open handle to a
// bucketed key-value writer.
type kvTransaction interface {
	KVTx(namespace string) store.Tx
}

type databaseHandler struct {
	context   context.Context
	identity  Identity
	database  *sqlstore.DB
	security  *security.Service
	msql      *msqlservice.Service
	closeOnce sync.Once
	closeErr  error
}

func newHandler(ctx context.Context, database *sqlstore.DB, identity Identity) *databaseHandler {
	rows := database.Rows()
	handler := &databaseHandler{
		context:  ctx,
		database: database,
		security: security.New(database.KV(securityNamespace), security.Options{}),
		identity: identity,
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

// Execute runs a batch that may write.
func Execute(
	ctx context.Context,
	dataDir, source string,
	statements []executor.StatementInput,
) (result.Envelope, error) {
	return execute(ctx, dataDir, source, statements, false)
}

// ExecuteReadOnly runs a batch the daemon must refuse if it would write. The
// decision lives here rather than in the caller so that every client — including
// one older than the statement being sent — is held to the same policy.
func ExecuteReadOnly(
	ctx context.Context,
	dataDir, source string,
	statements []executor.StatementInput,
) (result.Envelope, error) {
	return execute(ctx, dataDir, source, statements, true)
}

func execute(
	ctx context.Context,
	dataDir, source string,
	statements []executor.StatementInput,
	readOnly bool,
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
		Source: source, Statements: statements, ReadOnly: readOnly,
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
		if err := handler.record(ctx, session, input); err != nil && responseErr == nil {
			response, responseErr = nil, err
		}
	}()
	if request.Method == "build" {
		// The daemon's own identity, so a client can tell whether it is talking to
		// the same build it is running.
		return json.Marshal(handler.identity)
	}
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
	if payload.ReadOnly {
		if _, err := readquery.Validate(payload.Source); err != nil {
			return json.Marshal(result.FailedRequest(request.RequestID, result.CodeUnsupported,
				"this transport is read-only: "+err.Error(), false))
		}
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

// record writes the audit of one request. Inside an explicit transaction it
// writes through that transaction: the lock the audit would otherwise wait for
// is the one this very request is holding, so waiting for it is waiting for
// itself — and once one request waits there, every later request queues behind
// the audit service and the instance answers nothing at all.
//
// It is also the honest shape. A record written beside the transaction would
// survive a rollback and describe writes that never landed.
func (handler *databaseHandler) record(
	ctx context.Context, session ipc.Session, input security.AuditInput,
) error {
	// Detached from the request's own cancellation: a request that was cancelled
	// or timed out is exactly the one worth having a record of, and the audit is
	// not what the caller was waiting for.
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), auditBudget)
	defer cancel()
	return handler.msql.WithActiveTransaction(session.ID, func(active executor.ExplicitTransaction) error {
		if joinable, ok := active.(kvTransaction); ok {
			return handler.security.RecordIn(auditCtx, input, joinable.KVTx(securityNamespace))
		}
		return handler.security.Record(auditCtx, input)
	})
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
	// Bounded like every other audit write: another session may be holding a
	// transaction open, and a disconnect must not wait on it. Detached, because
	// a disconnect at shutdown would otherwise arrive with the daemon's context
	// already cancelled and drop the trail it came to write.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(handler.context), auditBudget)
	defer cancel()
	flushErr := handler.security.Flush(ctx)
	return errors.Join(closeErr, flushErr)
}

func (handler *databaseHandler) Close() error {
	handler.closeOnce.Do(func() {
		// The last flush the pending trail will get, and by now msql.Close has
		// rolled back every open transaction, so the write lock is free. The
		// context is detached and given its own budget because the daemon's own
		// context is already cancelled by the time this runs — flushing on it
		// would fail every time and lose exactly the events that had to wait.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(handler.context), shutdownAuditBudget)
		defer cancel()
		// Rolled back first, flushed second: the order is the reason the lock is
		// free, not an accident of argument evaluation.
		closeErr := handler.msql.Close()
		handler.closeErr = errors.Join(closeErr, handler.security.Flush(ctx))
	})
	return handler.closeErr
}

func (handler *databaseHandler) session(id string) (*msqlservice.Session, bool) {
	session, err := handler.msql.OpenSession(id)
	return session, err == nil
}
