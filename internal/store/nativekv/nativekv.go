// Package nativekv is a bucketed key/value Store over one native record log.
//
// The committed state lives in an on-disk B+ Tree beside the log, not in the
// process. It used to be a map holding every key and every payload, rebuilt by
// decoding the whole file at Open — an unbounded resident index over data the
// disk already held, which is the third criterion of architecture principle
// four, and the Stores this backs (auxiliary.memora, security.memora) are
// written on every call with route traces growing without bound.
//
// The log stays the authority. The Tree is derived from it and records the log
// Size it is current through, so a restart reads the records written since
// rather than all of them, and a crash between the log append and the Tree
// commit is repaired by reading that same tail again.
package nativekv

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/HW-Yue/Memora/internal/store"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
	"github.com/HW-Yue/Memora/internal/store/objectindex"
	"github.com/HW-Yue/Memora/internal/store/page"
	"github.com/HW-Yue/Memora/internal/store/treecommit"
	"github.com/HW-Yue/Memora/internal/store/wal"
)

const (
	// payloadVersion is one value stored whole; chunkedVersion is the header of
	// one stored in pieces, where the payload field holds the piece count.
	payloadVersion = 1
	chunkedVersion = 2
)

const (
	// entryKind holds one bucket/key pair; metaKind holds the catch-up marker.
	// Two kinds rather than a reserved ID so a walk of the entries never has to
	// step over the marker.
	entryKind = uint16(1)
	metaKind  = uint16(2)
	// chunkKind holds the pieces of a value too large for one leaf record. They
	// live in their own kind so a walk of the entries never sees them.
	chunkKind = uint16(3)

	// chunkBytes is how much of a payload goes in one piece.
	//
	// A leaf record is capped at 8 KiB (objectindex refuses more, because one
	// record shares a 16 KiB Page with its neighbours and overflow Pages do not
	// exist yet). The old map had no such limit, so moving to the Tree would
	// have capped what this Store can hold — a real loss of capability, not a
	// tuning question. Splitting the payload keeps values unbounded; the slack
	// below 8 KiB leaves room for the header and the key.
	chunkBytes = 6 << 10

	indexedThroughID = "indexed-through"

	kvSpaceID    = uint64(0x6b76) // "kv"
	kvOpenFrames = uint64(256)    // 256 x 16 KiB = 4 MiB per Store
	kvWALRing    = uint64(16 << 20)

	// scanPageSize bounds one step of a bucket walk. A Scan takes as many steps
	// as the bucket needs; the cost is the entries returned, not the entries
	// that exist.
	scanPageSize = 200
)

type value struct {
	bucket   string
	key      string
	payload  []byte
	revision uint64
	deleted  bool
}

// Database is one KV file and the Tree that indexes it.
//
// mu is held for the duration of one operation, never for the life of a
// transaction. That is what the isolation this Store offers rests on, and the
// move to a Tree changed it:
//
// The old map was snapshotted at Begin by copying every entry, so a transaction
// read the same state from first operation to last. A shadow-paged Tree has
// nothing to copy — a Page a commit retires is reusable immediately, because
// treecommit.Runtime keeps no reader table, so holding an old root is not
// holding a snapshot. Without such a table the choice is either a stable
// snapshot bought by copying, which is the cost being removed, or no copying
// with reads that see the Store as it is when they run. This takes the second.
//
// So each operation excludes a concurrent commit and is therefore never served
// a recycled Page, but two reads in one transaction may straddle another
// transaction's commit. Holding the lock for a transaction's life would restore
// the stronger guarantee and is not an option: store.Tx is explicitly allowed to
// be held by its caller across other work — an MSQL BEGIN/COMMIT is exactly
// that — so tying lock hold time to transaction lifetime deadlocks the Store on
// the next operation that connection makes.
//
// A transaction still always sees its own uncommitted writes, and a commit is
// still all-or-nothing: changes are staged in the transaction and reach the
// Tree in one batch.
type Database struct {
	mu      sync.RWMutex
	file    *nativestore.File
	set     *wal.SegmentSet
	manager *page.Manager
	runtime *treecommit.Runtime
	index   *objectindex.Index
	closed  bool
}

func Open(path string) (store.Store, error) {
	file, err := nativestore.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		file, err = nativestore.Create(path, nativestore.FileKindSystem)
	}
	if err != nil {
		return nil, err
	}
	database := &Database{file: file}
	if err := database.openIndex(path + ".index"); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := database.catchUp(); err != nil {
		_ = database.Close()
		return nil, err
	}
	return database, nil
}

