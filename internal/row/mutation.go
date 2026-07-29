package row

import (
	"context"
	"fmt"

	"github.com/HW-Yue/Memora/internal/result"
)

func (service *Service) Update(ctx context.Context, databaseName, tableName, rowID string, changes map[string]any, options WriteOptions) (Row, error) {
	if len(changes) == 0 {
		return Row{}, rowError(result.CodeValidation, "UPDATE requires at least one changed column")
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	tx, table, err := service.beginWrite(ctx, databaseName, tableName, options)
	if err != nil {
		return Row{}, stableError(err)
	}
	defer func() { _ = tx.Rollback() }()
	stored, err := getStored(ctx, tx, table.ID, rowID)
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
	stored.UpdatedAt = service.clock.Now().UTC()
	if err := putStored(ctx, tx, stored); err != nil {
		return Row{}, stableError(err)
	}
	if err := tx.Commit(); err != nil {
		return Row{}, stableError(err)
	}
	projected, err := project(table, stored)
	return projected, stableError(err)
}

func (service *Service) Delete(ctx context.Context, databaseName, tableName, rowID string, options WriteOptions) (Row, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	tx, table, err := service.beginWrite(ctx, databaseName, tableName, options)
	if err != nil {
		return Row{}, stableError(err)
	}
	defer func() { _ = tx.Rollback() }()
	stored, err := getStored(ctx, tx, table.ID, rowID)
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
	stored.UpdatedAt = service.clock.Now().UTC()
	if err := putStored(ctx, tx, stored); err != nil {
		return Row{}, stableError(err)
	}
	if err := tx.Commit(); err != nil {
		return Row{}, stableError(err)
	}
	projected, err := project(table, stored)
	return projected, stableError(err)
}
