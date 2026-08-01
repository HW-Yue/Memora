package row

import (
	"context"
	"fmt"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/store"
)

func (service *Service) AsOfRevision(
	ctx context.Context,
	databaseName, tableName, rowID string,
	revision uint64,
) (Row, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	tx, err := service.store.Begin(ctx, store.ReadOnly)
	if err != nil {
		return Row{}, stableError(err)
	}
	defer func() { _ = tx.Rollback() }()
	table, err := service.catalog.DescribeTableIn(ctx, tx, databaseName, tableName)
	if err != nil {
		return Row{}, stableError(err)
	}
	record, err := service.history.GetRevisionIn(ctx, tx, table.ID, rowID, revision)
	if err != nil {
		return Row{}, stableError(err)
	}
	return projectHistory(table, record)
}

func (service *Service) AsOfCommit(
	ctx context.Context,
	databaseName, tableName, rowID string,
	commitSequence uint64,
) (Row, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	tx, err := service.store.Begin(ctx, store.ReadOnly)
	if err != nil {
		return Row{}, stableError(err)
	}
	defer func() { _ = tx.Rollback() }()
	table, err := service.catalog.DescribeTableIn(ctx, tx, databaseName, tableName)
	if err != nil {
		return Row{}, stableError(err)
	}
	record, err := service.history.AsOfCommitIn(ctx, tx, table.ID, rowID, commitSequence)
	if err != nil {
		return Row{}, stableError(err)
	}
	return projectHistory(table, record)
}

func (service *Service) HistoryPage(
	ctx context.Context,
	databaseName, tableName, rowID, cursor string,
	limit int,
) ([]history.Record, history.ReadPage, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	tx, err := service.store.Begin(ctx, store.ReadOnly)
	if err != nil {
		return nil, history.ReadPage{}, stableError(err)
	}
	defer func() { _ = tx.Rollback() }()
	table, err := service.catalog.DescribeTableIn(ctx, tx, databaseName, tableName)
	if err != nil {
		return nil, history.ReadPage{}, stableError(err)
	}
	records, err := service.history.ListIn(ctx, tx, table.ID, rowID)
	if err != nil {
		return nil, history.ReadPage{}, stableError(err)
	}
	reverseHistory(records)
	pageRecords, page, err := history.Paginate(table.DatabaseID+"\x00"+table.ID+"\x00"+rowID, cursor, limit, records)
	return pageRecords, page, stableError(err)
}

func (service *Service) Restore(
	ctx context.Context,
	databaseName, tableName, rowID string,
	targetRevision uint64,
	options WriteOptions,
) (Row, error) {
	transaction, err := service.BeginTransaction(ctx)
	if err != nil {
		return Row{}, stableError(err)
	}
	defer func() { _ = transaction.Rollback() }()
	restored, err := transaction.Restore(ctx, databaseName, tableName, rowID, targetRevision, options)
	if err != nil {
		return Row{}, err
	}
	if err := transaction.Commit(); err != nil {
		return Row{}, err
	}
	return restored, nil
}

func (transaction *Transaction) AsOfRevision(
	ctx context.Context,
	databaseName, tableName, rowID string,
	revision uint64,
) (Row, error) {
	table, err := transaction.DescribeTable(ctx, databaseName, tableName)
	if err != nil {
		return Row{}, err
	}
	record, err := transaction.service.history.GetRevisionIn(ctx, transaction.tx, table.ID, rowID, revision)
	if err != nil {
		return Row{}, stableError(err)
	}
	return projectHistory(table, record)
}

func (transaction *Transaction) AsOfCommit(
	ctx context.Context,
	databaseName, tableName, rowID string,
	commitSequence uint64,
) (Row, error) {
	table, err := transaction.DescribeTable(ctx, databaseName, tableName)
	if err != nil {
		return Row{}, err
	}
	record, err := transaction.service.history.AsOfCommitIn(ctx, transaction.tx, table.ID, rowID, commitSequence)
	if err != nil {
		return Row{}, stableError(err)
	}
	return projectHistory(table, record)
}

