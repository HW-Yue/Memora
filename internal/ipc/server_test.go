package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientSupportsConcurrentRequestsInOneSession(t *testing.T) {
	t.Parallel()

	server, client := startTestPair(t, HandlerFunc(func(_ context.Context, session Session, request Request) (json.RawMessage, error) {
		return json.Marshal(map[string]string{"session": session.ID, "request": request.RequestID})
	}))
	defer server.Close()
	defer client.Close()

	const requests = 16
	var wait sync.WaitGroup
	sessions := make(chan string, requests)
	for index := 0; index < requests; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			var result map[string]string
			if err := client.Call(context.Background(), "echo", nil, &result); err != nil {
				t.Errorf("Call() error = %v", err)
				return
			}
			sessions <- result["session"]
		}()
	}
	wait.Wait()
	close(sessions)
	var first string
	for session := range sessions {
		if session == "" {
			t.Fatal("empty session ID")
		}
		if first == "" {
			first = session
		} else if session != first {
			t.Fatalf("session ID = %q, want %q", session, first)
		}
	}
}

func TestRequestTimeoutCancelsHandler(t *testing.T) {
	t.Parallel()

	cancelled := make(chan struct{})
	server, client := startTestPair(t, HandlerFunc(func(ctx context.Context, _ Session, _ Request) (json.RawMessage, error) {
		<-ctx.Done()
		close(cancelled)
		return nil, ctx.Err()
	}))
	defer server.Close()
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := client.Call(ctx, "wait", nil, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Call() error = %v, want deadline exceeded", err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("handler was not cancelled")
	}
}

func TestClientCancellationCancelsHandler(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	cancelled := make(chan struct{})
	server, client := startTestPair(t, HandlerFunc(func(ctx context.Context, _ Session, _ Request) (json.RawMessage, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return nil, ctx.Err()
	}))
	defer server.Close()
	defer client.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.Call(ctx, "wait", nil, nil) }()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Call() error = %v, want context canceled", err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("handler was not cancelled")
	}
}

func TestDisconnectCancelsRequestsAndClosesSession(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	cancelled := make(chan struct{})
	closed := make(chan string, 1)
	handler := HandlerFuncs{
		HandleRequest: func(ctx context.Context, _ Session, _ Request) (json.RawMessage, error) {
			close(started)
			<-ctx.Done()
			close(cancelled)
			return nil, ctx.Err()
		},
		CloseSession: func(_ context.Context, session Session) error {
			closed <- session.ID
			return nil
		},
	}
	server, client := startTestPair(t, handler)
	defer server.Close()

	done := make(chan error, 1)
	go func() { done <- client.Call(context.Background(), "wait", nil, nil) }()
	<-started
	if err := client.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("disconnect did not cancel handler")
	}
	select {
	case session := <-closed:
		if session == "" {
			t.Fatal("closed session ID is empty")
		}
	case <-time.After(time.Second):
		t.Fatal("session cleanup was not called")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("client call remained blocked after disconnect")
	}
}

func TestInvalidProtocolDoesNotStopServer(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	server, client := startTestPair(t, HandlerFunc(func(_ context.Context, _ Session, _ Request) (json.RawMessage, error) {
		calls.Add(1)
		return json.RawMessage(`{"ok":true}`), nil
	}))
	defer server.Close()
	defer client.Close()

	if err := client.callRequest(context.Background(), Request{Version: Version + 1, Method: "ping"}, nil); !errors.Is(err, ErrProtocolVersion) {
		t.Fatalf("callRequest() error = %v, want %v", err, ErrProtocolVersion)
	}
	if err := client.Call(context.Background(), "ping", nil, nil); err != nil {
		t.Fatalf("Call() after invalid protocol error = %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("handler calls = %d, want 1", calls.Load())
	}
}

func TestServerAcceptsConcurrentClients(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	server := NewServer(HandlerFunc(func(_ context.Context, _ Session, _ Request) (json.RawMessage, error) {
		return json.RawMessage(`{"message":"pong"}`), nil
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer server.Close()
	go func() { _ = server.Serve(ctx, listener) }()

	const clientCount = 12
	var wait sync.WaitGroup
	for index := 0; index < clientCount; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			connection, dialErr := net.Dial("tcp", listener.Addr().String())
			if dialErr != nil {
				t.Errorf("Dial() error = %v", dialErr)
				return
			}
			client := NewClient(connection)
			defer client.Close()
			if callErr := client.Call(context.Background(), "ping", nil, nil); callErr != nil {
				t.Errorf("Call() error = %v", callErr)
			}
		}()
	}
	wait.Wait()
}

func startTestPair(t *testing.T, handler Handler) (*Server, *Client) {
	t.Helper()
	serverConnection, clientConnection := net.Pipe()
	server := NewServer(handler)
	go server.ServeConnection(serverConnection)
	client := NewClient(clientConnection)
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	return server, client
}
