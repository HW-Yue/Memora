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
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/HW-Yue/Memora/internal/change"
	"github.com/HW-Yue/Memora/internal/result"
)

// FileName is the single SQLite file an Instance keeps under its data directory.
const FileName = "memora.db"

// fileSchemaVersion is the shape of the file itself, recorded in SQLite's own
// user_version so it survives everything and is written atomically with the
// change it describes.
//
// The guard it enables only protects binaries that have it: one released before
// this cannot be taught to look. What it buys is that every binary from now on
// refuses to open an Instance a newer one has written, instead of migrating it
// back to its own shape — which is not a parse error but a silent rewrite. It was
// measured: a release 253 commits old ran init and doctor against a newer
// Instance and wrote its own tables into it without complaint.
//
// Additive columns do not need a bump (the additive migration handles them); this
// moves when an older binary could no longer read the file correctly.
//
// It is 2 because the keyword index moved from FTS5's trigram tokenizer to the
// pair token stream recallTokens produces. That change lives entirely in a derived
// structure, but it is still a change of file: a binary that knows only version 1
// would keep its own trigram index, answer nothing for a two-character Chinese
// word, and report that as "not found" rather than as a version it cannot read.
const fileSchemaVersion = 2

// fileSchemaVersionKeywordIndex is the version at which the keyword index changed
// tokenizer. A file below it is reindexed from its payloads on open.
const fileSchemaVersionKeywordIndex = 2

type Options struct {
	Now func() time.Time
	// CheckInvariants asserts the mount invariant before every write commits.
	// Tests turn it on so a new path fails at the commit that broke it; a
	// production Instance leaves it off and relies on the doctor command.
	CheckInvariants bool
	// TransactionIdle is how long an explicit transaction may sit untouched
	// before it is rolled back. An explicit transaction holds the write lock
	// from BEGIN to COMMIT, deliberately and across requests, so a caller that
	// walks away from one would otherwise hold it until the process ends. Zero
	// selects defaultTransactionIdle; a negative value turns the timer off.
	TransactionIdle time.Duration
}

// defaultTransactionIdle is long enough for a person reading a receipt between
// two statements and short enough that a forgotten BEGIN is not a permanent
// one.
const defaultTransactionIdle = 5 * time.Minute