func (transaction *Transaction) HistoryPage(
	ctx context.Context,
	databaseName, tableName, rowID, cursor string,
	limit int,
) ([]history.Record, history.ReadPage, error) {
	table, err := transaction.DescribeTable(ctx, databaseName, tableName)
	if err != nil {
		return nil, history.ReadPage{}, err
	}
	records, err := transaction.service.history.ListIn(ctx, transaction.tx, table.ID, rowID)
	if err != nil {
		return nil, history.ReadPage{}, stableError(err)
	}
	reverseHistory(records)
	pageRecords, page, err := history.Paginate(table.DatabaseID+"\x00"+table.ID+"\x00"+rowID, cursor, limit, records)
	return pageRecords, page, stableError(err)
}

func reverseHistory(records []history.Record) {
	for left, right := 0, len(records)-1; left < right; left, right = left+1, right-1 {
		records[left], records[right] = records[right], records[left]
	}
}

func (transaction *Transaction) Restore(
	ctx context.Context,
	databaseName, tableName, rowID string,
	targetRevision uint64,
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
	if options.ExpectedRevision == 0 {
		return Row{}, rowError(result.CodeValidation, "RESTORE requires expected revision")
	}
	if stored.Revision != options.ExpectedRevision {
		return Row{}, revisionError("row", options.ExpectedRevision, stored.Revision)
	}
	target, err := transaction.service.history.GetRevisionIn(
		ctx, transaction.tx, table.ID, rowID, targetRevision,
	)
	if err != nil {
		return Row{}, stableError(err)
	}
	values, err := validateHistoryValues(table, target.Values)
	if err != nil {
		return Row{}, stableError(err)
	}
	state := State(target.State)
	if state != StateLive && state != StateDeleted {
		return Row{}, rowError(result.CodeInternal, "history Row state is invalid")
	}
	stored.Values = values
	stored.State = state
	stored.SchemaVersion = table.SchemaVersion
	stored.Revision++
	commitSequence, err := transaction.ensureCommitSequence(ctx)
	if err != nil {
		return Row{}, err
	}
	stored.CommitSequence = commitSequence
	stored.UpdatedAt = transaction.service.clock.Now().UTC()
	if err := putStored(ctx, transaction.tx, stored); err != nil {
		return Row{}, stableError(err)
	}
	if stored.State == StateDeleted {
		if err := transaction.invalidateRouterMemberships(ctx, stored); err != nil {
			return Row{}, err
		}
	} else if options.RouteLeafIDs != nil {
		if err := transaction.replaceRouterMemberships(ctx, stored, options.RouteLeafIDs); err != nil {
			return Row{}, err
		}
	}
	if err := transaction.appendHistory(ctx, stored, history.OperationCompensate, options.Metadata); err != nil {
		return Row{}, err
	}
	projected, err := project(table, stored)
	return projected, stableError(err)
}

func projectHistory(table catalog.Table, record history.Record) (Row, error) {
	projected, err := project(table, storedRow{
		ID: record.RowID, DatabaseID: record.DatabaseID, TableID: record.TableID,
		SchemaVersion: record.SchemaVersion, Revision: record.Revision,
		CommitSequence: record.CommitSequence, State: State(record.State),
		Values: record.Values, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
	})
	return projected, stableError(err)
}

func validateHistoryValues(table catalog.Table, snapshot map[string]any) (map[string]any, error) {
	values := make(map[string]any, len(table.Columns))
	for _, column := range table.Columns {
		value, exists := snapshot[column.ID]
		if !exists {
			value = nil
		}
		normalized, err := column.Validate(value)
		if err != nil {
			return nil, fmt.Errorf("restore column %q: %w", column.Name, err)
		}
		values[column.ID] = normalized
	}
	return values, nil
}
