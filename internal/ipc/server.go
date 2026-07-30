package ipc

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Server struct {
	handler Handler
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	conns   map[net.Conn]struct{}
}

func NewServer(handler Handler) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{handler: handler, ctx: ctx, cancel: cancel, conns: make(map[net.Conn]struct{})}
}

func (server *Server) Serve(ctx context.Context, listener net.Listener) error {
	go func() {
		select {
		case <-ctx.Done():
			_ = listener.Close()
		case <-server.ctx.Done():
			_ = listener.Close()
		}
	}()
	for {
		connection, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || server.ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go server.ServeConnection(connection)
	}
}

func (server *Server) ServeConnection(connection net.Conn) {
	server.mu.Lock()
	server.conns[connection] = struct{}{}
	server.mu.Unlock()
	defer func() {
		server.mu.Lock()
		delete(server.conns, connection)
		server.mu.Unlock()
		_ = connection.Close()
	}()

	ctx, cancelConnection := context.WithCancel(server.ctx)
	defer cancelConnection()
	session := Session{ID: uuid.NewString()}
	var requests sync.WaitGroup
	var writes sync.Mutex
	var activeMu sync.Mutex
	active := make(map[string]context.CancelFunc)

	for {
		var request Request
		if err := ReadFrame(connection, &request, DefaultMaxFrameSize); err != nil {
			cancelConnection()
			requests.Wait()
			_ = server.handler.SessionClosed(context.Background(), session)
			return
		}
		if request.Method == cancelMethod {
			activeMu.Lock()
			cancel := active[request.CancelRequestID]
			activeMu.Unlock()
			if cancel != nil {
				cancel()
			}
			continue
		}
		if request.Version != Version {
			writeResponse(connection, &writes, Response{
				Version: Version, RequestID: request.RequestID, SessionID: session.ID,
				Error: &ResponseError{Code: "protocol_version", Message: ErrProtocolVersion.Error()},
			})
			continue
		}
		requestCtx, cancel := requestContext(ctx, request.TimeoutMS)
		activeMu.Lock()
		active[request.RequestID] = cancel
		activeMu.Unlock()
		requests.Add(1)
		go func() {
			defer requests.Done()
			response := Response{Version: Version, RequestID: request.RequestID, SessionID: session.ID}
			defer func() {
				cancel()
				activeMu.Lock()
				delete(active, request.RequestID)
				activeMu.Unlock()
			}()
			payload, err := server.handler.Handle(requestCtx, session, request)
			response.Payload = payload
			if err != nil {
				response.Error = responseError(err)
			}
			writeResponse(connection, &writes, response)
		}()
	}
}

func (server *Server) Close() error {
	server.cancel()
	server.mu.Lock()
	defer server.mu.Unlock()
	for connection := range server.conns {
		_ = connection.Close()
	}
	return nil
}

func requestContext(parent context.Context, timeoutMS int64) (context.Context, context.CancelFunc) {
	if timeoutMS > 0 {
		return context.WithTimeout(parent, time.Duration(timeoutMS)*time.Millisecond)
	}
	return context.WithCancel(parent)
}

func responseError(err error) *ResponseError {
	code := "handler_error"
	if errors.Is(err, context.Canceled) {
		code = "cancelled"
	} else if errors.Is(err, context.DeadlineExceeded) {
		code = "deadline_exceeded"
	} else if stable, ok := err.(interface{ StableCode() string }); ok {
		code = stable.StableCode()
	}
	return &ResponseError{Code: code, Message: err.Error()}
}

func writeResponse(connection net.Conn, writes *sync.Mutex, response Response) {
	writes.Lock()
	defer writes.Unlock()
	_ = WriteFrame(connection, response, DefaultMaxFrameSize)
}
