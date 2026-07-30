package history

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/store"
)

const (
	metaBucket   = "history_meta"
	metaKey      = "commit_sequence"
	recordBucket = "history_records"
	indexBucket  = "history_indexes"
	maxPageSize  = 1000
)

type Service struct {
	store store.Store
}

type Error struct {
	Code    result.Code
	Message string
}

func (err *Error) Error() string      { return err.Message }
func (err *Error) StableCode() string { return string(err.Code) }

func New(database store.Store) *Service {
	return &Service{store: database}
}

func (service *Service) ReserveCommitSequence(ctx context.Context, tx store.Tx) (uint64, error) {
	current := uint64(0)
	encoded, err := tx.Get(ctx, metaBucket, metaKey)
	if err == nil {
		var state struct {
			Version string `json:"version"`
			Value   uint64 `json:"value"`
		}
		if decodeErr := json.Unmarshal(encoded, &state); decodeErr != nil || state.Version != Version {
			return 0, historyError(result.CodeInternal, "history commit sequence is corrupt")
		}
		current = state.Value
	} else if !errors.Is(err, store.ErrNotFound) {
		return 0, stableError(err)
	}
	if current == math.MaxUint64 {
		return 0, historyError(result.CodeConstraint, "history commit sequence is exhausted")
	}
	next := current + 1
	encoded, err = json.Marshal(struct {
		Version string `json:"version"`
		Value   uint64 `json:"value"`
	}{Version: Version, Value: next})
	if err != nil {
		return 0, historyError(result.CodeInternal, "history commit sequence could not be encoded")
	}
	if err := tx.Put(ctx, metaBucket, metaKey, encoded); err != nil {
		return 0, stableError(err)
	}
	return next, nil
}

func (service *Service) AppendIn(ctx context.Context, tx store.Tx, record Record) error {
	record.Version = Version
	if record.SourceKind == "" {
		record.SourceKind = SourceConversationAssertion
	}
	if err := validateRecord(record); err != nil {
		return err
	}
	revisions, err := loadIndex(ctx, tx, record.TableID, record.RowID)
	if err != nil {
		return err
	}
	expected := record.Revision
	if len(revisions) > 0 {
		expected = revisions[len(revisions)-1] + 1
	}
	if record.Revision != expected {
		return historyError(
			result.CodeRevisionConflict,
			fmt.Sprintf("history revision is %d; expected %d", record.Revision, expected),
		)
	}
	key := recordKey(record.TableID, record.RowID, record.Revision)
	if _, err := tx.Get(ctx, recordBucket, key); err == nil {
		return historyError(result.CodeAlreadyExists, "history revision already exists")
	} else if !errors.Is(err, store.ErrNotFound) {
		return stableError(err)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return historyError(result.CodeInternal, "history revision could not be encoded")
	}
	if err := tx.Put(ctx, recordBucket, key, encoded); err != nil {
		return stableError(err)
	}
	revisions = append(revisions, record.Revision)
	return saveIndex(ctx, tx, record.TableID, record.RowID, revisions)
}

func (service *Service) List(ctx context.Context, tableID, rowID string) ([]Record, error) {
	tx, err := service.store.Begin(ctx, store.ReadOnly)
	if err != nil {
		return nil, stableError(err)
	}
	defer func() { _ = tx.Rollback() }()
	return service.ListIn(ctx, tx, tableID, rowID)
}

func (service *Service) GetRevisionIn(
	ctx context.Context,
	tx store.Tx,
	tableID, rowID string,
	revision uint64,
) (Record, error) {
	if revision == 0 {
		return Record{}, historyError(result.CodeValidation, "history revision must be greater than zero")
	}
	return readRecord(ctx, tx, tableID, rowID, revision)
}

func (service *Service) AsOfCommitIn(
	ctx context.Context,
	tx store.Tx,
	tableID, rowID string,
	commitSequence uint64,
) (Record, error) {
	if commitSequence == 0 {
		return Record{}, historyError(result.CodeValidation, "commit sequence must be greater than zero")
	}
	revisions, err := loadIndex(ctx, tx, tableID, rowID)
	if err != nil {
		return Record{}, err
	}
	for index := len(revisions) - 1; index >= 0; index-- {
		record, err := readRecord(ctx, tx, tableID, rowID, revisions[index])
		if err != nil {
			return Record{}, err
		}
		if record.CommitSequence <= commitSequence {
			return record, nil
		}
	}
	return Record{}, historyError(result.CodeNotFound, "no Row history is visible at the requested commit sequence")
}

func (service *Service) ListPageIn(
	ctx context.Context,
	tx store.Tx,
	tableID, rowID string,
	limit int,
) ([]Record, bool, error) {
	if limit < 1 || limit > maxPageSize {
		return nil, false, historyError(
			result.CodeValidation,
			fmt.Sprintf("history limit must be between 1 and %d", maxPageSize),
		)
	}
	revisions, err := loadIndex(ctx, tx, tableID, rowID)
	if err != nil {
		return nil, false, err
	}
	count := min(limit, len(revisions))
	records := make([]Record, 0, count)
	for index := len(revisions) - 1; index >= len(revisions)-count; index-- {
		record, err := readRecord(ctx, tx, tableID, rowID, revisions[index])
		if err != nil {
			return nil, false, err
		}
		records = append(records, record)
	}
	return records, len(revisions) > count, nil
}

