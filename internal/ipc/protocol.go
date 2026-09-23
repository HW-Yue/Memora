package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

const (
	// Version is the framing version: it moves when how messages are packed
	// changes.
	Version = 1
	// EngineProtocol is the version of what the daemon can be asked to do — the
	// statement surface and its method set — and it moves when that breaks
	// compatibility, not only when the framing does. A client built from a
	// different revision disagrees here, and it is the one disagreement a thin
	// client cannot work around: it does not know the syntax the daemon knows.
	EngineProtocol      = 1
	DefaultMaxFrameSize = 1 << 20
	cancelMethod        = "$cancel"
	// CodeEngineProtocol is the stable code of a refusal for engine protocol
	// skew. It is not "protocol_version": that one is the framing, and a client
	// that packs messages correctly but speaks another statement surface is a
	// different failure with a different fix. It carries the same meaning as
	// SkewedError, and errors.Is maps it back to one.
	CodeEngineProtocol = "engine_protocol"
)

var (
	ErrFrameTooLarge   = errors.New("IPC frame is too large")
	ErrProtocolVersion = errors.New("unsupported IPC protocol version")
	ErrClientClosed    = errors.New("IPC client is closed")
)

type Request struct {
	Version int `json:"version"`
	// EngineProtocol is the statement surface this request was built against.
	// It travels with the request because the daemon has to know before it runs
	// anything: a check that happens after the response is a check that happens
	// after the write committed. A client that sends nothing decodes as 0, which
	// is skew, never agreement.
	EngineProtocol  int             `json:"engine_protocol,omitempty"`
	RequestID       string          `json:"request_id"`
	Method          string          `json:"method"`
	TimeoutMS       int64           `json:"timeout_ms,omitempty"`
	CancelRequestID string          `json:"cancel_request_id,omitempty"`
	Payload         json.RawMessage `json:"payload,omitempty"`
}

type Response struct {
	Version   int    `json:"version"`
	RequestID string `json:"request_id"`
	SessionID string `json:"session_id"`
	// ServerProtocol is the engine protocol this responder speaks, stamped on
	// every response so the check costs no extra round trip and needs no state
	// file. A daemon older than this field sends nothing, which decodes as 0 and
	// reads as skew — never as agreement.
	ServerProtocol int             `json:"server_protocol,omitempty"`
	Payload        json.RawMessage `json:"payload,omitempty"`
	Error          *ResponseError  `json:"error,omitempty"`
}

// SkewedError reports a client and a daemon that do not speak the same engine
// protocol. It is a refusal rather than a warning: the client would be guessing
// at the daemon's syntax, and a guess that happens to parse is worse than a
// refusal.
type SkewedError struct {
	Client int
	Server int
}

func (err *SkewedError) Error() string {
	return fmt.Sprintf("the daemon speaks engine protocol %d, this client speaks %d", err.Server, err.Client)
}

func (err *SkewedError) Is(target error) bool {
	_, ok := target.(*SkewedError)
	return ok
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
	if _, ok := target.(*SkewedError); ok {
		return err.Code == CodeEngineProtocol
	}
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
