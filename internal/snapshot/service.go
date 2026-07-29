package snapshot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/store"
)

const (
	catalogBucket = "catalog"
	catalogKey    = "snapshot"
	rowPrefix     = "rows:"
	rowIndex      = "row_indexes"
	historyMeta   = "history_meta"
	historyKey    = "commit_sequence"
	historyRows   = "history_records"
	historyIndex  = "history_indexes"
	relationNow   = "relation_current"
	relationPast  = "relation_versions"
	relationOut   = "relation_forward"
	relationIn    = "relation_reverse"
	snapshotMeta  = "logical_snapshot_meta"
	unknownKey    = "unknown_fields"
)

type Service struct{ store store.Store }

func New(database store.Store) *Service { return &Service{store: database} }

func (service *Service) Export(ctx context.Context) ([]byte, error) {
	tx, err := service.store.Begin(ctx, store.ReadOnly)
	if err != nil {
		return nil, stableError(err)
	}
	defer func() { _ = tx.Rollback() }()
	catalogJSON, err := tx.Get(ctx, catalogBucket, catalogKey)
	if errors.Is(err, store.ErrNotFound) {
		catalogJSON = []byte(`{"version":"memora.catalog/v1","databases":[]}`)
	} else if err != nil {
		return nil, stableError(err)
	}
	layout, err := decodeCatalog(catalogJSON)
	if err != nil {
		return nil, err
	}
	value := document{
		Version: Version, Catalog: cloneRaw(catalogJSON),
		Rows: []json.RawMessage{}, History: []json.RawMessage{},
		RelationNow: []json.RawMessage{}, RelationPast: []json.RawMessage{},
		Unknown: map[string]json.RawMessage{},
	}
	for _, tableID := range layout.tableOrder {
		entries, err := tx.Scan(ctx, rowPrefix+tableID)
		if err != nil {
			return nil, stableError(err)
		}
		ordered, err := orderedRows(ctx, tx, tableID, entries)
		if err != nil {
			return nil, err
		}
		value.Rows = append(value.Rows, ordered...)
	}
	if value.History, err = scanValues(ctx, tx, historyRows); err != nil {
		return nil, err
	}
	if value.RelationNow, err = scanValues(ctx, tx, relationNow); err != nil {
		return nil, err
	}
	if value.RelationPast, err = scanValues(ctx, tx, relationPast); err != nil {
		return nil, err
	}
	if value.CommitSequence, err = loadCommitSequence(ctx, tx); err != nil {
		return nil, err
	}
	if encoded, getErr := tx.Get(ctx, snapshotMeta, unknownKey); getErr == nil {
		if json.Unmarshal(encoded, &value.Unknown) != nil || value.Unknown == nil {
			return nil, snapshotError(result.CodeInternal, "logical snapshot metadata is corrupt")
		}
	} else if !errors.Is(getErr, store.ErrNotFound) {
		return nil, stableError(getErr)
	}
	return encodeDocument(value)
}

