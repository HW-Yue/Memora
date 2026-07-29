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
	databaseName, tableName, rowID string,
	limit int,
) ([]history.Record, bool, error) {
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
	records, hasMore, err := service.history.ListPageIn(ctx, tx, table.ID, rowID, limit)
	return records, hasMore, stableError(err)
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
	databaseName, tableName, rowID string,
	limit int,
) ([]history.Record, bool, error) {
	table, err := transaction.DescribeTable(ctx, databaseName, tableName)
	if err != nil {
		return nil, false, err
	}
	records, hasMore, err := transaction.service.history.ListPageIn(
		ctx, transaction.tx, table.ID, rowID, limit,
	)
	return records, hasMore, stableError(err)
}

func (transaction *Transaction) Restore(
	ctx context.Context,
	databaseName, tableName, rowID string,
	targetRevision uint64,
	options WriteOptions,
) (Row, error) {
	if err := transaction.validateIndexTerms(options.IndexTerms); err != nil {
		return Row{}, err
	}
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
		if _, err := transaction.service.agentIndex.InvalidateIn(
			ctx, transaction.tx, agentIndexLocator(stored),
		); err != nil {
			return Row{}, stableError(err)
		}
	} else if options.IndexTerms == nil {
		if _, err := transaction.service.agentIndex.InvalidateIn(
			ctx, transaction.tx, agentIndexLocator(stored),
		); err != nil {
			return Row{}, stableError(err)
		}
	} else if err := transaction.replaceAgentIndex(ctx, stored, options.IndexTerms); err != nil {
		return Row{}, err
	}
	if stored.State == StateDeleted {
		if _, err := transaction.service.mechanicalIndex.InvalidateIn(
			ctx, transaction.tx, mechanicalIndexLocator(stored),
		); err != nil {
			return Row{}, stableError(err)
		}
	} else if err := transaction.replaceMechanicalIndex(ctx, table, stored); err != nil {
		return Row{}, err
	}
	if stored.State == StateDeleted {
		if err := transaction.invalidateRouterMemberships(ctx, stored); err != nil {
			return Row{}, err
		}
	} else if options.RouteLeafIDs == nil {
		if err := transaction.invalidateRouterMemberships(ctx, stored); err != nil {
			return Row{}, err
		}
	} else if err := transaction.replaceRouterMemberships(ctx, stored, options.RouteLeafIDs); err != nil {
		return Row{}, err
	}
	if stored.State == StateDeleted {
		if err := transaction.clearReindex(ctx, stored); err != nil {
			return Row{}, err
		}
	} else {
		needAgent, needRouter := options.IndexTerms == nil, options.RouteLeafIDs == nil
		if needAgent || needRouter {
			if err := transaction.enqueueReindex(ctx, stored, needAgent, needRouter); err != nil {
				return Row{}, err
			}
		} else if err := transaction.clearReindex(ctx, stored); err != nil {
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
