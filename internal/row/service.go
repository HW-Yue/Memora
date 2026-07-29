package row

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/store"
	"github.com/google/uuid"
)

const (
	rowIndexBucket = "row_indexes"
	maxListRows    = 1000
)

type Catalog interface {
	DescribeTableIn(context.Context, store.Tx, string, string) (catalog.Table, error)
}

type IDSource interface {
	Next() (string, error)
}

type Clock interface {
	Now() time.Time
}

type Options struct {
	IDs   IDSource
	Clock Clock
}

type Service struct {
	store   store.Store
	catalog Catalog
	history *history.Service
	ids     IDSource
	clock   Clock
	mu      sync.Mutex
}

func New(database store.Store, dictionary Catalog, options Options) *Service {
	if options.IDs == nil {
		options.IDs = uuidSource{}
	}
	if options.Clock == nil {
		options.Clock = systemClock{}
	}
	return &Service{
		store: database, catalog: dictionary, history: history.New(database),
		ids: options.IDs, clock: options.Clock,
	}
}

func (service *Service) Insert(ctx context.Context, databaseName, tableName string, values map[string]any, options WriteOptions) (Row, error) {
	transaction, err := service.BeginTransaction(ctx)
	if err != nil {
		return Row{}, stableError(err)
	}
	defer func() { _ = transaction.Rollback() }()
	inserted, err := transaction.Insert(ctx, databaseName, tableName, values, options)
	if err != nil {
		return Row{}, err
	}
	if err := transaction.Commit(); err != nil {
		return Row{}, err
	}
	return inserted, nil
}

func (service *Service) Get(ctx context.Context, databaseName, tableName, rowID string) (Row, error) {
	value, err := service.get(ctx, databaseName, tableName, rowID, false)
	return value, stableError(err)
}

func (service *Service) GetIncludingDeleted(ctx context.Context, databaseName, tableName, rowID string) (Row, error) {
	value, err := service.get(ctx, databaseName, tableName, rowID, true)
	return value, stableError(err)
}

func (service *Service) get(ctx context.Context, databaseName, tableName, rowID string, includeDeleted bool) (Row, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	tx, err := service.store.Begin(ctx, store.ReadOnly)
	if err != nil {
		return Row{}, err
	}
	defer func() { _ = tx.Rollback() }()
	table, err := service.catalog.DescribeTableIn(ctx, tx, databaseName, tableName)
	if err != nil {
		return Row{}, err
	}
	stored, err := getStored(ctx, tx, table.ID, rowID)
	if err != nil {
		return Row{}, err
	}
	if stored.State == StateDeleted && !includeDeleted {
		return Row{}, rowError(result.CodeNotFound, fmt.Sprintf("row %q was not found", rowID))
	}
	return project(table, stored)
}

func (service *Service) List(ctx context.Context, databaseName, tableName string, limit int) ([]Row, error) {
	rows, _, err := service.ListPage(ctx, databaseName, tableName, limit)
	return rows, err
}