type DB struct {
	sql             *sql.DB
	write           writeLock
	now             func() time.Time
	checkInvariants bool
	transactionIdle time.Duration
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
	// The vector module is an auto-extension: it only reaches connections opened
	// after registration, so this comes first.
	registerVectorModule()
	dsn := "file:" + path + "?_journal_mode=WAL&_busy_timeout=10000&_foreign_keys=on&_synchronous=NORMAL"
	handle, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	handle.SetMaxOpenConns(8)
	if err := requireVectorModule(context.Background(), handle); err != nil {
		_ = handle.Close()
		return nil, err
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	idle := options.TransactionIdle
	if idle == 0 {
		idle = defaultTransactionIdle
	}
	db := &DB{
		sql: handle, write: newWriteLock(), now: func() time.Time { return now().UTC() },
		checkInvariants: options.CheckInvariants, transactionIdle: idle,
	}
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
	body TEXT NOT NULL,
	-- The vector identity of this Database, set by the first embedding it
	-- accepts and never changed in place afterwards. It is a column here rather
	-- than a row in mem_config because it is an identity, not a setting: the
	-- config table is a revisioned channel for things that may change, and an
	-- identity that can be edited in place is one that will be.
	embedding_model TEXT NOT NULL DEFAULT '',
	embedding_dimensions INTEGER NOT NULL DEFAULT 0,
	embedding_locked_at TEXT NOT NULL DEFAULT '',
	-- The rekey window. Non-empty embedding_rekey_at means this Database is
	-- between identities: the derived index is gone, the units are being
	-- released, and every vector path must refuse until the pass that clears the
	-- last unit writes the new identity and empties this column. It is stored
	-- rather than derived because a crash must not lose the fact that the index
	-- and the identity no longer agree.
	embedding_rekey_at TEXT NOT NULL DEFAULT '',
	embedding_rekey_model TEXT NOT NULL DEFAULT '',
	embedding_rekey_dimensions INTEGER NOT NULL DEFAULT 0
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
CREATE TABLE IF NOT EXISTS mem_recall_units (
	unit_no INTEGER PRIMARY KEY AUTOINCREMENT,
	route_id TEXT NOT NULL UNIQUE,
	database_id TEXT NOT NULL,
	table_id TEXT NOT NULL,
	row_id TEXT NOT NULL,
	revision INTEGER NOT NULL,
	content_hash TEXT NOT NULL,
	payload TEXT NOT NULL,
	-- The token stream the keyword index holds, derived from payload by
	-- recallTokens. It is not the text: nothing reads it back as prose, and
	-- rebuilding it never touches a Row or the vectors that describe it.
	payload_index TEXT NOT NULL,
	embedding_model TEXT NOT NULL DEFAULT '',
	embedding_dimensions INTEGER NOT NULL DEFAULT 0,
	embedded_at TEXT NOT NULL DEFAULT '',
	-- The truth: the bytes a host computed, and the hash of the text it
	-- computed them from. The second is what separates "this unit has a vector"
	-- from "this unit has a vector for the text it currently holds" — without
	-- it, embedded_at could only say a vector was once made, and comparing it
	-- with updated_at would be a clock-dependent guess.
	embedding BLOB,
	embedded_content_hash TEXT NOT NULL DEFAULT '',
	updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS mem_recall_units_row ON mem_recall_units(table_id, row_id);
-- The vector index is a derived structure, so which (Table) has one is recorded
-- rather than inferred: sqlite_master cannot answer it without scanning for a
-- name prefix, and a prefix scan also matches the virtual table's shadow tables
-- (_chunks, _rowids, _vector_chunksNN). This is a registry of derived indexes,
-- not a second copy of any vector.
CREATE TABLE IF NOT EXISTS mem_recall_vec_indexes (
	table_id TEXT PRIMARY KEY,
	database_id TEXT NOT NULL,
	model TEXT NOT NULL,
	dimensions INTEGER NOT NULL,
	created_at TEXT NOT NULL
);
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

// keywordIndexDDL is kept out of the schema block above because it is also what
// the rebuild writes: an Instance created today and one reindexed later must end
// up with the same declaration, and two copies of it would eventually differ.
//
// Keyword recall is an external-content FTS5 index over the units: the payload is
// stored once, and unit_no is the stable rowid the index points at. The indexed
// column holds the token stream recallTokens produced rather than the text, so
// the tokenizer here only has to split on spaces — unicode61 does exactly that,
// and folds ASCII case while it is at it. The text-level tokenizer that used to
// be named here could not answer a two-character Chinese word at all.
const keywordIndexDDL = `CREATE VIRTUAL TABLE IF NOT EXISTS mem_recall_fts USING fts5(
	payload_index,
	content='mem_recall_units',
	content_rowid='unit_no',
	tokenize='unicode61'
);`

func (db *DB) migrate(ctx context.Context) error {
	// Before anything is created or altered: an Instance written by a newer
	// binary is not ours to migrate backwards.
	found := 0
	if err := db.sql.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&found); err != nil {
		return fmt.Errorf("read file schema version: %w", err)
	}
	if found > fileSchemaVersion {
		return fmt.Errorf(
			"this instance was written by a newer Memora (file schema %d, this binary understands %d): "+
				"upgrade the binary instead of opening it with this one", found, fileSchemaVersion)
	}
	if _, err := db.sql.ExecContext(ctx, schema+";"+kvSchema+";"+keywordIndexDDL); err != nil {
		return fmt.Errorf("create Memora schema: %w", err)
	}
	// CREATE TABLE IF NOT EXISTS leaves an Instance created by an earlier
	// version with the earlier columns, so additive columns are added here.
	// Only additive changes belong in this list: anything that would have to
	// rewrite rows is a rebuild, not a migration.
	additive := []struct{ table, column, ddl string }{
		{"mem_databases", "embedding_model", "ALTER TABLE mem_databases ADD COLUMN embedding_model TEXT NOT NULL DEFAULT ''"},
		{"mem_databases", "embedding_dimensions", "ALTER TABLE mem_databases ADD COLUMN embedding_dimensions INTEGER NOT NULL DEFAULT 0"},
		{"mem_databases", "embedding_locked_at", "ALTER TABLE mem_databases ADD COLUMN embedding_locked_at TEXT NOT NULL DEFAULT ''"},
		{"mem_databases", "embedding_rekey_at", "ALTER TABLE mem_databases ADD COLUMN embedding_rekey_at TEXT NOT NULL DEFAULT ''"},
		{"mem_databases", "embedding_rekey_model", "ALTER TABLE mem_databases ADD COLUMN embedding_rekey_model TEXT NOT NULL DEFAULT ''"},
		{"mem_databases", "embedding_rekey_dimensions", "ALTER TABLE mem_databases ADD COLUMN embedding_rekey_dimensions INTEGER NOT NULL DEFAULT 0"},
		{"mem_recall_units", "embedding", "ALTER TABLE mem_recall_units ADD COLUMN embedding BLOB"},
		{"mem_recall_units", "embedded_content_hash", "ALTER TABLE mem_recall_units ADD COLUMN embedded_content_hash TEXT NOT NULL DEFAULT ''"},
	}
	for _, change := range additive {
		present, err := db.hasColumn(ctx, change.table, change.column)
		if err != nil {
			return err
		}
		if present {
			continue
		}
		if _, err := db.sql.ExecContext(ctx, change.ddl); err != nil {
			return fmt.Errorf("add %s.%s: %w", change.table, change.column, err)
		}
	}
	// A file written before the bigram index is holding a keyword index built on
	// a tokenizer that cannot answer what this one can, so it is re-derived here.
	// Nothing else moves: the payload is untouched, which is also what the vectors
	// were made from, so no Row is rewritten and nothing needs re-embedding.
	if found < fileSchemaVersionKeywordIndex {
		if err := db.rebuildKeywordIndex(ctx); err != nil {
			return err
		}
	}
	if _, err := db.sql.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", fileSchemaVersion)); err != nil {
		return fmt.Errorf("record file schema version: %w", err)
	}
	return nil
}

// rebuildKeywordIndex drops the derived keyword index and writes it again from
// the units it indexes. It runs in one transaction — an index half built from the
// old tokenizer and half from the new one would answer with gaps that look like
// missing data — and it is idempotent, so a file whose version was not recorded
// before the process stopped is simply rebuilt on the next open.
func (db *DB) rebuildKeywordIndex(ctx context.Context) error {
	transaction, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("rebuild keyword index: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	if _, err := transaction.ExecContext(ctx, `DROP TABLE IF EXISTS mem_recall_fts`); err != nil {
		return fmt.Errorf("rebuild keyword index: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, keywordIndexDDL); err != nil {
		return fmt.Errorf("rebuild keyword index: %w", err)
	}
	rows, err := transaction.QueryContext(ctx, `SELECT unit_no, payload FROM mem_recall_units ORDER BY unit_no`)
	if err != nil {
		return fmt.Errorf("rebuild keyword index: %w", err)
	}
	type unit struct {
		unitNo  int64
		payload string
	}
	units := []unit{}
	for rows.Next() {
		one := unit{}
		if err := rows.Scan(&one.unitNo, &one.payload); err != nil {
			_ = rows.Close()
			return fmt.Errorf("rebuild keyword index: %w", err)
		}
		units = append(units, one)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("rebuild keyword index: %w", err)
	}
	_ = rows.Close()
	for _, one := range units {
		indexed := recallIndexText(foldRecallText(one.payload))
		if _, err := transaction.ExecContext(ctx,
			`UPDATE mem_recall_units SET payload_index = ? WHERE unit_no = ?`, indexed, one.unitNo); err != nil {
			return fmt.Errorf("rebuild keyword index: %w", err)
		}
		if _, err := transaction.ExecContext(ctx,
			`INSERT INTO mem_recall_fts(rowid, payload_index) VALUES (?, ?)`, one.unitNo, indexed); err != nil {
			return fmt.Errorf("rebuild keyword index: %w", err)
		}
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("rebuild keyword index: %w", err)
	}
	return nil
}

func (db *DB) hasColumn(ctx context.Context, table, column string) (bool, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		name := ""
		if err := rows.Scan(&name); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
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

// begin opens a write transaction. The write lock is what makes writers serial;
// SQLite's immediate lock would do it too, but would turn contention into
// busy-timeouts instead of a queue.
func (db *DB) begin(ctx context.Context) (*tx, error) {
	if err := db.write.acquire(ctx); err != nil {
		return nil, err
	}
	handle, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		db.write.Unlock()
		return nil, err
	}
	return &tx{db: db, sql: handle, now: db.now()}, nil
}

// writeLock serialises writers. It is a channel and not a sync.Mutex because a
// wait on a mutex cannot be given up: a caller that ends up behind a writer
// that is never coming back waits for the process to die. Here the wait ends
// when the caller's context does, or at maxWriteWait, and a deadlock the caller
// could do nothing about becomes an error the caller can report.
type writeLock struct{ slot chan struct{} }

func newWriteLock() writeLock { return writeLock{slot: make(chan struct{}, 1)} }

// maxWriteWait bounds a wait whose context carries no deadline of its own. It
// is long: a legitimate writer queued behind a long write must still get
// through, and this is only the line past which waiting has stopped being
// queueing and started being a hang.
const maxWriteWait = 30 * time.Second

func (lock writeLock) acquire(ctx context.Context) error {
	select {
	case lock.slot <- struct{}{}:
		return nil
	default:
	}
	timer := time.NewTimer(maxWriteWait)
	defer timer.Stop()
	select {
	case lock.slot <- struct{}{}:
		return nil
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.Canceled) {
			return fail(result.CodeCancelled, "waiting for the write lock: %v", ctx.Err())
		}
		return fail(result.CodeDeadlineExceeded, "waiting for the write lock: %v", ctx.Err())
	case <-timer.C:
		return fail(result.CodeDeadlineExceeded, "waiting for the write lock: nothing released it within %s", maxWriteWait)
	}
}

func (lock writeLock) Unlock() { <-lock.slot }

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
