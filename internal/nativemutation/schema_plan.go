package nativemutation

import (
	"context"
	"reflect"
	"strings"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/schemachangeplan"
	"github.com/HW-Yue/Memora/internal/security"
)

func (service *Service) ApplySchemaChangePlan(
	ctx context.Context, databaseName, tableName string, plan schemachangeplan.Plan,
) (schemachangeplan.Receipt, error) {
	if service == nil || service.Service == nil || service.catalog == nil || service.rows == nil {
		return schemachangeplan.Receipt{}, schemaPlanError(result.CodeInternal, "native Schema plan service is incomplete")
	}
	if err := schemachangeplan.Validate(plan); err != nil {
		return schemachangeplan.Receipt{}, err
	}
	if err := security.RequireApproval(ctx, security.ActionApplySchemaChange, strings.TrimPrefix(plan.Hash, "sha256:")); err != nil {
		return schemachangeplan.Receipt{}, err
	}
	table, err := service.catalog.DescribeTable(ctx, databaseName, tableName)
	if err != nil {
		return schemachangeplan.Receipt{}, err
	}
	if err := security.RequireAnyDatabase(ctx, databaseName, table.DatabaseID); err != nil {
		return schemachangeplan.Receipt{}, err
	}
	if plan.Scope.DatabaseID != table.DatabaseID || plan.Scope.TableID != table.ID ||
		(plan.Scope.Database != "" && !strings.EqualFold(plan.Scope.Database, databaseName)) ||
		(plan.Scope.Table != "" && !strings.EqualFold(plan.Scope.Table, tableName)) {
		return schemachangeplan.Receipt{}, schemaPlanError(result.CodePermissionDenied, "plan is outside the bound Table")
	}
	updated, sequence, err := service.catalog.ApplySchemaChangePlan(ctx, databaseName, tableName, plan, func(current catalog.Table) error {
		return service.revalidateSchemaRows(plan, current)
	})
	if err != nil {
		return schemachangeplan.Receipt{}, err
	}
	receipt := schemachangeplan.Receipt{Version: schemachangeplan.ReceiptVersion, PlanID: plan.PlanID,
		PlanHash: plan.Hash, Status: "committed", ChangeSequence: sequence,
		DatabaseRevision: plan.DatabaseRevision + 1, TableRevision: updated.SchemaVersion,
		AppliedActions: len(plan.Actions), Verified: true}
	observed, verificationErr := service.catalog.DescribeTable(ctx, databaseName, tableName)
	if verificationErr != nil || !sameSchemaTable(observed, updated) {
		receipt.Status, receipt.Verified, receipt.VerificationCode = "committed_unverified", false, "post_commit_catalog_mismatch"
	} else if proposal, ok := schemachangeplan.CompensationProposal(plan, updated.SchemaVersion); ok {
		receipt.CompensationProposal = &proposal
	}
	return receipt, nil
}

func sameSchemaTable(left, right catalog.Table) bool {
	left.Aliases, right.Aliases = append([]string{}, left.Aliases...), append([]string{}, right.Aliases...)
	left.ColumnSummaries, right.ColumnSummaries = nil, nil
	left.Columns, right.Columns = append([]catalog.Column(nil), left.Columns...), append([]catalog.Column(nil), right.Columns...)
	for index := range left.Columns {
		left.Columns[index].Aliases = append([]string{}, left.Columns[index].Aliases...)
	}
	for index := range right.Columns {
		right.Columns[index].Aliases = append([]string{}, right.Columns[index].Aliases...)
	}
	return reflect.DeepEqual(left, right)
}

func (service *Service) revalidateSchemaRows(plan schemachangeplan.Plan, table catalog.Table) error {
	if plan.RowSnapshotHash == "" {
		return nil
	}
	values, more, err := service.rows.List(table.DatabaseID, table.ID, 1000)
	if err != nil {
		return err
	}
	if more || len(values) != len(plan.RowGuards) {
		return schemaPlanError(result.CodeRevisionConflict, "live Row set changed after Schema planning")
	}
	for index, guard := range plan.RowGuards {
		if values[index].ID != guard.RowID || values[index].Revision != guard.Revision {
			return schemaPlanError(result.CodeRevisionConflict, "live Row revisions changed after Schema planning")
		}
	}
	return nil
}

func schemaPlanError(code result.Code, message string) error {
	return &ServiceError{Code: code, Message: "Schema plan: " + message}
}
