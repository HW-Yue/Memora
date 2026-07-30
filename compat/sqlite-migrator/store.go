package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"sync"

	"github.com/HW-Yue/Memora/internal/store"
	_ "modernc.org/sqlite"
)

type sqliteStore struct{ database *sql.DB }

func openSQLite(path string) (store.Store, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	dsn := url.URL{Scheme: "file", Path: absolute}
	database, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return nil, err
	}
	if err := database.Ping(); err != nil {
		_ = database.Close()
		return nil, err
	}
	return &sqliteStore{database: database}, nil
}

func (database *sqliteStore) Begin(ctx context.Context, mode store.Mode) (store.Tx, error) {
	tx, err := database.database.BeginTx(ctx, &sql.TxOptions{ReadOnly: mode == store.ReadOnly})
	if err != nil {
		return nil, err
	}
	return &sqliteTx{tx: tx, mode: mode}, nil
}

func (database *sqliteStore) Close() error { return database.database.Close() }

type sqliteTx struct {
	mu   sync.Mutex
	tx   *sql.Tx
	mode store.Mode
	done bool
}

func (tx *sqliteTx) Get(ctx context.Context, bucket, key string) ([]byte, error) {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if tx.done {
		return nil, store.ErrTxClosed
	}
	var value []byte
	err := tx.tx.QueryRowContext(ctx, "SELECT value FROM memora_kv WHERE bucket = ? AND key = ?", bucket, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return append([]byte(nil), value...), err
}

func (tx *sqliteTx) Scan(ctx context.Context, bucket string) ([]store.Entry, error) {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if tx.done {
		return nil, store.ErrTxClosed
	}
	rows, err := tx.tx.QueryContext(ctx, "SELECT key, value FROM memora_kv WHERE bucket = ? ORDER BY key", bucket)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []store.Entry{}
	for rows.Next() {
		var entry store.Entry
		if err := rows.Scan(&entry.Key, &entry.Value); err != nil {
			return nil, err
		}
		result = append(result, entry)
	}
	return result, rows.Err()
}

func (tx *sqliteTx) Put(context.Context, string, string, []byte) error {
	return fmt.Errorf("compatibility source is read-only")
}

func (tx *sqliteTx) Delete(context.Context, string, string) error {
	return fmt.Errorf("compatibility source is read-only")
}

func (tx *sqliteTx) Commit() error {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if tx.done {
		return store.ErrTxClosed
	}
	tx.done = true
	return tx.tx.Commit()
}

func (tx *sqliteTx) Rollback() error {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if tx.done {
		return store.ErrTxClosed
	}
	tx.done = true
	err := tx.tx.Rollback()
	if errors.Is(err, sql.ErrTxDone) {
		return nil
	}
	return err
}