func (service *Service) ListPage(ctx context.Context, databaseName, tableName string, limit int) ([]Row, bool, error) {
	if limit < 1 || limit > maxListRows {
		return nil, false, rowError(result.CodeValidation, fmt.Sprintf("row list limit must be between 1 and %d", maxListRows))
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	tx, err := service.store.Begin(ctx, store.ReadOnly)
	if err != nil {
		return nil, false, stableError(err)
	}
	defer func() { _ = tx.Rollback() }()
	table, err := service.catalog.DescribeTableIn(ctx, tx, databaseName, tableName)
	if err != nil {
		return nil, false, stableError(err)
	}
	index, err := loadIndex(ctx, tx, table.ID)
	if err != nil {
		return nil, false, stableError(err)
	}
	rows := make([]Row, 0, min(limit, len(index)))
	for _, rowID := range index {
		stored, err := getStored(ctx, tx, table.ID, rowID)
		if err != nil {
			return nil, false, stableError(err)
		}
		if stored.State == StateDeleted {
			continue
		}
		projected, err := project(table, stored)
		if err != nil {
			return nil, false, stableError(err)
		}
		if len(rows) == limit {
			return rows, true, nil
		}
		rows = append(rows, projected)
	}
	return rows, false, nil
}

func (service *Service) History(ctx context.Context, databaseName, tableName, rowID string) ([]history.Record, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	tx, err := service.store.Begin(ctx, store.ReadOnly)
	if err != nil {
		return nil, stableError(err)
	}
	defer func() { _ = tx.Rollback() }()
	table, err := service.catalog.DescribeTableIn(ctx, tx, databaseName, tableName)
	if err != nil {
		return nil, stableError(err)
	}
	records, err := service.history.ListIn(ctx, tx, table.ID, rowID)
	return records, stableError(err)
}

func (service *Service) nextID() (string, error) {
	id, err := service.ids.Next()
	if err != nil {
		return "", fmt.Errorf("allocate row ID: %w", err)
	}
	if strings.TrimSpace(id) == "" {
		return "", errors.New("allocate row ID: empty ID")
	}
	return "row_" + id, nil
}

func validateValues(table catalog.Table, input map[string]any, base map[string]any) (map[string]any, error) {
	values := make(map[string]any, len(table.Columns))
	for key, value := range base {
		values[key] = value
	}
	resolved := make(map[string]struct{}, len(input))
	for name, value := range input {
		column, found := resolveColumn(table, name)
		if !found {
			return nil, rowError(result.CodeValidation, fmt.Sprintf("unknown column %q", name))
		}
		if _, duplicate := resolved[column.ID]; duplicate {
			return nil, rowError(result.CodeValidation, fmt.Sprintf("column %q was provided more than once", column.Name))
		}
		normalized, err := column.Validate(value)
		if err != nil {
			return nil, err
		}
		resolved[column.ID] = struct{}{}
		values[column.ID] = normalized
	}
	for _, column := range table.Columns {
		value, exists := values[column.ID]
		if !exists {
			value = nil
		}
		normalized, err := column.Validate(value)
		if err != nil {
			return nil, err
		}
		values[column.ID] = normalized
	}
	return values, nil
}

func resolveColumn(table catalog.Table, name string) (catalog.Column, bool) {
	candidate := strings.ToLower(strings.TrimSpace(name))
	for _, column := range table.Columns {
		if strings.ToLower(strings.TrimSpace(column.Name)) == candidate {
			return column, true
		}
		for _, alias := range column.Aliases {
			if strings.ToLower(strings.TrimSpace(alias)) == candidate {
				return column, true
			}
		}
	}
	return catalog.Column{}, false
}

func project(table catalog.Table, stored storedRow) (Row, error) {
	if stored.TableID != table.ID || stored.DatabaseID != table.DatabaseID {
		return Row{}, errors.New("stored row belongs to another table")
	}
	values := make(map[string]any, len(table.Columns))
	for _, column := range table.Columns {
		value, exists := stored.Values[column.ID]
		if !exists {
			value = nil
		}
		normalized, err := column.Validate(value)
		if err != nil {
			return Row{}, err
		}
		values[column.Name] = normalized
	}
	return Row{
		ID: stored.ID, DatabaseID: stored.DatabaseID, TableID: stored.TableID,
		SchemaVersion: stored.SchemaVersion, Revision: stored.Revision,
		CommitSequence: stored.CommitSequence, State: stored.State,
		Values: values, CreatedAt: stored.CreatedAt, UpdatedAt: stored.UpdatedAt,
	}, nil
}

func rowBucket(tableID string) string {
	return "rows:" + tableID
}

func putStored(ctx context.Context, tx store.Tx, value storedRow) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode row: %w", err)
	}
	if err := tx.Put(ctx, rowBucket(value.TableID), value.ID, encoded); err != nil {
		return fmt.Errorf("persist row: %w", err)
	}
	return nil
}

func getStored(ctx context.Context, tx store.Tx, tableID, rowID string) (storedRow, error) {
	encoded, err := tx.Get(ctx, rowBucket(tableID), rowID)
	if errors.Is(err, store.ErrNotFound) {
		return storedRow{}, rowError(result.CodeNotFound, fmt.Sprintf("row %q was not found", rowID))
	}
	if err != nil {
		return storedRow{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var value storedRow
	if err := decoder.Decode(&value); err != nil {
		return storedRow{}, fmt.Errorf("decode row: %w", err)
	}
	if value.ID != rowID || value.TableID != tableID || value.DatabaseID == "" || value.Revision == 0 ||
		(value.State != StateLive && value.State != StateDeleted) {
		return storedRow{}, errors.New("stored row identity is corrupt")
	}
	return value, nil
}

func loadIndex(ctx context.Context, tx store.Tx, tableID string) ([]string, error) {
	encoded, err := tx.Get(ctx, rowIndexBucket, tableID)
	if errors.Is(err, store.ErrNotFound) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	var index []string
	if err := json.Unmarshal(encoded, &index); err != nil || index == nil {
		return nil, errors.New("row index is corrupt")
	}
	return index, nil
}

func saveIndex(ctx context.Context, tx store.Tx, tableID string, index []string) error {
	encoded, err := json.Marshal(index)
	if err != nil {
		return fmt.Errorf("encode row index: %w", err)
	}
	if err := tx.Put(ctx, rowIndexBucket, tableID, encoded); err != nil {
		return fmt.Errorf("persist row index: %w", err)
	}
	return nil
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
		return rowError(result.CodeCancelled, "row operation was cancelled")
	case errors.Is(err, context.DeadlineExceeded):
		return rowError(result.CodeDeadlineExceeded, "row operation exceeded its deadline")
	default:
		return rowError(result.CodeInternal, "row operation failed")
	}
}

type uuidSource struct{}

func (uuidSource) Next() (string, error) { return uuid.NewString(), nil }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }
