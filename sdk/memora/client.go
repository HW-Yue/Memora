package memora

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/HW-Yue/Memora/internal/daemon"
	"github.com/HW-Yue/Memora/internal/ipc"
)

var (
	ErrInvalidTransport = errors.New("memora SDK transport is required")
	ErrClientClosed     = errors.New("memora SDK client is closed")
)

type Transport interface {
	Call(context.Context, string, any, any) error
	Close() error
}

type DialOptions struct {
	DataDir string
}

type Client struct {
	transport Transport
	closed    atomic.Bool
	closeOnce sync.Once
	closeErr  error
}

type RemoteError struct {
	Code    string
	Message string
}

func (err *RemoteError) Error() string {
	return fmt.Sprintf("Memora daemon error %s: %s", err.Code, err.Message)
}

func Dial(ctx context.Context, options DialOptions) (*Client, error) {
	if !filepath.IsAbs(options.DataDir) || filepath.Clean(options.DataDir) != options.DataDir {
		return nil, fmt.Errorf("%w: data directory must be absolute and normalized", ErrInvalidRequest)
	}
	socket, err := daemon.SocketPath(options.DataDir)
	if err != nil {
		return nil, err
	}
	transport, err := ipc.Dial(ctx, socket)
	if err != nil {
		return nil, err
	}
	return NewClient(transport)
}

func NewClient(transport Transport) (*Client, error) {
	if transport == nil {
		return nil, ErrInvalidTransport
	}
	return &Client{transport: transport}, nil
}

func (client *Client) Execute(ctx context.Context, request Request) (Envelope, error) {
	if client == nil || client.transport == nil || client.closed.Load() {
		return Envelope{}, ErrClientClosed
	}
	if err := request.Validate(); err != nil {
		return Envelope{}, err
	}
	payload := struct {
		Source     string           `json:"source"`
		Statements []StatementInput `json:"statements"`
	}{Source: request.Source, Statements: request.Statements}
	var envelope Envelope
	if err := client.transport.Call(ctx, "msql.execute", payload, &envelope); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return Envelope{}, err
		}
		var remote *ipc.RemoteError
		if errors.As(err, &remote) {
			return Envelope{}, &RemoteError{Code: remote.Code, Message: remote.Message}
		}
		return Envelope{}, err
	}
	if err := envelope.Validate(); err != nil {
		return Envelope{}, err
	}
	return envelope, nil
}

func (client *Client) Close() error {
	if client == nil || client.transport == nil {
		return nil
	}
	client.closeOnce.Do(func() {
		client.closed.Store(true)
		client.closeErr = client.transport.Close()
	})
	return client.closeErr
}
