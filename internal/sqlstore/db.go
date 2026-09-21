// Package store is Memora's storage backend on SQLite.
//
// Everything Memora keeps is an ordinary SQLite table (ADR-0011): the Catalog,
// every data table, its history table, its semantic-route table, the change
// log, and configuration. There is no MVCC of Memora's own: writers are
// serialised, readers see the last commit. Keyword/vector recall is not in
// this kernel; that architecture is planned separately.
package sqlstore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/HW-Yue/Memora/internal/change"
	"github.com/HW-Yue/Memora/internal/result"
)

// FileName is the single SQLite file an Instance keeps under its data directory.
const FileName = "memora.db"

type Options struct {
	Now func() time.Time
	// CheckInvariants asserts the mount invariant before every write commits.
	// Tests turn it on so a new path fails at the commit that broke it; a
	// production Instance leaves it off and relies on the doctor command.
	CheckInvariants bool
}

type DB struct {
	sql             *sql.DB
	write           sync.Mutex
	now             func() time.Time
	checkInvariants bool
}

// Error is a storage failure with a stable result code.
type Error struct {
	Code    result.Code
	Message string
}

func (err *Error) Error() string      { return err.Message }
func (err *Error) StableCode() string { return string(err.Code) }

func fail(code result.Code, format string, arguments ...any) error {
	return &Error{Code: code, Message: fmt.Sprintf(format, arguments...)}
}

// Open opens (creating if needed) the Instance database at path.
func Open(path string, options Options) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	dsn := "file:" + path + "?_journal_mode=WAL&_busy_timeout=10000&_foreign_keys=on&_synchronous=NORMAL"
	handle, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	handle.SetMaxOpenConns(8)
	now := options.Now
	if now == nil {
		now = time.Now
	}
	db := &DB{sql: handle, now: func() time.Time { return now().UTC() }, checkInvariants: options.CheckInvariants}
	if err := db.migrate(context.Background()); err != nil {
		_ = handle.Close()
		return nil, err
	}
	return db, nil
}

func (db *DB) Close() error {
	return db.sql.Close()
}

// SQL exposes the handle for maintenance tooling and tests.
func (db *DB) SQL() *sql.DB { return db.sql }

const schema = `
CREATE TABLE IF NOT EXISTS mem_databases (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL UNIQUE COLLATE NOCASE,
	body TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS mem_tables (
	id TEXT PRIMARY KEY,
	database_id TEXT NOT NULL REFERENCES mem_databases(id),
	name TEXT NOT NULL COLLATE NOCASE,
	role TEXT NOT NULL DEFAULT 'data',
	owner_table_id TEXT,
	router_root_id TEXT,
	body TEXT NOT NULL,
	UNIQUE(database_id, name)
);
CREATE TABLE IF NOT EXISTS mem_changes (
	sequence INTEGER PRIMARY KEY AUTOINCREMENT,
	transaction_id TEXT NOT NULL UNIQUE,
	database_id TEXT NOT NULL,
	committed_at TEXT NOT NULL,
	body TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS mem_change_scopes (
	database_id TEXT NOT NULL,
	sequence INTEGER NOT NULL,
	PRIMARY KEY(database_id, sequence)
);
CREATE TABLE IF NOT EXISTS mem_archive (
	sequence INTEGER PRIMARY KEY AUTOINCREMENT,
	archive_id TEXT NOT NULL UNIQUE,
	database_id TEXT NOT NULL,
	table_id TEXT NOT NULL,
	row_id TEXT NOT NULL,
	revision INTEGER NOT NULL,
	deleted_at TEXT NOT NULL,
	actor TEXT NOT NULL,
	source TEXT NOT NULL,
	reason TEXT NOT NULL,
	path_json TEXT NOT NULL,
	row_json TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS mem_archive_row ON mem_archive(table_id, row_id);
CREATE TABLE IF NOT EXISTS mem_repairs (
	database_id TEXT NOT NULL,
	table_id TEXT NOT NULL,
	row_id TEXT NOT NULL,
	counterpart_table_id TEXT NOT NULL,
	counterpart_row_id TEXT NOT NULL,
	reason TEXT NOT NULL,
	queued_at TEXT NOT NULL,
	PRIMARY KEY (table_id, row_id, counterpart_table_id, counterpart_row_id)
);
CREATE TABLE IF NOT EXISTS mem_route_index (
	route_id TEXT PRIMARY KEY,
	table_id TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS mem_counters (
	name TEXT PRIMARY KEY,
	value INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS mem_config (
	key TEXT NOT NULL,
	revision INTEGER NOT NULL,
	body TEXT NOT NULL,
	PRIMARY KEY(key, revision)
);
CREATE TABLE IF NOT EXISTS mem_traces (
	trace_id TEXT PRIMARY KEY,
	database_id TEXT NOT NULL,
	sequence INTEGER NOT NULL,
	body TEXT NOT NULL
);
`

