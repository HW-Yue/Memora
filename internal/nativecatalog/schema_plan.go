package nativecatalog

import (
	"context"
	"fmt"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/change"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/schemachangeplan"
)

// ApplySchemaChangePlan owns the Catalog write window. verify runs after the
// global authority is held and before any generation is staged, allowing the
// native mutation facade to compare raw Row guards without a race.
func (service *Service) ApplySchemaChangePlan(
	ctx context.Context,
	databaseName, tableName string,
	plan schemachangeplan.Plan,
	verify func(catalog.Table) error,
) (catalog.Table, uint64, error) {
	if service == nil || service.repository == nil {
		return catalog.Table{}, 0, fmt.Errorf("native Catalog service is incomplete")
	}
	if err := schemachangeplan.Validate(plan); err != nil {
		return catalog.Table{}, 0, err
	}
	if err := ctx.Err(); err != nil {
		return catalog.Table{}, 0, err
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.authority != nil {
		release, err := service.authority.BeginWrite(ctx)
		if err != nil {
			return catalog.Table{}, 0, err
		}
		defer release()
	}
	databases, err := service.repository.Read()
	if err != nil {
		return catalog.Table{}, 0, err
	}
	database, table, err := locateTable(databases, databaseName, tableName)
	if err != nil {
		return catalog.Table{}, 0, err
	}
	if database.ID != plan.Scope.DatabaseID || table.ID != plan.Scope.TableID {
		return catalog.Table{}, 0, &schemachangeplan.Error{
			Code: result.CodeRevisionConflict, Message: "bound Table changed after planning",
		}
	}
	if verify != nil {
		if err := verify(*table); err != nil {
			return catalog.Table{}, 0, err
		}
	}
	before := catalogRevisionMap(databases)
	updatedDatabase, updatedTable, err := schemachangeplan.ApplyToSnapshot(plan, *database, *table, service.clock.Now().UTC())
	if err != nil {
		return catalog.Table{}, 0, err
	}
	for index := range databases {
		if databases[index].ID == updatedDatabase.ID {
			databases[index] = updatedDatabase
		}
	}
	sequence, err := service.nextChangeSequence(ctx)
	if err != nil {
		return catalog.Table{}, 0, err
	}
	envelope, err := changeEnvelope(sequence, service.clock.Now().UTC(), before, databases, change.Metadata{
		Actor: plan.Actor, Source: plan.SourceEventID, Reason: plan.Reason,
	})
	if err != nil {
		return catalog.Table{}, 0, err
	}
	commit := func() error { return service.repository.WriteCommitted(databases, envelope) }
	if service.authority != nil {
		if err := service.authority.PublishCatalog(ctx, databases, commit); err != nil {
			return catalog.Table{}, 0, err
		}
	} else if err := commit(); err != nil {
		return catalog.Table{}, 0, err
	}
	return updatedTable, sequence, nil
}
