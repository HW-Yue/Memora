package sqlstore

import (
	"context"
	"strings"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/change"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/schemachangeplan"
	"github.com/HW-Yue/Memora/internal/security"
)

// ApplySchemaChangePlan applies a reviewed column change plan to the Catalog.
// Stored values are keyed by column ID, so renames and type changes never
// rewrite data rows.
func (db *DB) ApplySchemaChangePlan(ctx context.Context, databaseName, tableName string, plan schemachangeplan.Plan) (schemachangeplan.Receipt, error) {
	if err := schemachangeplan.Validate(plan); err != nil {
		return schemachangeplan.Receipt{}, err
	}
	if err := security.RequireApproval(ctx, security.ActionApplySchemaChange, strings.TrimPrefix(plan.Hash, "sha256:")); err != nil {
		return schemachangeplan.Receipt{}, err
	}
	receipt := schemachangeplan.Receipt{
		Version: schemachangeplan.ReceiptVersion, PlanID: plan.PlanID, PlanHash: plan.Hash,
		Status: "committed", AppliedActions: len(plan.Actions), Verified: true,
	}
	err := db.update(ctx, func(t *tx) error {
		database, err := t.resolveDatabase(ctx, databaseName)
		if err != nil {
			return err
		}
		table, ok := findTable(&database, tableName)
		if !ok {
			return catalogError(catalog.CodeNotFound, "table", tableName, "")
		}
		if err := security.RequireAnyDatabaseLevel(ctx, security.LevelStructural, databaseName, table.DatabaseID); err != nil {
			return err
		}
		if database.ID != plan.Scope.DatabaseID || table.ID != plan.Scope.TableID {
			return fail(result.CodePermissionDenied, "plan is outside the bound Table")
		}
		if plan.RowSnapshotHash != "" {
			rows, err := t.q().QueryContext(ctx, `SELECT row_id, revision FROM `+dataTable(table.ID)+` WHERE row_state = 'live'`)
			if err != nil {
				return err
			}
			live := map[string]uint64{}
			for rows.Next() {
				var id string
				var revision uint64
				if err := rows.Scan(&id, &revision); err != nil {
					rows.Close()
					return err
				}
				live[id] = revision
			}
			rows.Close()
			if len(live) != len(plan.RowGuards) {
				return fail(result.CodeRevisionConflict, "live Row set changed after Schema planning")
			}
			for _, guard := range plan.RowGuards {
				if live[guard.RowID] != guard.Revision {
					return fail(result.CodeRevisionConflict, "Row %q changed after Schema planning", guard.RowID)
				}
			}
		}
		updatedDatabase, updatedTable, err := schemachangeplan.ApplyToSnapshot(plan, database, *table, t.now)
		if err != nil {
			return err
		}
		t.claimAttribution(change.Metadata{Actor: plan.Actor, Source: fallback(plan.SourceEventID, "schema-plan"), Reason: plan.Reason})
		if err := t.saveTable(ctx, updatedTable); err != nil {
			return err
		}
		if err := t.saveDatabase(ctx, updatedDatabase); err != nil {
			return err
		}
		t.catalogChange(change.ObjectTable, change.OperationUpdate, database.ID, table.ID, table.ID,
			table.SchemaVersion, updatedTable.SchemaVersion, updatedTable.SchemaVersion)
		receipt.DatabaseRevision = updatedDatabase.SchemaVersion
		receipt.TableRevision = updatedTable.SchemaVersion
		sequence, err := t.commitSequence(ctx)
		receipt.ChangeSequence = sequence
		if proposal, ok := schemachangeplan.CompensationProposal(plan, updatedTable.SchemaVersion); ok {
			receipt.CompensationProposal = &proposal
		}
		return err
	})
	if err != nil {
		return schemachangeplan.Receipt{}, err
	}
	return receipt, nil
}

var _ = row.StateLive
