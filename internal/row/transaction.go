package row

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/store"
)

// Transaction exposes Row and Catalog operations over one Store transaction.
// A transaction owns the Service write lock until Commit or Rollback.
type Transaction struct {
	service *Service
	tx      store.Tx

	finishMu sync.Mutex
	done     bool
}

func (service *Service) BeginTransaction(ctx context.Context) (*Transaction, error) {
	service.mu.Lock()
	tx, err := service.store.Begin(ctx, store.ReadWrite)
	if err != nil {
		service.mu.Unlock()
		return nil, stableError(err)
	}
	return &Transaction{service: service, tx: tx}, nil
}

func (transaction *Transaction) DescribeTable(ctx context.Context, databaseName, tableName string) (catalog.Table, error) {
	table, err := transaction.service.catalog.DescribeTableIn(ctx, transaction.tx, databaseName, tableName)
	return table, stableError(err)
}

func (transaction *Transaction) Get(ctx context.Context, databaseName, tableName, rowID string) (Row, error) {
	value, err := transaction.get(ctx, databaseName, tableName, rowID, false)
	return value, stableError(err)
}

func (transaction *Transaction) GetIncludingDeleted(ctx context.Context, databaseName, tableName, rowID string) (Row, error) {
	value, err := transaction.get(ctx, databaseName, tableName, rowID, true)
	return value, stableError(err)
}

func (transaction *Transaction) get(ctx context.Context, databaseName, tableName, rowID string, includeDeleted bool) (Row, error) {
	table, err := transaction.DescribeTable(ctx, databaseName, tableName)
	if err != nil {
		return Row{}, err
	}
	stored, err := getStored(ctx, transaction.tx, table.ID, rowID)
	if err != nil {
		return Row{}, err
	}
	if stored.State == StateDeleted && !includeDeleted {
		return Row{}, rowError(result.CodeNotFound, fmt.Sprintf("row %q was not found", rowID))
	}
	return project(table, stored)
}

func (transaction *Transaction) List(ctx context.Context, databaseName, tableName string, limit int) ([]Row, error) {
	rows, _, err := transaction.ListPage(ctx, databaseName, tableName, limit)
	return rows, err
}

