// Package service owns the instance-level MSQL execution service and its
// isolated logical sessions.
package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	protocolmsql "github.com/HW-Yue/Memora/protocol/msql"
)

var (
	ErrInvalidSession = errors.New("MSQL session ID is invalid")
	ErrServiceClosed  = errors.New("MSQL service is closed")
	ErrSessionLimit   = errors.New("MSQL service has too many open sessions")
	ErrSessionClosed  = errors.New("MSQL session is closed")
)

// Config is the shared dependency set used to construct every logical
// session. Dependencies remain owned by the Memora instance.
type Config struct {
	Catalog      executor.Catalog
	Rows         executor.Rows
	Points       executor.PointReads
	Packages     executor.PackageManager
	Wiki         executor.WikiExporter
	Assimilation executor.AssimilationCommitter
	Transactions executor.TransactionFactory

	// MaxSessions caps how many sessions may be open at once. Sessions are
	// created on demand by ID and only ever removed by an explicit close, so
	// without a cap a caller that invents an ID per request grows the registry
	// without bound. Zero selects DefaultMaxSessions.
	MaxSessions int
}

// DefaultMaxSessions bounds the session registry when a Config does not.
const DefaultMaxSessions = 1024

// Service owns the session registry for one Memora instance.
type Service struct {
	mu        sync.Mutex
	context   context.Context
	cancel    context.CancelFunc
	config    Config
	sessions  map[string]*Session
	closed    bool
	closeDone chan struct{}
	closeErr  error
}

// New constructs one shared service. It does not open a session or allocate
// an independent storage engine.
func New(ctx context.Context, config Config) *Service {
	if ctx == nil {
		ctx = context.Background()
	}
	serviceContext, cancel := context.WithCancel(ctx)
	return &Service{
		context:   serviceContext,
		cancel:    cancel,
		config:    config,
		sessions:  make(map[string]*Session),
		closeDone: make(chan struct{}),
	}
}

// OpenSession returns the existing session for id or creates an isolated one.
func (service *Service) OpenSession(id string) (*Session, error) {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > 256 {
		return nil, ErrInvalidSession
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.closed || service.context.Err() != nil {
		return nil, ErrServiceClosed
	}
	if session := service.sessions[id]; session != nil {
		return session, nil
	}
	limit := service.config.MaxSessions
	if limit <= 0 {
		limit = DefaultMaxSessions
	}
	if len(service.sessions) >= limit {
		return nil, fmt.Errorf("%w: %d of %d in use", ErrSessionLimit, len(service.sessions), limit)
	}
	session := newSession(service.context, id, service.config)
	service.sessions[id] = session
	return session, nil
}

// CloseSession removes and closes one logical session. Closing an unknown
// session is idempotent.
func (service *Service) CloseSession(id string) error {
	service.mu.Lock()
	defer service.mu.Unlock()
	session := service.sessions[id]
	delete(service.sessions, id)
	if session == nil {
		return nil
	}
	return session.Close()
}

// Close rejects new sessions, cancels every active call, and then rolls back
// all unfinished transactions.
func (service *Service) Close() error {
	service.mu.Lock()
	if service.closed {
		done := service.closeDone
		service.mu.Unlock()
		<-done
		return service.closeErr
	}
	service.closed = true
	service.cancel()
	sessions := make([]*Session, 0, len(service.sessions))
	for id, session := range service.sessions {
		sessions = append(sessions, session)
		delete(service.sessions, id)
	}
	service.mu.Unlock()

	for _, session := range sessions {
		session.beginClose()
	}
	var closeErr error
	for _, session := range sessions {
		closeErr = errors.Join(closeErr, session.Close())
	}
	service.closeErr = closeErr
	close(service.closeDone)
	return closeErr
}

// Session serializes requests and owns the explicit transaction for one
// logical caller. Different Session values may execute concurrently.
type Session struct {
	id         string
	context    context.Context
	cancel     context.CancelFunc
	gate       chan struct{}
	batch      *executor.BatchSession
	requestSeq atomic.Uint64
	closed     atomic.Bool
	closeOnce  sync.Once
	closeDone  chan struct{}
	closeErr   error
}

func newSession(ctx context.Context, id string, config Config) *Session {
	sessionContext, cancel := context.WithCancel(ctx)
	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	batch := executor.NewBatchSessionWithCapabilitiesAndTransactions(
		sessionContext, config.Catalog, config.Rows, config.Points,
		config.Packages, config.Wiki, config.Assimilation, config.Transactions,
	)
	return &Session{
		id: id, context: sessionContext, cancel: cancel, gate: gate,
		batch: batch, closeDone: make(chan struct{}),
	}
}

// ExecuteMSQL is the untrusted, runtime-neutral adapter used by an in-process
// Agent. It validates the public protocol before entering the executor.
func (session *Session) ExecuteMSQL(
	ctx context.Context,
	request protocolmsql.Request,
) (protocolmsql.Envelope, error) {
	if err := request.Validate(); err != nil {
		return protocolmsql.Envelope{}, err
	}
	batchRequest := toBatchRequest(request)
	batchRequest.RequestID = fmt.Sprintf("%s:%d", session.id, session.requestSeq.Add(1))
	value, err := session.execute(ctx, func(callContext context.Context) result.Envelope {
		return session.batch.Execute(callContext, batchRequest)
	})
	if err != nil {
		return protocolmsql.Envelope{}, err
	}
	return toProtocolEnvelope(value)
}

// ExecuteBatch is the trusted compatibility adapter used by the local IPC
// server and older internal workflows. It deliberately preserves the legacy
// zero-StatementInput mode, which ExecuteMSQL cannot invoke.
func (session *Session) ExecuteBatch(ctx context.Context, request executor.BatchRequest) result.Envelope {
	value, err := session.execute(ctx, func(callContext context.Context) result.Envelope {
		return session.batch.Execute(callContext, request)
	})
	if err == nil {
		return value
	}
	requestID := request.RequestID
	if requestID == "" {
		requestID = "unknown"
	}
	code, message, retryable := callError(err)
	return result.FailedRequest(requestID, code, message, retryable)
}

func (session *Session) execute(
	ctx context.Context,
	call func(context.Context) result.Envelope,
) (result.Envelope, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if session.closed.Load() {
		return result.Envelope{}, ErrSessionClosed
	}
	callContext, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(session.context, cancel)
	defer func() {
		stop()
		cancel()
	}()
	select {
	case <-session.gate:
	case <-ctx.Done():
		return result.Envelope{}, ctx.Err()
	case <-session.context.Done():
		return result.Envelope{}, ErrSessionClosed
	}
	defer func() { session.gate <- struct{}{} }()
	if session.closed.Load() {
		return result.Envelope{}, ErrSessionClosed
	}
	return call(callContext), nil
}

func (session *Session) beginClose() {
	if session.closed.CompareAndSwap(false, true) {
		session.cancel()
	}
}

// Close cancels the active request before waiting for the serialized executor
// boundary, then rolls back any explicit transaction.
func (session *Session) Close() error {
	session.beginClose()
	session.closeOnce.Do(func() {
		<-session.gate
		session.closeErr = session.batch.Close()
		session.gate <- struct{}{}
		close(session.closeDone)
	})
	<-session.closeDone
	return session.closeErr
}

func callError(err error) (result.Code, string, bool) {
	switch {
	case errors.Is(err, context.Canceled):
		return result.CodeCancelled, "query was cancelled", true
	case errors.Is(err, context.DeadlineExceeded):
		return result.CodeDeadlineExceeded, "query exceeded its deadline", true
	default:
		return result.CodeInvalidRequest, err.Error(), false
	}
}