func (service *Service) Import(ctx context.Context, encoded []byte) error {
	migrated, err := decodeDocument(encoded)
	if err != nil {
		return err
	}
	prepared, err := prepare(migrated)
	if err != nil {
		return err
	}
	tx, err := service.store.Begin(ctx, store.ReadWrite)
	if err != nil {
		return stableError(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := requireEmpty(ctx, tx); err != nil {
		return err
	}
	if err := tx.Put(ctx, catalogBucket, catalogKey, migrated.Catalog); err != nil {
		return stableError(err)
	}
	for index, rowValue := range migrated.Rows {
		header := prepared.rows[index]
		if err := tx.Put(ctx, rowPrefix+header.TableID, header.ID, rowValue); err != nil {
			return stableError(err)
		}
	}
	for tableID, ids := range prepared.rowIDs {
		if err := putJSON(ctx, tx, rowIndex, tableID, ids); err != nil {
			return err
		}
	}
	for index, record := range migrated.History {
		header := prepared.history[index]
		if err := tx.Put(ctx, historyRows, historyRecordKey(header), record); err != nil {
			return stableError(err)
		}
	}
	for key, revisions := range prepared.historyRevisions {
		if err := putJSON(ctx, tx, historyIndex, key, struct {
			Version   string   `json:"version"`
			Revisions []uint64 `json:"revisions"`
		}{Version: history.Version, Revisions: revisions}); err != nil {
			return err
		}
	}
	if err := putJSON(ctx, tx, historyMeta, historyKey, struct {
		Version string `json:"version"`
		Value   uint64 `json:"value"`
	}{Version: history.Version, Value: prepared.commitSequence}); err != nil {
		return err
	}
	for index, raw := range migrated.RelationNow {
		current := prepared.relationNow[index]
		if err := tx.Put(ctx, relationNow, current.ID, raw); err != nil {
			return stableError(err)
		}
	}
	for index, raw := range migrated.RelationPast {
		version := prepared.relationPast[index]
		if err := tx.Put(ctx, relationPast, relationVersionKey(version.ID, version.Revision), raw); err != nil {
			return stableError(err)
		}
	}
	for key, ids := range prepared.relationOutgoing {
		if err := putRelationIndex(ctx, tx, relationOut, key, ids); err != nil {
			return err
		}
	}
	for key, ids := range prepared.relationIncoming {
		if err := putRelationIndex(ctx, tx, relationIn, key, ids); err != nil {
			return err
		}
	}
	if len(migrated.Unknown) > 0 {
		if err := putJSON(ctx, tx, snapshotMeta, unknownKey, migrated.Unknown); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return stableError(err)
	}
	return nil
}

func scanValues(ctx context.Context, tx store.Tx, bucket string) ([]json.RawMessage, error) {
	entries, err := tx.Scan(ctx, bucket)
	if err != nil {
		return nil, stableError(err)
	}
	values := make([]json.RawMessage, 0, len(entries))
	for _, entry := range entries {
		values = append(values, cloneRaw(entry.Value))
	}
	return values, nil
}

func orderedRows(
	ctx context.Context,
	tx store.Tx,
	tableID string,
	entries []store.Entry,
) ([]json.RawMessage, error) {
	byID := make(map[string]json.RawMessage, len(entries))
	for _, entry := range entries {
		byID[entry.Key] = cloneRaw(entry.Value)
	}
	ids := []string{}
	if encoded, err := tx.Get(ctx, rowIndex, tableID); err == nil {
		if json.Unmarshal(encoded, &ids) != nil || ids == nil {
			return nil, snapshotError(result.CodeInternal, "Row index is corrupt")
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, stableError(err)
	}
	ordered := make([]json.RawMessage, 0, len(entries))
	for _, id := range ids {
		if value, exists := byID[id]; exists {
			ordered = append(ordered, value)
			delete(byID, id)
		}
	}
	remaining := make([]string, 0, len(byID))
	for id := range byID {
		remaining = append(remaining, id)
	}
	sort.Strings(remaining)
	for _, id := range remaining {
		ordered = append(ordered, byID[id])
	}
	return ordered, nil
}

func loadCommitSequence(ctx context.Context, tx store.Tx) (uint64, error) {
	encoded, err := tx.Get(ctx, historyMeta, historyKey)
	if errors.Is(err, store.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, stableError(err)
	}
	var state struct {
		Version string `json:"version"`
		Value   uint64 `json:"value"`
	}
	if json.Unmarshal(encoded, &state) != nil || state.Version != history.Version {
		return 0, snapshotError(result.CodeInternal, "history commit sequence is corrupt")
	}
	return state.Value, nil
}

func requireEmpty(ctx context.Context, tx store.Tx) error {
	for _, bucket := range []string{
		catalogBucket, historyRows, relationNow, relationPast, snapshotMeta,
	} {
		entries, err := tx.Scan(ctx, bucket)
		if err != nil {
			return stableError(err)
		}
		if len(entries) > 0 {
			return snapshotError(result.CodeAlreadyExists, "logical snapshot target is not empty")
		}
	}
	return nil
}

func putJSON(ctx context.Context, tx store.Tx, bucket, key string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return snapshotError(result.CodeInternal, "logical snapshot metadata could not be encoded")
	}
	if err := tx.Put(ctx, bucket, key, encoded); err != nil {
		return stableError(err)
	}
	return nil
}

func putRelationIndex(ctx context.Context, tx store.Tx, bucket, key string, ids []string) error {
	return putJSON(ctx, tx, bucket, key, struct {
		Version     string   `json:"version"`
		RelationIDs []string `json:"relation_ids"`
	}{Version: "memora.relation-index/v1", RelationIDs: ids})
}

func historyRecordKey(value historyHeader) string {
	return fmt.Sprintf("%s\x00%s\x00%020d", value.TableID, value.RowID, value.Revision)
}

func relationVersionKey(id string, revision uint64) string {
	return fmt.Sprintf("%s\x00%020d", id, revision)
}

func stableError(err error) error {
	if err == nil {
		return nil
	}
	var stable interface{ StableCode() string }
	if errors.As(err, &stable) {
		return err
	}
	switch {
	case errors.Is(err, context.Canceled):
		return snapshotError(result.CodeCancelled, "logical snapshot operation was cancelled")
	case errors.Is(err, context.DeadlineExceeded):
		return snapshotError(result.CodeDeadlineExceeded, "logical snapshot operation exceeded its deadline")
	default:
		return snapshotError(result.CodeInternal, "logical snapshot operation failed")
	}
}