// openIndex opens the Tree beside the log, creating it the first time.
func (database *Database) openIndex(directory string) error {
	walDirectory := filepath.Join(directory, "wal")
	pageFile := filepath.Join(directory, "pages")

	_, statErr := os.Stat(pageFile)
	fresh := errors.Is(statErr, os.ErrNotExist)
	if statErr != nil && !fresh {
		return fmt.Errorf("stat native KV index: %w", statErr)
	}
	if fresh {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return fmt.Errorf("create native KV index directory: %w", err)
		}
	}

	var (
		set     *wal.SegmentSet
		manager *page.Manager
		err     error
	)
	if fresh {
		set, err = wal.CreateSegmentSetWithCapacity(walDirectory, 0, kvWALRing)
	} else {
		set, err = wal.OpenSegmentSetWithCapacity(walDirectory, 0, kvWALRing)
	}
	if err != nil {
		return fmt.Errorf("open native KV redo log: %w", err)
	}
	if fresh {
		manager, err = page.Create(pageFile, kvSpaceID)
	} else {
		manager, err = page.Open(pageFile, kvSpaceID)
	}
	if err != nil {
		_ = set.Close()
		return fmt.Errorf("open native KV page file: %w", err)
	}
	runtime, _, err := treecommit.OpenRuntime(set, manager, treecommit.RuntimeConfig{
		SpaceID: kvSpaceID, Capacity: kvOpenFrames, OldFrames: kvOpenFrames / 2,
	})
	if err != nil {
		_ = set.Close()
		_ = manager.Close()
		return fmt.Errorf("open native KV Tree: %w", err)
	}
	index, err := objectindex.Open(runtime)
	if err != nil {
		_ = set.Close()
		_ = manager.Close()
		return fmt.Errorf("open native KV object index: %w", err)
	}
	database.set, database.manager, database.runtime, database.index = set, manager, runtime, index
	return nil
}

// catchUp brings the Tree level with the log.
//
// The marker is the log Size the Tree is current through, so the usual case is
// two numbers being equal and nothing read. Anything else — a first open, or a
// crash between the log append and the Tree commit — replays only the records
// written past the marker.
func (database *Database) catchUp() error {
	size, err := database.file.Size()
	if err != nil {
		return err
	}
	indexed, err := database.indexedThrough()
	if err != nil {
		return err
	}
	if indexed == size {
		return nil
	}
	if indexed > size {
		return fmt.Errorf("native KV index leads its log: indexed %d, log %d", indexed, size)
	}
	records, err := database.file.RecordsSince(indexed)
	if err != nil {
		return err
	}
	latest := map[string]value{}
	for _, record := range records {
		if record.Kind != nativestore.ObjectKindOpaque {
			continue
		}
		item, _, err := decode(record.Payload)
		if err != nil || recordID(item) != record.ID {
			return errors.New("native KV record is corrupt")
		}
		logical := logicalKey(item.bucket, item.key)
		if current, ok := latest[logical]; !ok || item.revision > current.revision {
			latest[logical] = item
		}
	}
	return database.publish(latest, size)
}

// publish writes one batch of entries plus the marker in a single Tree commit,
// so the marker can never claim more than the Tree holds.
func (database *Database) publish(entries map[string]value, through int64) error {
	transactionID, err := database.nextTransactionID()
	if err != nil {
		return err
	}
	logicalKeys := make([]string, 0, len(entries))
	for logical := range entries {
		logicalKeys = append(logicalKeys, logical)
	}
	sort.Strings(logicalKeys)

	updates := make([]objectindex.Update, 0, len(entries)+1)
	for _, logical := range logicalKeys {
		item := entries[logical]
		expected, err := database.storedRevision(entryKind, logical)
		if err != nil {
			return err
		}
		if expected >= item.revision {
			// Already indexed; replaying the same tail has to converge.
			continue
		}
		entry, chunks := encodeEntry(item)
		updates = append(updates, objectindex.Update{
			Record: objectindex.Record{
				Kind: entryKind, ID: logical, Revision: item.revision, Body: entry,
			},
			ExpectedRevision: expected,
		})
		for index, chunk := range chunks {
			id := chunkID(logical, index)
			chunkRevision, err := database.storedRevision(chunkKind, id)
			if err != nil {
				return err
			}
			updates = append(updates, objectindex.Update{
				Record: objectindex.Record{
					Kind: chunkKind, ID: id, Revision: chunkRevision + 1, Body: chunk,
				},
				ExpectedRevision: chunkRevision,
			})
		}
	}

	markerRevision, err := database.storedRevision(metaKind, indexedThroughID)
	if err != nil {
		return err
	}
	body := make([]byte, 8)
	binary.LittleEndian.PutUint64(body, uint64(through))
	updates = append(updates, objectindex.Update{
		Record: objectindex.Record{
			Kind: metaKind, ID: indexedThroughID, Revision: markerRevision + 1, Body: body,
		},
		ExpectedRevision: markerRevision,
	})

	if err := database.relieveRedoRing(); err != nil {
		return err
	}
	if _, err := database.index.Apply(transactionID, updates); err != nil {
		return err
	}
	return database.maintainRedoLog()
}

