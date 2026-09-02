package nativekv

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"

	"github.com/HW-Yue/Memora/internal/store"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

const payloadVersion = 1

type value struct {
	bucket   string
	key      string
	payload  []byte
	revision uint64
	deleted  bool
}

// Database holds the committed state of one KV file.
//
// values is treated as immutable once published: nothing writes into the map
// or into a value's payload after it becomes visible. Commit builds the next
// map and swaps it in under the write lock instead of mutating this one. That
// is what lets Begin hand out the current map by reference rather than copying
// it — see Begin.
type Database struct {
	mu     sync.RWMutex
	file   *nativestore.File
	values map[string]value
	closed bool
}

func Open(path string) (store.Store, error) {
	file, err := nativestore.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		file, err = nativestore.Create(path, nativestore.FileKindSystem)
	}
	if err != nil {
		return nil, err
	}
	database := &Database{file: file, values: map[string]value{}}
	ids, err := file.IDs(nativestore.ObjectKindOpaque)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	for _, id := range ids {
		payload, err := file.Get(nativestore.ObjectKindOpaque, id)
		if err != nil {
			_ = file.Close()
			return nil, err
		}
		item, err := decode(payload)
		if err != nil || recordID(item) != id {
			_ = file.Close()
			return nil, fmt.Errorf("native KV record is corrupt")
		}
		logical := logicalKey(item.bucket, item.key)
		if current, ok := database.values[logical]; !ok || item.revision > current.revision {
			database.values[logical] = item
		}
	}
	return database, nil
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
	// The snapshot is the published map by reference, not a copy.
	//
	// A transaction must not see commits that land after it starts, and it used
	// to buy that by deep-copying every entry and every payload — on read-only
	// transactions too. That is paid once per transaction, and the stores this
	// backs are touched on every call, so the cost grew with how much the
	// instance had ever recorded.
	//
	// Referencing is safe because the published map is immutable: Commit swaps
	// in a new map rather than writing into this one, and no payload is ever
	// mutated in place (Get and Scan hand out copies, Put copies its argument).
	// A commit that lands after this line therefore publishes a different map
	// and leaves the one this transaction holds untouched.
	return &transaction{database: database, mode: mode, snapshot: database.values, changes: map[string]value{}}, nil
}

func (database *Database) Close() error {
	database.mu.Lock()
	defer database.mu.Unlock()
	if database.closed {
		return nil
	}
	database.closed = true
	return database.file.Close()
}

type transaction struct {
	mu       sync.Mutex
	database *Database
	mode     store.Mode
	snapshot map[string]value
	changes  map[string]value
	done     bool
}

func (tx *transaction) Get(ctx context.Context, bucket, key string) ([]byte, error) {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if err := tx.ready(ctx); err != nil {
		return nil, err
	}
	item, ok := tx.current(bucket, key)
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
	// Uncommitted changes win over the snapshot, so they are read first and the
	// snapshot skips any key they cover. This used to merge both into a third
	// map first, which allocated one entry per key in the store on every scan
	// even when the bucket held nothing.
	entries := []store.Entry{}
	for _, item := range tx.changes {
		if item.bucket == bucket && !item.deleted {
			entries = append(entries, store.Entry{Key: item.key, Value: append([]byte(nil), item.payload...)})
		}
	}
	for key, item := range tx.snapshot {
		if _, overridden := tx.changes[key]; overridden {
			continue
		}
		if item.bucket == bucket && !item.deleted {
			entries = append(entries, store.Entry{Key: item.key, Value: append([]byte(nil), item.payload...)})
		}
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
	staged := make([]value, 0, len(keys))
	for _, key := range keys {
		item := tx.changes[key]
		item.revision = tx.database.values[key].revision + 1
		if err := nativeTx.Put(nativestore.ObjectKindOpaque, 1, recordID(item), encode(item)); err != nil {
			return err
		}
		staged = append(staged, item)
	}
	if err := nativeTx.Commit(); err != nil {
		return err
	}
	// Publish a new map rather than writing into the current one: transactions
	// opened earlier hold that map by reference and must keep seeing the state
	// they started from. This copy is the one place the entry count is paid,
	// and only on a write that actually commits.
	next := make(map[string]value, len(tx.database.values)+len(staged))
	for key, item := range tx.database.values {
		next[key] = item
	}
	for _, item := range staged {
		next[logicalKey(item.bucket, item.key)] = item
	}
	tx.database.values = next
	return nil
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

func (tx *transaction) current(bucket, key string) (value, bool) {
	logical := logicalKey(bucket, key)
	if item, ok := tx.changes[logical]; ok {
		return item, true
	}
	item, ok := tx.snapshot[logical]
	return item, ok
}

func logicalKey(bucket, key string) string { return bucket + "\x00" + key }

func recordID(item value) string {
	sum := sha256.Sum256([]byte(logicalKey(item.bucket, item.key)))
	return fmt.Sprintf("kv-%s-%020d", hex.EncodeToString(sum[:]), item.revision)
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

func decode(payload []byte) (value, error) {
	if len(payload) < 23 || binary.LittleEndian.Uint16(payload[:2]) != payloadVersion {
		return value{}, errors.New("invalid native KV payload")
	}
	revision := binary.LittleEndian.Uint64(payload[2:10])
	deleted := payload[10]
	bucketLength := int(binary.LittleEndian.Uint32(payload[11:15]))
	keyLength := int(binary.LittleEndian.Uint32(payload[15:19]))
	valueLength := int(binary.LittleEndian.Uint32(payload[19:23]))
	if revision == 0 || deleted > 1 || bucketLength < 1 || keyLength < 1 || bucketLength+keyLength+valueLength != len(payload)-23 {
		return value{}, errors.New("invalid native KV payload")
	}
	offset := 23
	item := value{revision: revision, deleted: deleted == 1}
	item.bucket = string(payload[offset : offset+bucketLength])
	offset += bucketLength
	item.key = string(payload[offset : offset+keyLength])
	offset += keyLength
	item.payload = append([]byte(nil), payload[offset:]...)
	return item, nil
}
