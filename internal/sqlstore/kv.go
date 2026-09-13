package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"sync"

	"github.com/HW-Yue/Memora/internal/store"
)

// KV is a bucketed key-value store in one SQLite table. It carries the
// workflow state the host commands keep — host inputs, worthiness decisions,
// assimilation receipts, feedback, conversation checkpoints, the audit log —
// under its own namespace, so those services run unchanged on SQLite.
type KV struct {
	db        *DB
	namespace string
}

const kvSchema = `CREATE TABLE IF NOT EXISTS mem_kv (
	namespace TEXT NOT NULL,
	bucket TEXT NOT NULL,
	key TEXT NOT NULL,
	value BLOB NOT NULL,
	PRIMARY KEY(namespace, bucket, key)
)`

// KV returns the key-value store for namespace.
func (db *DB) KV(namespace string) *KV { return &KV{db: db, namespace: namespace} }

func (kv *KV) Close() error { return nil }

func (kv *KV) Begin(ctx context.Context, mode store.Mode) (store.Tx, error) {
	if mode == store.ReadWrite {
		t, err := kv.db.begin(ctx)
		if err != nil {
			return nil, err
		}
		return &kvTx{kv: kv, sql: t.sql, write: true, release: t.db.write.Unlock}, nil
	}
	handle, err := kv.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	return &kvTx{kv: kv, sql: handle, release: func() {}}, nil
}

type kvTx struct {
	kv      *KV
	sql     *sql.Tx
	write   bool
	mu      sync.Mutex
	closed  bool
	release func()
}

func (tx *kvTx) check(write bool) error {
	if tx.closed {
		return store.ErrTxClosed
	}
	if write && !tx.write {
		return store.ErrReadOnly
	}
	return nil
}

func (tx *kvTx) Get(ctx context.Context, bucket, key string) ([]byte, error) {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if err := tx.check(false); err != nil {
		return nil, err
	}
	var value []byte
	err := tx.sql.QueryRowContext(ctx, `SELECT value FROM mem_kv WHERE namespace = ? AND bucket = ? AND key = ?`,
		tx.kv.namespace, bucket, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return value, err
}

func (tx *kvTx) Scan(ctx context.Context, bucket string) ([]store.Entry, error) {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if err := tx.check(false); err != nil {
		return nil, err
	}
	rows, err := tx.sql.QueryContext(ctx, `SELECT key, value FROM mem_kv WHERE namespace = ? AND bucket = ? ORDER BY key`,
		tx.kv.namespace, bucket)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := []store.Entry{}
	for rows.Next() {
		var entry store.Entry
		if err := rows.Scan(&entry.Key, &entry.Value); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (tx *kvTx) Put(ctx context.Context, bucket, key string, value []byte) error {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if err := tx.check(true); err != nil {
		return err
	}
	if value == nil {
		value = []byte{}
	}
	_, err := tx.sql.ExecContext(ctx, `INSERT INTO mem_kv(namespace, bucket, key, value) VALUES (?, ?, ?, ?)
		ON CONFLICT(namespace, bucket, key) DO UPDATE SET value = excluded.value`, tx.kv.namespace, bucket, key, value)
	return err
}

func (tx *kvTx) Delete(ctx context.Context, bucket, key string) error {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if err := tx.check(true); err != nil {
		return err
	}
	_, err := tx.sql.ExecContext(ctx, `DELETE FROM mem_kv WHERE namespace = ? AND bucket = ? AND key = ?`, tx.kv.namespace, bucket, key)
	return err
}

func (tx *kvTx) Commit() error {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if tx.closed {
		return store.ErrTxClosed
	}
	tx.closed = true
	defer tx.release()
	return tx.sql.Commit()
}

func (tx *kvTx) Rollback() error {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if tx.closed {
		return nil
	}
	tx.closed = true
	defer tx.release()
	return tx.sql.Rollback()
}