func (db *DB) migrate(ctx context.Context) error {
	if _, err := db.sql.ExecContext(ctx, schema+";"+kvSchema); err != nil {
		return fmt.Errorf("create Memora schema: %w", err)
	}
	return nil
}

// tx is one serialised write transaction, or a read over the last commit.
type tx struct {
	db     *DB
	sql    *sql.Tx
	now    time.Time
	change *changeDraft
}

type queryer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (t *tx) q() queryer { return t.sql }

// begin opens a write transaction. The Go mutex is what makes writers serial;
// SQLite's immediate lock would do it too, but would turn contention into
// busy-timeouts instead of a queue.
func (db *DB) begin(ctx context.Context) (*tx, error) {
	db.write.Lock()
	handle, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		db.write.Unlock()
		return nil, err
	}
	return &tx{db: db, sql: handle, now: db.now()}, nil
}

func (t *tx) commit(ctx context.Context) error {
	defer t.db.write.Unlock()
	if t.db.checkInvariants {
		if err := t.requireMountInvariant(ctx); err != nil {
			_ = t.sql.Rollback()
			return err
		}
	}
	if err := t.flushChange(ctx); err != nil {
		_ = t.sql.Rollback()
		return err
	}
	if err := t.sql.Commit(); err != nil {
		return err
	}
	return nil
}

func (t *tx) rollback() error {
	defer t.db.write.Unlock()
	return t.sql.Rollback()
}

// update runs fn in its own write transaction.
func (db *DB) update(ctx context.Context, fn func(*tx) error) error {
	t, err := db.begin(ctx)
	if err != nil {
		return err
	}
	if metadata, ok := change.MetadataFrom(ctx); ok {
		t.claimAttribution(metadata)
	}
	if err := fn(t); err != nil {
		_ = t.rollback()
		return err
	}
	return t.commit(ctx)
}

// view runs fn against the last commit. It uses a deferred read transaction so
// one call sees one consistent state.
func (db *DB) view(ctx context.Context, fn func(*tx) error) error {
	handle, err := db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer func() { _ = handle.Rollback() }()
	return fn(&tx{db: db, sql: handle, now: db.now()})
}

func newID(prefix string) string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(value[:])
}

func (t *tx) nextCounter(ctx context.Context, name string) (uint64, error) {
	var value uint64
	err := t.q().QueryRowContext(ctx,
		`INSERT INTO mem_counters(name, value) VALUES (?, 1)
		 ON CONFLICT(name) DO UPDATE SET value = value + 1 RETURNING value`, name).Scan(&value)
	return value, err
}

func (t *tx) peekCounter(ctx context.Context, name string) (uint64, error) {
	var value uint64
	err := t.q().QueryRowContext(ctx, `SELECT value FROM mem_counters WHERE name = ?`, name).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return value, err
}

func encodeJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func decodeJSON(text string, target any) error {
	if text == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(text), target); err != nil {
		return fail(result.CodeInternal, "stored value is corrupt: %v", err)
	}
	return nil
}

func canonical(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

func quoteIdent(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func parseTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}