func (service *Service) ListIn(ctx context.Context, tx store.Tx, tableID, rowID string) ([]Record, error) {
	revisions, err := loadIndex(ctx, tx, tableID, rowID)
	if err != nil {
		return nil, err
	}
	records := make([]Record, 0, len(revisions))
	for _, revision := range revisions {
		record, err := readRecord(ctx, tx, tableID, rowID, revision)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

func readRecord(ctx context.Context, tx store.Tx, tableID, rowID string, revision uint64) (Record, error) {
	encoded, err := tx.Get(ctx, recordBucket, recordKey(tableID, rowID, revision))
	if errors.Is(err, store.ErrNotFound) {
		return Record{}, historyError(result.CodeNotFound, "history revision was not found")
	}
	if err != nil {
		return Record{}, stableError(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var record Record
	if err := decoder.Decode(&record); err != nil || record.Version != Version ||
		record.TableID != tableID || record.RowID != rowID || record.Revision != revision {
		return Record{}, historyError(result.CodeInternal, "history revision is corrupt")
	}
	if err := validateRecord(record); err != nil {
		return Record{}, historyError(result.CodeInternal, "history revision is corrupt")
	}
	return record, nil
}

func loadIndex(ctx context.Context, tx store.Tx, tableID, rowID string) ([]uint64, error) {
	encoded, err := tx.Get(ctx, indexBucket, indexKey(tableID, rowID))
	if errors.Is(err, store.ErrNotFound) {
		return []uint64{}, nil
	}
	if err != nil {
		return nil, stableError(err)
	}
	var state struct {
		Version   string   `json:"version"`
		Revisions []uint64 `json:"revisions"`
	}
	if err := json.Unmarshal(encoded, &state); err != nil || state.Version != Version || state.Revisions == nil {
		return nil, historyError(result.CodeInternal, "history index is corrupt")
	}
	for index, revision := range state.Revisions {
		if revision == 0 || (index > 0 && revision != state.Revisions[index-1]+1) {
			return nil, historyError(result.CodeInternal, "history index revisions are corrupt")
		}
	}
	return state.Revisions, nil
}

func saveIndex(ctx context.Context, tx store.Tx, tableID, rowID string, revisions []uint64) error {
	encoded, err := json.Marshal(struct {
		Version   string   `json:"version"`
		Revisions []uint64 `json:"revisions"`
	}{Version: Version, Revisions: revisions})
	if err != nil {
		return historyError(result.CodeInternal, "history index could not be encoded")
	}
	if err := tx.Put(ctx, indexBucket, indexKey(tableID, rowID), encoded); err != nil {
		return stableError(err)
	}
	return nil
}

func validateRecord(record Record) error {
	if record.DatabaseID == "" || record.TableID == "" || record.RowID == "" ||
		record.SchemaVersion == 0 || record.Revision == 0 || record.CommitSequence == 0 ||
		(record.State != "live" && record.State != "deleted") || record.Values == nil || record.CreatedAt.IsZero() ||
		record.UpdatedAt.IsZero() || record.RecordedAt.IsZero() {
		return historyError(result.CodeValidation, "history revision is incomplete")
	}
	switch record.Operation {
	case OperationInsert, OperationUpdate, OperationDelete, OperationCompensate:
	default:
		return historyError(result.CodeValidation, "history operation is invalid")
	}
	if strings.TrimSpace(record.Actor) == "" || strings.TrimSpace(record.Source) == "" ||
		strings.TrimSpace(record.Reason) == "" {
		return historyError(result.CodeValidation, "history provenance is incomplete")
	}
	if record.SourceKind == "" {
		record.SourceKind = SourceConversationAssertion
	}
	switch record.SourceKind {
	case SourceConversationAssertion, SourceDocumentAnchor, SourceRepositoryAnchor, SourceReviewed:
	default:
		return historyError(result.CodeValidation, "history source kind is invalid")
	}
	return nil
}

func recordKey(tableID, rowID string, revision uint64) string {
	return fmt.Sprintf("%s\x00%s\x00%020d", tableID, rowID, revision)
}

func indexKey(tableID, rowID string) string {
	return tableID + "\x00" + rowID
}

func historyError(code result.Code, message string) error {
	return &Error{Code: code, Message: message}
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
		return historyError(result.CodeCancelled, "history operation was cancelled")
	case errors.Is(err, context.DeadlineExceeded):
		return historyError(result.CodeDeadlineExceeded, "history operation exceeded its deadline")
	default:
		return historyError(result.CodeInternal, "history operation failed")
	}
}
