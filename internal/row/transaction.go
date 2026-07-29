package row

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/HW-Yue/Memora/internal/agentindex"
	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/relation"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/store"
)

// Transaction exposes Row and Catalog operations over one Store transaction.
// A transaction owns the Service write lock until Commit or Rollback.
type Transaction struct {
	service *Service
	tx      store.Tx

	finishMu       sync.Mutex
	done           bool
	commitSequence uint64
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
	if err := transaction.validateIndexTerms(options.IndexTerms); err != nil {
		return Row{}, err
	}
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
	commitSequence, err := transaction.ensureCommitSequence(ctx)
	if err != nil {
		return Row{}, err
	}
	stored := storedRow{
		ID: id, DatabaseID: table.DatabaseID, TableID: table.ID,
		SchemaVersion: table.SchemaVersion, Revision: 1,
		CommitSequence: commitSequence, State: StateLive,
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
	if err := transaction.replaceAgentIndex(ctx, stored, options.IndexTerms); err != nil {
		return Row{}, err
	}
	if err := transaction.replaceMechanicalIndex(ctx, table, stored); err != nil {
		return Row{}, err
	}
	if err := transaction.replaceRouterMemberships(ctx, stored, options.RouteLeafIDs); err != nil {
		return Row{}, err
	}
	if err := transaction.appendHistory(ctx, stored, history.OperationInsert, options.Metadata); err != nil {
		return Row{}, err
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
	commitSequence, err := transaction.ensureCommitSequence(ctx)
	if err != nil {
		return Row{}, err
	}
	stored.CommitSequence = commitSequence
	stored.UpdatedAt = transaction.service.clock.Now().UTC()
	if err := putStored(ctx, transaction.tx, stored); err != nil {
		return Row{}, stableError(err)
	}
	if err := transaction.replaceAgentIndex(ctx, stored, options.IndexTerms); err != nil {
		return Row{}, err
	}
	if err := transaction.replaceMechanicalIndex(ctx, table, stored); err != nil {
		return Row{}, err
	}
	if err := transaction.replaceRouterMemberships(ctx, stored, options.RouteLeafIDs); err != nil {
		return Row{}, err
	}
	if err := transaction.appendHistory(ctx, stored, history.OperationUpdate, options.Metadata); err != nil {
		return Row{}, err
	}
	projected, err := project(table, stored)
	return projected, stableError(err)
}

func relationEndpoint(stored storedRow) relation.Endpoint {
	return relation.Endpoint{
		DatabaseID: stored.DatabaseID, TableID: stored.TableID, RowID: stored.ID,
	}
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
	commitSequence, err := transaction.ensureCommitSequence(ctx)
	if err != nil {
		return Row{}, err
	}
	stored.CommitSequence = commitSequence
	stored.UpdatedAt = transaction.service.clock.Now().UTC()
	if err := transaction.invalidateRelations(ctx, relationEndpoint(stored), commitSequence); err != nil {
		return Row{}, err
	}
	if _, err := transaction.service.agentIndex.InvalidateIn(
		ctx, transaction.tx, agentIndexLocator(stored),
	); err != nil {
		return Row{}, stableError(err)
	}
	if _, err := transaction.service.mechanicalIndex.InvalidateIn(
		ctx, transaction.tx, mechanicalIndexLocator(stored),
	); err != nil {
		return Row{}, stableError(err)
	}
	if err := transaction.invalidateRouterMemberships(ctx, stored); err != nil {
		return Row{}, err
	}
	if err := putStored(ctx, transaction.tx, stored); err != nil {
		return Row{}, stableError(err)
	}
	if err := transaction.appendHistory(ctx, stored, history.OperationDelete, options.Metadata); err != nil {
		return Row{}, err
	}
	projected, err := project(table, stored)
	return projected, stableError(err)
}

func (transaction *Transaction) validateIndexTerms(terms []string) error {
	if terms == nil {
		return nil
	}
	return stableError(transaction.service.agentIndex.ValidateTerms(terms))
}

func (transaction *Transaction) replaceAgentIndex(
	ctx context.Context,
	stored storedRow,
	terms []string,
) error {
	if terms == nil {
		return nil
	}
	_, err := transaction.service.agentIndex.ReplaceIn(
		ctx, transaction.tx, agentIndexLocator(stored), terms,
	)
	return stableError(err)
}

func agentIndexLocator(stored storedRow) agentindex.Locator {
	return agentindex.Locator{
		DatabaseID: stored.DatabaseID, TableID: stored.TableID,
		RowID: stored.ID, Revision: stored.Revision,
	}
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

func (transaction *Transaction) ensureCommitSequence(ctx context.Context) (uint64, error) {
	if transaction.commitSequence != 0 {
		return transaction.commitSequence, nil
	}
	sequence, err := transaction.service.history.ReserveCommitSequence(ctx, transaction.tx)
	if err != nil {
		return 0, stableError(err)
	}
	transaction.commitSequence = sequence
	return sequence, nil
}

func (transaction *Transaction) appendHistory(
	ctx context.Context,
	stored storedRow,
	operation history.Operation,
	metadata WriteMetadata,
) error {
	metadata = normalizedMetadata(metadata)
	return stableError(transaction.service.history.AppendIn(ctx, transaction.tx, history.Record{
		DatabaseID: stored.DatabaseID, TableID: stored.TableID, RowID: stored.ID,
		SchemaVersion: stored.SchemaVersion, Revision: stored.Revision,
		CommitSequence: stored.CommitSequence, Operation: operation,
		State: string(stored.State), Values: stored.Values,
		Actor: metadata.Actor, Source: metadata.Source, Reason: metadata.Reason,
		CreatedAt: stored.CreatedAt, UpdatedAt: stored.UpdatedAt, RecordedAt: stored.UpdatedAt,
	}))
}

func normalizedMetadata(metadata WriteMetadata) WriteMetadata {
	if strings.TrimSpace(metadata.Actor) == "" {
		metadata.Actor = "system:direct-api"
	}
	if strings.TrimSpace(metadata.Source) == "" {
		metadata.Source = "direct-api"
	}
	if strings.TrimSpace(metadata.Reason) == "" {
		metadata.Reason = "row mutation"
	}
	return metadata
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
