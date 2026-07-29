package row

import (
	"context"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/logical"
	"github.com/HW-Yue/Memora/internal/mechanicalindex"
	"github.com/HW-Yue/Memora/internal/result"
)

func (service *Service) LookupMechanicalTerm(
	ctx context.Context,
	databaseID, term string,
	limit int,
) ([]mechanicalindex.Posting, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	values, err := service.mechanicalIndex.Lookup(ctx, databaseID, term, limit)
	return values, stableError(err)
}

func (transaction *Transaction) LookupMechanicalTerm(
	ctx context.Context,
	databaseID, term string,
	limit int,
) ([]mechanicalindex.Posting, error) {
	values, err := transaction.service.mechanicalIndex.LookupIn(
		ctx, transaction.tx, databaseID, term, limit,
	)
	return values, stableError(err)
}

func (service *Service) RebuildMechanicalIndex(
	ctx context.Context,
	databaseName, tableName, rowID string,
) error {
	transaction, err := service.BeginTransaction(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback() }()
	if err := transaction.RebuildMechanicalIndex(ctx, databaseName, tableName, rowID); err != nil {
		return err
	}
	return transaction.Commit()
}

func (transaction *Transaction) RebuildMechanicalIndex(
	ctx context.Context,
	databaseName, tableName, rowID string,
) error {
	table, err := transaction.DescribeTable(ctx, databaseName, tableName)
	if err != nil {
		return err
	}
	stored, err := getStored(ctx, transaction.tx, table.ID, rowID)
	if err != nil {
		return stableError(err)
	}
	if stored.State == StateDeleted {
		return rowError(result.CodeNotFound, "cannot rebuild mechanical index for a deleted Row")
	}
	_, err = transaction.service.mechanicalIndex.RebuildIn(
		ctx, transaction.tx, mechanicalIndexLocator(stored), mechanicalTextFields(table, stored),
	)
	return stableError(err)
}

func (transaction *Transaction) replaceMechanicalIndex(
	ctx context.Context,
	table catalog.Table,
	stored storedRow,
) error {
	_, err := transaction.service.mechanicalIndex.ReplaceIn(
		ctx, transaction.tx, mechanicalIndexLocator(stored), mechanicalTextFields(table, stored),
	)
	return stableError(err)
}

func mechanicalIndexLocator(stored storedRow) mechanicalindex.Locator {
	return mechanicalindex.Locator{
		DatabaseID: stored.DatabaseID, TableID: stored.TableID,
		RowID: stored.ID, Revision: stored.Revision,
	}
}

func mechanicalTextFields(table catalog.Table, stored storedRow) []string {
	fields := make([]string, 0, len(table.Columns))
	for _, column := range table.Columns {
		definition, err := logical.ParseDeclaration(column.Type)
		if err != nil || definition.Kind != logical.KindText {
			continue
		}
		if value, ok := stored.Values[column.ID].(string); ok {
			fields = append(fields, value)
		}
	}
	return fields
}
