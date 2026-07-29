package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

const (
	Version             = 1
	DefaultMaxFrameSize = 1 << 20
	cancelMethod        = "$cancel"
)

var (
	ErrFrameTooLarge   = errors.New("IPC frame is too large")
	ErrProtocolVersion = errors.New("unsupported IPC protocol version")
	ErrClientClosed    = errors.New("IPC client is closed")
)

type Request struct {
	Version         int             `json:"version"`
	RequestID       string          `json:"request_id"`
	Method          string          `json:"method"`
	TimeoutMS       int64           `json:"timeout_ms,omitempty"`
	CancelRequestID string          `json:"cancel_request_id,omitempty"`
	Payload         json.RawMessage `json:"payload,omitempty"`
}

type Response struct {
	Version   int             `json:"version"`
	RequestID string          `json:"request_id"`
	SessionID string          `json:"session_id"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	Error     *ResponseError  `json:"error,omitempty"`
}

type ResponseError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type RemoteError struct {
	Code    string
	Message string
}

func (err *RemoteError) Error() string {
	return fmt.Sprintf("remote IPC error %s: %s", err.Code, err.Message)
}

func (err *RemoteError) Is(target error) bool {
	switch target {
	case ErrProtocolVersion:
		return err.Code == "protocol_version"
	case context.Canceled:
		return err.Code == "cancelled"
	case context.DeadlineExceeded:
		return err.Code == "deadline_exceeded"
	default:
		return false
	}
}
