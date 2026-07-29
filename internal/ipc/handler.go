package ipc

import (
	"context"
	"encoding/json"
)

type Session struct {
	ID string
}

type Handler interface {
	Handle(context.Context, Session, Request) (json.RawMessage, error)
	SessionClosed(context.Context, Session) error
}

type HandlerFunc func(context.Context, Session, Request) (json.RawMessage, error)

func (function HandlerFunc) Handle(ctx context.Context, session Session, request Request) (json.RawMessage, error) {
	return function(ctx, session, request)
}

func (HandlerFunc) SessionClosed(context.Context, Session) error { return nil }

type HandlerFuncs struct {
	HandleRequest func(context.Context, Session, Request) (json.RawMessage, error)
	CloseSession  func(context.Context, Session) error
}

func (handler HandlerFuncs) Handle(ctx context.Context, session Session, request Request) (json.RawMessage, error) {
	return handler.HandleRequest(ctx, session, request)
}

func (handler HandlerFuncs) SessionClosed(ctx context.Context, session Session) error {
	if handler.CloseSession == nil {
		return nil
	}
	return handler.CloseSession(ctx, session)
}
