package row

import (
	"context"
)

func (service *Service) Update(ctx context.Context, databaseName, tableName, rowID string, changes map[string]any, options WriteOptions) (Row, error) {
	transaction, err := service.BeginTransaction(ctx)
	if err != nil {
		return Row{}, stableError(err)
	}
	defer func() { _ = transaction.Rollback() }()
	updated, err := transaction.Update(ctx, databaseName, tableName, rowID, changes, options)
	if err != nil {
		return Row{}, err
	}
	if err := transaction.Commit(); err != nil {
		return Row{}, err
	}
	return updated, nil
}

func (service *Service) Delete(ctx context.Context, databaseName, tableName, rowID string, options WriteOptions) (Row, error) {
	transaction, err := service.BeginTransaction(ctx)
	if err != nil {
		return Row{}, stableError(err)
	}
	defer func() { _ = transaction.Rollback() }()
	deleted, err := transaction.Delete(ctx, databaseName, tableName, rowID, options)
	if err != nil {
		return Row{}, err
	}
	if err := transaction.Commit(); err != nil {
		return Row{}, err
	}
	return deleted, nil
}
