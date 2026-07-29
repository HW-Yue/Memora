package store

import (
	"context"
	"errors"
)

var (
	ErrNotFound = errors.New("store entry not found")
	ErrReadOnly = errors.New("store transaction is read-only")
	ErrTxClosed = errors.New("store transaction is closed")
)

type Mode uint8

const (
	ReadOnly Mode = iota
	ReadWrite
)

type Store interface {
	Begin(context.Context, Mode) (Tx, error)
	Close() error
}

type Entry struct {
	Key   string
	Value []byte
}

type Tx interface {
	Get(context.Context, string, string) ([]byte, error)
	Scan(context.Context, string) ([]Entry, error)
	Put(context.Context, string, string, []byte) error
	Delete(context.Context, string, string) error
	Commit() error
	Rollback() error
}
