package objectlock

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/HW-Yue/Memora/internal/result"
)

const (
	maxComponentBytes = 2048
	maxBatchKeys      = 1000
)

var (
	ErrInvalid  = errors.New("exact-object write lock request is invalid")
	ErrConflict = errors.New("exact-object write lock conflicts")
)

// Key names one Row. Nothing else takes an exact-object lock.
//
// The lock used to be three-tiered — Row, schema object, Route node — but only
// the Row tier ever had a caller. The other two were deleted rather than wired
// up, because writes are serialised: an exact-object lock buys one thing over
// that serialisation, failing fast when two requests want the same object, and
// that is worth having on the hot Row path and not on schema or Route
// mutations. Those are rare, structural, and already guarded by EXPECTED
// REVISION, which reports a conflict more precisely than a lock error can.
type Key struct {
	databaseID string
	tableID    string
	rowID      string
}

func RowKey(databaseID, tableID, rowID string) (Key, error) {
	if !validComponent(databaseID) || !validComponent(tableID) || !validComponent(rowID) {
		return Key{}, fmt.Errorf("%w: logical object key", ErrInvalid)
	}
	return Key{databaseID: databaseID, tableID: tableID, rowID: rowID}, nil
}

func validComponent(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && len(value) <= maxComponentBytes &&
		utf8.ValidString(value)
}

func (key Key) DatabaseID() string { return key.databaseID }

func (key Key) TableID() string { return key.tableID }

func (key Key) RowID() string { return key.rowID }

func (key Key) String() string {
	return fmt.Sprintf("row(%q,%q,%q)", key.databaseID, key.tableID, key.rowID)
}

func (key Key) valid() bool {
	_, err := RowKey(key.databaseID, key.tableID, key.rowID)
	return err == nil
}

func (key Key) canonical() []byte {
	size := 2 + len(key.databaseID) + 2 + len(key.tableID) + 2 + len(key.rowID)
	result := make([]byte, 0, size)
	result = appendComponent(result, key.databaseID)
	result = appendComponent(result, key.tableID)
	return appendComponent(result, key.rowID)
}

func appendComponent(target []byte, value string) []byte {
	start := len(target)
	target = append(target, 0, 0)
	binary.BigEndian.PutUint16(target[start:start+2], uint16(len(value)))
	return append(target, value...)
}

type ConflictError struct {
	Blocked Key
}

func (err *ConflictError) Error() string {
	return fmt.Sprintf("write conflicts on %s", err.Blocked.String())
}

func (err *ConflictError) Unwrap() error { return ErrConflict }

func (err *ConflictError) StableCode() string { return string(result.CodeWriteConflict) }

type Manager struct {
	mu     sync.Mutex
	owners map[uint64]*Guard
	held   map[Key]*Guard
}

type Guard struct {
	manager *Manager
	ownerID uint64
	held    map[Key]struct{}
	closed  bool
}

func New() *Manager {
	return &Manager{owners: make(map[uint64]*Guard), held: make(map[Key]*Guard)}
}

func (manager *Manager) Begin(ownerID uint64) (*Guard, error) {
	if manager == nil || ownerID == 0 {
		return nil, fmt.Errorf("%w: transaction owner", ErrInvalid)
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.owners == nil || manager.held == nil {
		return nil, fmt.Errorf("%w: uninitialized Manager", ErrInvalid)
	}
	if _, exists := manager.owners[ownerID]; exists {
		return nil, fmt.Errorf("%w: transaction owner is already active", ErrInvalid)
	}
	guard := &Guard{manager: manager, ownerID: ownerID, held: make(map[Key]struct{})}
	manager.owners[ownerID] = guard
	return guard, nil
}

func (guard *Guard) TryAcquire(ctx context.Context, keys ...Key) error {
	if guard == nil || guard.manager == nil || ctx == nil || len(keys) == 0 || len(keys) > maxBatchKeys {
		return fmt.Errorf("%w: transaction guard or key batch", ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	type candidate struct {
		key       Key
		canonical []byte
	}
	unique := make(map[Key]candidate, len(keys))
	for _, key := range keys {
		if !key.valid() {
			return fmt.Errorf("%w: logical object key", ErrInvalid)
		}
		unique[key] = candidate{key: key, canonical: key.canonical()}
	}
	ordered := make([]candidate, 0, len(unique))
	for _, item := range unique {
		ordered = append(ordered, item)
	}
	sort.Slice(ordered, func(left, right int) bool {
		return bytes.Compare(ordered[left].canonical, ordered[right].canonical) < 0
	})

	manager := guard.manager
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if guard.closed || manager.owners[guard.ownerID] != guard {
		return fmt.Errorf("%w: transaction guard is closed", ErrInvalid)
	}
	for _, item := range ordered {
		if holder, exists := manager.held[item.key]; exists && holder != guard {
			return &ConflictError{Blocked: item.key}
		}
	}
	for _, item := range ordered {
		if _, exists := guard.held[item.key]; exists {
			continue
		}
		manager.held[item.key] = guard
		guard.held[item.key] = struct{}{}
	}
	return nil
}

func (guard *Guard) Release() error {
	if guard == nil || guard.manager == nil {
		return nil
	}
	manager := guard.manager
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if guard.closed {
		return nil
	}
	for key := range guard.held {
		delete(manager.held, key)
	}
	delete(manager.owners, guard.ownerID)
	guard.closed = true
	guard.held = nil
	return nil
}