func (transaction *Transaction) ListPage(ctx context.Context, databaseName, tableName string, limit int) ([]Row, bool, error) {
	if limit < 1 || limit > maxListRows {
		return nil, false, rowError(result.CodeValidation, fmt.Sprintf("row list limit must be between 1 and %d", maxListRows))
	}
	table, err := transaction.DescribeTable(ctx, databaseName, tableName)
	if err != nil {
		return nil, false, err
	}
	index, err := loadIndex(ctx, transaction.tx, table.ID)
	if err != nil {
		return nil, false, stableError(err)
	}
	rows := make([]Row, 0, min(limit, len(index)))
	for _, rowID := range index {
		stored, err := getStored(ctx, transaction.tx, table.ID, rowID)
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

func (transaction *Transaction) Insert(
	ctx context.Context,
	databaseName, tableName string,
	values map[string]any,
	options WriteOptions,
) (Row, error) {
	table, err := transaction.tableForWrite(ctx, databaseName, tableName, options)
	if err != nil {
		return Row{}, stableError(err)
	}
	encodedValues, err := validateValues(table, values, nil)
	if err != nil {
		return Row{}, stableError(err)
	}
	id, err := transaction.service.nextID()
	if err != nil {
		return Row{}, stableError(err)
	}
	if _, err := transaction.tx.Get(ctx, rowBucket(table.ID), id); err == nil {
		return Row{}, stableError(rowError(result.CodeAlreadyExists, "allocated row ID already exists"))
	} else if !errors.Is(err, store.ErrNotFound) {
		return Row{}, stableError(err)
	}
	now := transaction.service.clock.Now().UTC()
	stored := storedRow{
		ID: id, DatabaseID: table.DatabaseID, TableID: table.ID,
		SchemaVersion: table.SchemaVersion, Revision: 1, State: StateLive,
		Values: encodedValues, CreatedAt: now, UpdatedAt: now,
	}
	if err := putStored(ctx, transaction.tx, stored); err != nil {
		return Row{}, stableError(err)
	}
	index, err := loadIndex(ctx, transaction.tx, table.ID)
	if err != nil {
		return Row{}, stableError(err)
	}
	index = append(index, id)
	if err := saveIndex(ctx, transaction.tx, table.ID, index); err != nil {
		return Row{}, stableError(err)
	}
	projected, err := project(table, stored)
	return projected, stableError(err)
}

func (transaction *Transaction) Update(
	ctx context.Context,
	databaseName, tableName, rowID string,
	changes map[string]any,
	options WriteOptions,
) (Row, error) {
	if len(changes) == 0 {
		return Row{}, rowError(result.CodeValidation, "UPDATE requires at least one changed column")
	}
	table, err := transaction.tableForWrite(ctx, databaseName, tableName, options)
	if err != nil {
		return Row{}, stableError(err)
	}
	stored, err := getStored(ctx, transaction.tx, table.ID, rowID)
	if err != nil {
		return Row{}, stableError(err)
	}
	if stored.State == StateDeleted {
		return Row{}, rowError(result.CodeNotFound, fmt.Sprintf("row %q was not found", rowID))
	}
	if options.ExpectedRevision == 0 {
		return Row{}, rowError(result.CodeValidation, "UPDATE requires expected revision")
	}
	if stored.Revision != options.ExpectedRevision {
		return Row{}, revisionError("row", options.ExpectedRevision, stored.Revision)
	}
	encodedValues, err := validateValues(table, changes, stored.Values)
	if err != nil {
		return Row{}, stableError(err)
	}
	stored.Values = encodedValues
	stored.SchemaVersion = table.SchemaVersion
	stored.Revision++
	stored.UpdatedAt = transaction.service.clock.Now().UTC()
	if err := putStored(ctx, transaction.tx, stored); err != nil {
		return Row{}, stableError(err)
	}
	projected, err := project(table, stored)
	return projected, stableError(err)
}

func (transaction *Transaction) Delete(
	ctx context.Context,
	databaseName, tableName, rowID string,
	options WriteOptions,
) (Row, error) {
	table, err := transaction.tableForWrite(ctx, databaseName, tableName, options)
	if err != nil {
		return Row{}, stableError(err)
	}
	stored, err := getStored(ctx, transaction.tx, table.ID, rowID)
	if err != nil {
		return Row{}, stableError(err)
	}
	if stored.State == StateDeleted {
		return Row{}, rowError(result.CodeNotFound, fmt.Sprintf("row %q was not found", rowID))
	}
	if options.ExpectedRevision == 0 {
		return Row{}, rowError(result.CodeValidation, "DELETE requires expected revision")
	}
	if stored.Revision != options.ExpectedRevision {
		return Row{}, revisionError("row", options.ExpectedRevision, stored.Revision)
	}
	stored.State = StateDeleted
	stored.SchemaVersion = table.SchemaVersion
	stored.Revision++
	stored.UpdatedAt = transaction.service.clock.Now().UTC()
	if err := putStored(ctx, transaction.tx, stored); err != nil {
		return Row{}, stableError(err)
	}
	projected, err := project(table, stored)
	return projected, stableError(err)
}

func (transaction *Transaction) tableForWrite(
	ctx context.Context,
	databaseName, tableName string,
	options WriteOptions,
) (catalog.Table, error) {
	if options.ExpectedSchemaVersion == 0 {
		return catalog.Table{}, rowError(result.CodeValidation, "write requires expected schema version")
	}
	table, err := transaction.DescribeTable(ctx, databaseName, tableName)
	if err != nil {
		return catalog.Table{}, err
	}
	if table.SchemaVersion != options.ExpectedSchemaVersion {
		return catalog.Table{}, revisionError("schema", options.ExpectedSchemaVersion, table.SchemaVersion)
	}
	return table, nil
}

func (transaction *Transaction) Commit() error {
	return transaction.finish(true)
}

func (transaction *Transaction) Rollback() error {
	return transaction.finish(false)
}

func (transaction *Transaction) finish(commit bool) error {
	transaction.finishMu.Lock()
	defer transaction.finishMu.Unlock()
	if transaction.done {
		return nil
	}
	transaction.done = true
	var err error
	if commit {
		err = transaction.tx.Commit()
	} else {
		err = transaction.tx.Rollback()
	}
	transaction.service.mu.Unlock()
	return stableError(err)
}
