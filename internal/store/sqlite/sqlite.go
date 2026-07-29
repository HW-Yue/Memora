package sqlite

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

const schema = `
CREATE TABLE IF NOT EXISTS memora_kv (
    bucket TEXT NOT NULL,
    key TEXT NOT NULL,
    value BLOB NOT NULL,
    PRIMARY KEY (bucket, key)
) WITHOUT ROWID;
`

type Database struct {
	db *sql.DB
}

func Open(path string) (store.Store, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve SQLite path: %w", err)
	}
	dsn := sqliteDSN(absolute)
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open SQLite prototype store: %w", err)
	}
	database.SetMaxOpenConns(8)
	database.SetMaxIdleConns(8)

	if err := database.Ping(); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("ping SQLite prototype store: %w", err)
	}
	if _, err := database.Exec(schema); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("initialize SQLite prototype store: %w", err)
	}
	return &Database{db: database}, nil
}

func sqliteDSN(path string) string {
	dsn := url.URL{Scheme: "file", Path: path}
	query := url.Values{}
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "journal_mode(WAL)")
	query.Add("_pragma", "synchronous(FULL)")
	dsn.RawQuery = query.Encode()
	return dsn.String()
}

func (database *Database) Begin(ctx context.Context, mode store.Mode) (store.Tx, error) {
	if mode != store.ReadOnly && mode != store.ReadWrite {
		return nil, fmt.Errorf("begin SQLite transaction: unknown mode %d", mode)
	}
	tx, err := database.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: mode == store.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("begin SQLite transaction: %w", err)
	}
	return &transaction{tx: tx, mode: mode}, nil
}

func (database *Database) Close() error {
	if err := database.db.Close(); err != nil {
		return fmt.Errorf("close SQLite prototype store: %w", err)
	}
	return nil
}

type transaction struct {
	mu   sync.Mutex
	tx   *sql.Tx
	mode store.Mode
	done bool
}

func (tx *transaction) Get(ctx context.Context, bucket, key string) ([]byte, error) {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if tx.done {
		return nil, store.ErrTxClosed
	}
	var value []byte
	err := tx.tx.QueryRowContext(ctx,
		"SELECT value FROM memora_kv WHERE bucket = ? AND key = ?",
		bucket, key,
	).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read SQLite store entry: %w", err)
	}
	return append([]byte(nil), value...), nil
}

func (tx *transaction) Put(ctx context.Context, bucket, key string, value []byte) error {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if tx.done {
		return store.ErrTxClosed
	}
	if tx.mode != store.ReadWrite {
		return store.ErrReadOnly
	}
	_, err := tx.tx.ExecContext(ctx, `
INSERT INTO memora_kv (bucket, key, value) VALUES (?, ?, ?)
ON CONFLICT (bucket, key) DO UPDATE SET value = excluded.value
`, bucket, key, append([]byte(nil), value...))
	if err != nil {
		return fmt.Errorf("write SQLite store entry: %w", err)
	}
	return nil
}

func (tx *transaction) Delete(ctx context.Context, bucket, key string) error {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if tx.done {
		return store.ErrTxClosed
	}
	if tx.mode != store.ReadWrite {
		return store.ErrReadOnly
	}
	if _, err := tx.tx.ExecContext(ctx,
		"DELETE FROM memora_kv WHERE bucket = ? AND key = ?",
		bucket, key,
	); err != nil {
		return fmt.Errorf("delete SQLite store entry: %w", err)
	}
	return nil
}

func (tx *transaction) Commit() error {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if tx.done {
		return store.ErrTxClosed
	}
	tx.done = true
	if err := tx.tx.Commit(); err != nil {
		return fmt.Errorf("commit SQLite transaction: %w", err)
	}
	return nil
}

func (tx *transaction) Rollback() error {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if tx.done {
		return store.ErrTxClosed
	}
	tx.done = true
	if err := tx.tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		return fmt.Errorf("rollback SQLite transaction: %w", err)
	}
	return nil
}
