package routetrace

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/HW-Yue/Memora/internal/store"
)

const (
	bodyBucket = "route_trace_v1_body"
	idBucket   = "route_trace_v1_id"
	metaBucket = "route_trace_v1_meta"
	metaKey    = "state"
)

type Service struct {
	mu    sync.Mutex
	store store.Store
}

func New(database store.Store) *Service { return &Service{store: database} }

func (service *Service) Record(ctx context.Context, draft Draft) (Trace, error) {
	if service == nil || service.store == nil || ctx == nil {
		return Trace{}, ErrInvalid
	}
	draft = cloneDraft(draft)
	if draft.validate() != nil {
		return Trace{}, ErrInvalid
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	tx, err := service.store.Begin(ctx, store.ReadWrite)
	if err != nil {
		return Trace{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if locator, err := tx.Get(ctx, idBucket, draft.TraceID); err == nil {
		key := string(locator)
		sequence, parseErr := parseBodyKey(key)
		encoded, bodyErr := tx.Get(ctx, bodyBucket, key)
		existing, decodeErr := decodeTrace(encoded)
		if parseErr != nil || bodyErr != nil || decodeErr != nil || existing.Sequence != sequence || existing.TraceID != draft.TraceID {
			return Trace{}, ErrCorrupt
		}
		state, stateErr := readState(ctx, tx)
		if stateErr != nil || sequence > state.HighWater {
			return Trace{}, ErrCorrupt
		}
		want, sealErr := seal(draft, sequence)
		if sealErr != nil || want.Checksum != existing.Checksum {
			return Trace{}, ErrConflict
		}
		return existing, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return Trace{}, err
	}
	state, err := readState(ctx, tx)
	if err != nil {
		return Trace{}, err
	}
	if state.HighWater == math.MaxUint64 {
		return Trace{}, ErrCorrupt
	}
	sequence := state.HighWater + 1
	key := bodyKey(sequence)
	if _, err := tx.Get(ctx, bodyBucket, key); err == nil || !errors.Is(err, store.ErrNotFound) {
		return Trace{}, ErrCorrupt
	}
	value, err := seal(draft, sequence)
	if err != nil {
		return Trace{}, err
	}
	encoded, err := encodeTrace(value)
	if err != nil {
		return Trace{}, err
	}
	state.HighWater = sequence
	stateEncoded, err := encodeStoreState(state)
	if err != nil {
		return Trace{}, err
	}
	if err := tx.Put(ctx, bodyBucket, key, encoded); err != nil {
		return Trace{}, err
	}
	if err := tx.Put(ctx, idBucket, value.TraceID, []byte(key)); err != nil {
		return Trace{}, err
	}
	if err := tx.Put(ctx, metaBucket, metaKey, stateEncoded); err != nil {
		return Trace{}, err
	}
	if err := tx.Commit(); err != nil {
		return Trace{}, err
	}
	return value, nil
}

func (service *Service) Get(ctx context.Context, traceID, databaseID string) (Trace, error) {
	if service == nil || service.store == nil || ctx == nil || !validTraceID(traceID) {
		return Trace{}, ErrInvalid
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	tx, err := service.store.Begin(ctx, store.ReadOnly)
	if err != nil {
		return Trace{}, err
	}
	defer func() { _ = tx.Rollback() }()
	locator, err := tx.Get(ctx, idBucket, traceID)
	if errors.Is(err, store.ErrNotFound) {
		return Trace{}, ErrNotFound
	}
	if err != nil {
		return Trace{}, err
	}
	value, err := readLocatedTrace(ctx, tx, traceID, string(locator))
	if err != nil {
		return Trace{}, err
	}
	state, err := readState(ctx, tx)
	if err != nil || value.Sequence > state.HighWater {
		return Trace{}, ErrCorrupt
	}
	if databaseID != "" && value.DatabaseID != databaseID {
		return Trace{}, ErrNotFound
	}
	return value, nil
}

func (service *Service) List(
	ctx context.Context,
	databaseID string,
	after uint64,
	snapshot Snapshot,
	limit int,
) ([]Trace, Snapshot, bool, error) {
	if service == nil || service.store == nil || ctx == nil || limit < 1 || limit > 256 {
		return nil, Snapshot{}, false, ErrInvalid
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	tx, err := service.store.Begin(ctx, store.ReadOnly)
	if err != nil {
		return nil, Snapshot{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	state, err := readState(ctx, tx)
	if err != nil {
		return nil, Snapshot{}, false, err
	}
	if snapshot.RetentionEpoch == 0 && snapshot.HighWater == 0 {
		snapshot = Snapshot{HighWater: state.HighWater, RetentionEpoch: state.RetentionEpoch}
	} else if snapshot.RetentionEpoch == 0 || snapshot.RetentionEpoch != state.RetentionEpoch {
		return nil, Snapshot{}, false, ErrSnapshotExpired
	}
	if snapshot.HighWater > state.HighWater || after > snapshot.HighWater {
		return nil, Snapshot{}, false, ErrInvalid
	}
	entries, err := tx.Scan(ctx, bodyBucket)
	if err != nil {
		return nil, Snapshot{}, false, err
	}
	values := make([]Trace, 0, min(limit+1, len(entries)))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, Snapshot{}, false, err
		}
		sequence, err := parseBodyKey(entry.Key)
		value, decodeErr := decodeTrace(entry.Value)
		if err != nil || decodeErr != nil || value.Sequence != sequence || sequence > state.HighWater {
			return nil, Snapshot{}, false, ErrCorrupt
		}
		locator, locatorErr := tx.Get(ctx, idBucket, value.TraceID)
		if locatorErr != nil || string(locator) != entry.Key {
			return nil, Snapshot{}, false, ErrCorrupt
		}
		if sequence <= after || sequence > snapshot.HighWater ||
			(databaseID != "" && value.DatabaseID != databaseID) {
			continue
		}
		values = append(values, value)
		if len(values) > limit {
			return values[:limit], snapshot, true, nil
		}
	}
	return values, snapshot, false, nil
}

func (service *Service) PruneExpired(ctx context.Context, now time.Time) (uint64, error) {
	if service == nil || service.store == nil || ctx == nil || now.IsZero() || now.Location() != time.UTC {
		return 0, ErrInvalid
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	tx, err := service.store.Begin(ctx, store.ReadWrite)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	state, err := readState(ctx, tx)
	if err != nil {
		return 0, err
	}
	entries, err := tx.Scan(ctx, bodyBucket)
	if err != nil {
		return 0, err
	}
	var deleted uint64
	for _, entry := range entries {
		sequence, keyErr := parseBodyKey(entry.Key)
		value, decodeErr := decodeTrace(entry.Value)
		locator, locatorErr := tx.Get(ctx, idBucket, value.TraceID)
		if keyErr != nil || decodeErr != nil || value.Sequence != sequence ||
			locatorErr != nil || string(locator) != entry.Key || sequence > state.HighWater {
			return 0, ErrCorrupt
		}
		if value.ExpiresAt.After(now) {
			continue
		}
		if err := tx.Delete(ctx, bodyBucket, entry.Key); err != nil {
			return 0, err
		}
		if err := tx.Delete(ctx, idBucket, value.TraceID); err != nil {
			return 0, err
		}
		deleted++
	}
	if deleted == 0 {
		return 0, nil
	}
	if state.RetentionEpoch == math.MaxUint64 {
		return 0, ErrCorrupt
	}
	state.RetentionEpoch++
	encoded, err := encodeStoreState(state)
	if err != nil {
		return 0, err
	}
	if err := tx.Put(ctx, metaBucket, metaKey, encoded); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return deleted, nil
}

func readState(ctx context.Context, tx store.Tx) (storeState, error) {
	encoded, err := tx.Get(ctx, metaBucket, metaKey)
	if errors.Is(err, store.ErrNotFound) {
		return defaultStoreState(), nil
	}
	if err != nil {
		return storeState{}, err
	}
	return decodeStoreState(encoded)
}

func readLocatedTrace(ctx context.Context, tx store.Tx, traceID, key string) (Trace, error) {
	sequence, err := parseBodyKey(key)
	if err != nil {
		return Trace{}, ErrCorrupt
	}
	encoded, err := tx.Get(ctx, bodyBucket, key)
	if err != nil {
		return Trace{}, ErrCorrupt
	}
	value, err := decodeTrace(encoded)
	if err != nil || value.Sequence != sequence || value.TraceID != traceID {
		return Trace{}, ErrCorrupt
	}
	return value, nil
}

func parseBodyKey(key string) (uint64, error) {
	if len(key) != len("trace_")+20 || !strings.HasPrefix(key, "trace_") {
		return 0, ErrCorrupt
	}
	sequence, err := strconv.ParseUint(key[len("trace_"):], 10, 64)
	if err != nil || sequence == 0 || bodyKey(sequence) != key {
		return 0, fmt.Errorf("%w: body key", ErrCorrupt)
	}
	return sequence, nil
}
