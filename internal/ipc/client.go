package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

type Client struct {
	connection net.Conn
	writes     sync.Mutex
	mu         sync.Mutex
	pending    map[string]chan Response
	nextID     atomic.Uint64
	done       chan struct{}
	closeOnce  sync.Once
}

func Dial(ctx context.Context, socketPath string) (*Client, error) {
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("dial Memora daemon: %w", err)
	}
	return NewClient(connection), nil
}

func NewClient(connection net.Conn) *Client {
	client := &Client{connection: connection, pending: make(map[string]chan Response), done: make(chan struct{})}
	go client.readResponses()
	return client
}

func (client *Client) Call(ctx context.Context, method string, payload, result any) error {
	request := Request{Version: Version, Method: method}
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encode IPC request payload: %w", err)
		}
		request.Payload = encoded
	}
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining > 0 {
			request.TimeoutMS = durationMillisecondsCeil(remaining)
		}
	}
	return client.callRequest(ctx, request, result)
}

func durationMillisecondsCeil(duration time.Duration) int64 {
	milliseconds := duration / time.Millisecond
	if duration%time.Millisecond != 0 {
		milliseconds++
	}
	return max(1, int64(milliseconds))
}

func (client *Client) callRequest(ctx context.Context, request Request, result any) error {
	if request.RequestID == "" {
		request.RequestID = fmt.Sprintf("request-%d", client.nextID.Add(1))
	}
	responses := make(chan Response, 1)
	client.mu.Lock()
	select {
	case <-client.done:
		client.mu.Unlock()
		return ErrClientClosed
	default:
	}
	client.pending[request.RequestID] = responses
	client.mu.Unlock()
	if err := client.write(request); err != nil {
		client.removePending(request.RequestID)
		return err
	}
	select {
	case response := <-responses:
		if response.Error != nil {
			return &RemoteError{Code: response.Error.Code, Message: response.Error.Message}
		}
		if result != nil && len(response.Payload) > 0 {
			if err := json.Unmarshal(response.Payload, result); err != nil {
				return fmt.Errorf("decode IPC response payload: %w", err)
			}
		}
		return nil
	case <-ctx.Done():
		client.removePending(request.RequestID)
		_ = client.write(Request{Version: Version, RequestID: fmt.Sprintf("cancel-%d", client.nextID.Add(1)), Method: cancelMethod, CancelRequestID: request.RequestID})
		return ctx.Err()
	case <-client.done:
		return ErrClientClosed
	}
}

func (client *Client) Close() error {
	var err error
	client.closeOnce.Do(func() {
		err = client.connection.Close()
		close(client.done)
	})
	return err
}

func (client *Client) write(request Request) error {
	client.writes.Lock()
	defer client.writes.Unlock()
	return WriteFrame(client.connection, request, DefaultMaxFrameSize)
}

func (client *Client) readResponses() {
	for {
		var response Response
		if err := ReadFrame(client.connection, &response, DefaultMaxFrameSize); err != nil {
			_ = client.Close()
			return
		}
		client.mu.Lock()
		responses := client.pending[response.RequestID]
		delete(client.pending, response.RequestID)
		client.mu.Unlock()
		if responses != nil {
			responses <- response
		}
	}
}

func (client *Client) removePending(requestID string) {
	client.mu.Lock()
	delete(client.pending, requestID)
	client.mu.Unlock()
}