func (database *Database) storedRevision(kind uint16, id string) (uint64, error) {
	stored, err := database.index.Lookup(kind, id)
	switch {
	case err == nil:
		return stored.Revision, nil
	case errors.Is(err, objectindex.ErrNotFound):
		return 0, nil
	default:
		return 0, err
	}
}

func (database *Database) indexedThrough() (int64, error) {
	stored, err := database.index.Lookup(metaKind, indexedThroughID)
	if errors.Is(err, objectindex.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if len(stored.Body) != 8 {
		return 0, errors.New("native KV index marker is corrupt")
	}
	return int64(binary.LittleEndian.Uint64(stored.Body)), nil
}

func (database *Database) nextTransactionID() (uint64, error) {
	frontier, err := database.set.DurableFrontier()
	if err != nil {
		return 0, err
	}
	if frontier.LastTransactionID == math.MaxUint64 {
		return 0, errors.New("native KV transaction ID exhausted")
	}
	return frontier.LastTransactionID + 1, nil
}

// lookup resolves one logical key against the Tree, excluding a concurrent
// commit for the duration of the descent.
func (database *Database) lookup(logical string) (value, bool, error) {
	database.mu.RLock()
	defer database.mu.RUnlock()
	if database.closed {
		return value{}, false, store.ErrTxClosed
	}
	return database.lookupLocked(logical)
}

// lookupLocked is lookup for callers that already hold mu — Commit does, and
// sync.RWMutex is not reentrant.
func (database *Database) lookupLocked(logical string) (value, bool, error) {
	stored, err := database.index.Lookup(entryKind, logical)
	if errors.Is(err, objectindex.ErrNotFound) {
		return value{}, false, nil
	}
	if err != nil {
		return value{}, false, err
	}
	item, chunks, err := decode(stored.Body)
	if err != nil {
		return value{}, false, errors.New("native KV record is corrupt")
	}
	if chunks > 0 {
		payload, err := database.readChunks(logical, chunks)
		if err != nil {
			return value{}, false, err
		}
		item.payload = payload
	}
	return item, true, nil
}

// readChunks joins the pieces of a value too large for one leaf record.
func (database *Database) readChunks(logical string, chunks int) ([]byte, error) {
	joined := make([]byte, 0, chunks*chunkBytes)
	for index := 0; index < chunks; index++ {
		stored, err := database.index.Lookup(chunkKind, chunkID(logical, index))
		if err != nil {
			return nil, fmt.Errorf("read native KV value piece %d: %w", index, err)
		}
		joined = append(joined, stored.Body...)
	}
	return joined, nil
}

func (database *Database) Begin(ctx context.Context, mode store.Mode) (store.Tx, error) {
	if mode != store.ReadOnly && mode != store.ReadWrite {
		return nil, fmt.Errorf("native KV transaction mode is invalid")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	database.mu.RLock()
	defer database.mu.RUnlock()
	if database.closed {
		return nil, store.ErrTxClosed
	}
	return &transaction{database: database, mode: mode, changes: map[string]value{}}, nil
}

func (database *Database) Close() error {
	database.mu.Lock()
	defer database.mu.Unlock()
	if database.closed {
		return nil
	}
	database.closed = true
	var errs []error
	// A clean close leaves nothing to replay. Without this the redo log keeps
	// whatever the last roll threshold did not reach, and the next Open replays
	// it — which pulls those Pages into the buffer Pool and makes opening cost
	// the tail again. Failures here are reported but do not stop the close: the
	// log is still correct, the next open is just slower.
	if database.set != nil {
		errs = append(errs, database.runRedoMaintenance())
	}
	if database.set != nil {
		errs = append(errs, database.set.Close())
	}
	if database.manager != nil {
		errs = append(errs, database.manager.Close())
	}
	errs = append(errs, database.file.Close())
	return errors.Join(errs...)
}

type transaction struct {
	mu       sync.Mutex
	database *Database
	mode     store.Mode
	changes  map[string]value
	done     bool
}

func (tx *transaction) Get(ctx context.Context, bucket, key string) ([]byte, error) {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if err := tx.ready(ctx); err != nil {
		return nil, err
	}
	item, ok, err := tx.current(bucket, key)
	if err != nil {
		return nil, err
	}
	if !ok || item.deleted {
		return nil, store.ErrNotFound
	}
	return append([]byte(nil), item.payload...), nil
}

func (tx *transaction) Scan(ctx context.Context, bucket string) ([]store.Entry, error) {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if err := tx.ready(ctx); err != nil {
		return nil, err
	}
	tx.database.mu.RLock()
	defer tx.database.mu.RUnlock()
	if tx.database.closed {
		return nil, store.ErrTxClosed
	}
	entries := []store.Entry{}
	for _, item := range tx.changes {
		if item.bucket == bucket && !item.deleted {
			entries = append(entries, store.Entry{Key: item.key, Value: append([]byte(nil), item.payload...)})
		}
	}
	// Entry IDs are bucket\x00key, so one bucket is a contiguous key range and
	// the walk stops at the first ID outside it rather than reading the Store.
	// The cursor starts after the bare bucket name, which sorts immediately
	// before every key in it and is never itself an entry.
	prefix := bucket + "\x00"
	after := bucket
	for {
		found, err := tx.database.index.Page(entryKind, after, scanPageSize)
		if err != nil {
			return nil, err
		}
		stop := false
		for _, stored := range found.Records {
			if !strings.HasPrefix(stored.ID, prefix) {
				stop = true
				break
			}
			if _, overridden := tx.changes[stored.ID]; overridden {
				continue
			}
			item, chunks, err := decode(stored.Body)
			if err != nil {
				return nil, errors.New("native KV record is corrupt")
			}
			if chunks > 0 {
				payload, err := tx.database.readChunks(stored.ID, chunks)
				if err != nil {
					return nil, err
				}
				item.payload = payload
			}
			if !item.deleted {
				entries = append(entries, store.Entry{
					Key: item.key, Value: append([]byte(nil), item.payload...),
				})
			}
		}
		if stop || !found.Truncated {
			break
		}
		after = found.NextAfterID
	}
	sort.Slice(entries, func(left, right int) bool { return entries[left].Key < entries[right].Key })
	return entries, nil
}

func (tx *transaction) Put(ctx context.Context, bucket, key string, payload []byte) error {
	return tx.change(ctx, value{bucket: bucket, key: key, payload: append([]byte(nil), payload...)})
}

func (tx *transaction) Delete(ctx context.Context, bucket, key string) error {
	return tx.change(ctx, value{bucket: bucket, key: key, deleted: true})
}

func (tx *transaction) change(ctx context.Context, item value) error {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if err := tx.ready(ctx); err != nil {
		return err
	}
	if tx.mode != store.ReadWrite {
		return store.ErrReadOnly
	}
	if item.bucket == "" || item.key == "" {
		return fmt.Errorf("native KV bucket and key are required")
	}
	tx.changes[logicalKey(item.bucket, item.key)] = item
	return nil
}

func (tx *transaction) Commit() error {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if tx.done {
		return store.ErrTxClosed
	}
	tx.done = true
	if tx.mode == store.ReadOnly || len(tx.changes) == 0 {
		return nil
	}
	tx.database.mu.Lock()
	defer tx.database.mu.Unlock()
	if tx.database.closed {
		return store.ErrTxClosed
	}

	keys := make([]string, 0, len(tx.changes))
	for key := range tx.changes {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	nativeTx, err := tx.database.file.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = nativeTx.Rollback() }()
	staged := make(map[string]value, len(keys))
	for _, key := range keys {
		item := tx.changes[key]
		previous, _, err := tx.database.lookupLocked(key)
		if err != nil {
			return err
		}
		item.revision = previous.revision + 1
		if err := nativeTx.Put(nativestore.ObjectKindOpaque, 1, recordID(item), encode(item)); err != nil {
			return err
		}
		staged[key] = item
	}
	// The log is the authority, so it commits first. A crash before the Tree
	// commit leaves the Tree behind by exactly this batch, and catchUp replays
	// it from the marker on the next open.
	if err := nativeTx.Commit(); err != nil {
		return err
	}
	size, err := tx.database.file.Size()
	if err != nil {
		return err
	}
	return tx.database.publish(staged, size)
}

func (tx *transaction) Rollback() error {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if tx.done {
		return store.ErrTxClosed
	}
	tx.done = true
	return nil
}

func (tx *transaction) ready(ctx context.Context) error {
	if tx.done {
		return store.ErrTxClosed
	}
	return ctx.Err()
}

func (tx *transaction) current(bucket, key string) (value, bool, error) {
	logical := logicalKey(bucket, key)
	if item, ok := tx.changes[logical]; ok {
		return item, true, nil
	}
	return tx.database.lookup(logical)
}

func logicalKey(bucket, key string) string { return bucket + "\x00" + key }

func recordID(item value) string {
	sum := sha256.Sum256([]byte(logicalKey(item.bucket, item.key)))
	return fmt.Sprintf("kv-%s-%020d", hex.EncodeToString(sum[:]), item.revision)
}

// encodeEntry returns the record for an entry and, when its payload is too
// large for one leaf record, the pieces it has to be split into.
func encodeEntry(item value) (entry []byte, chunks [][]byte) {
	whole := encode(item)
	if len(whole) <= chunkBytes {
		return whole, nil
	}
	for offset := 0; offset < len(item.payload); offset += chunkBytes {
		end := min(offset+chunkBytes, len(item.payload))
		chunks = append(chunks, append([]byte(nil), item.payload[offset:end]...))
	}
	header := item
	header.payload = nil
	entry = encode(header)
	binary.LittleEndian.PutUint16(entry[:2], chunkedVersion)
	binary.LittleEndian.PutUint32(entry[19:23], uint32(len(chunks)))
	return entry, chunks
}

func chunkID(logical string, index int) string {
	return fmt.Sprintf("%s#%08d", logical, index)
}

func encode(item value) []byte {
	encoded := make([]byte, 0, 25+len(item.bucket)+len(item.key)+len(item.payload))
	encoded = binary.LittleEndian.AppendUint16(encoded, payloadVersion)
	encoded = binary.LittleEndian.AppendUint64(encoded, item.revision)
	if item.deleted {
		encoded = append(encoded, 1)
	} else {
		encoded = append(encoded, 0)
	}
	encoded = binary.LittleEndian.AppendUint32(encoded, uint32(len(item.bucket)))
	encoded = binary.LittleEndian.AppendUint32(encoded, uint32(len(item.key)))
	encoded = binary.LittleEndian.AppendUint32(encoded, uint32(len(item.payload)))
	encoded = append(encoded, item.bucket...)
	encoded = append(encoded, item.key...)
	return append(encoded, item.payload...)
}

// decode reads an entry record. A chunked entry comes back with chunks set and
// no payload; the caller reads the pieces and joins them.
func decode(payload []byte) (item value, chunks int, err error) {
	if len(payload) < 23 {
		return value{}, 0, errors.New("invalid native KV payload")
	}
	version := binary.LittleEndian.Uint16(payload[:2])
	if version != payloadVersion && version != chunkedVersion {
		return value{}, 0, errors.New("invalid native KV payload")
	}
	revision := binary.LittleEndian.Uint64(payload[2:10])
	deleted := payload[10]
	bucketLength := int(binary.LittleEndian.Uint32(payload[11:15]))
	keyLength := int(binary.LittleEndian.Uint32(payload[15:19]))
	valueLength := int(binary.LittleEndian.Uint32(payload[19:23]))
	if revision == 0 || deleted > 1 || bucketLength < 1 || keyLength < 1 {
		return value{}, 0, errors.New("invalid native KV payload")
	}
	if version == chunkedVersion {
		if bucketLength+keyLength != len(payload)-23 || valueLength == 0 {
			return value{}, 0, errors.New("invalid native KV payload")
		}
		chunks = int(valueLength)
		valueLength = 0
	} else if bucketLength+keyLength+valueLength != len(payload)-23 {
		return value{}, 0, errors.New("invalid native KV payload")
	}
	offset := 23
	item = value{revision: revision, deleted: deleted == 1}
	item.bucket = string(payload[offset : offset+bucketLength])
	offset += bucketLength
	item.key = string(payload[offset : offset+keyLength])
	offset += keyLength
	item.payload = append([]byte(nil), payload[offset:]...)
	return item, chunks, nil
}
