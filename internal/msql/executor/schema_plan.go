package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"

	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/schemachangeplan"
	"github.com/HW-Yue/Memora/internal/security"
)

const (
	maximumSchemaChangeProposalBytes = 128 * 1024
	maximumSchemaChangePlanBytes     = 512 * 1024
)

type schemaPlanApplier interface {
	ApplySchemaChangePlan(context.Context, string, string, schemachangeplan.Plan) (schemachangeplan.Receipt, error)
}

func (engine *Engine) planSchemaChange(
	ctx context.Context,
	statement *ast.PlanSchemaChangeStatement,
	bound bindings,
) (Output, error) {
	if statement == nil || statement.Proposal == nil {
		return Output{}, executeError(result.CodeValidation, "PLAN SCHEMA CHANGE is incomplete")
	}
	databaseName, _, table, err := engine.bindTable(ctx, statement.Table)
	if err != nil {
		return Output{}, err
	}
	database, err := engine.catalog.DescribeDatabase(ctx, databaseName)
	if err != nil {
		return Output{}, normalizeError(err)
	}
	value, err := evaluate(statement.Proposal, table, nil, bound)
	if err != nil {
		return Output{}, err
	}
	proposal, err := decodeSchemaChangeProposal(value)
	if err != nil {
		return Output{}, err
	}
	plan, err := schemachangeplan.Build(ctx, engine.rows, database, table, proposal)
	if err != nil {
		return Output{}, normalizeError(err)
	}
	return Output{
		Columns: []result.Column{
			{Name: "plan_id", Type: "ID"}, {Name: "status", Type: "TEXT"},
			{Name: "base_schema_hash", Type: "TEXT"}, {Name: "row_snapshot_hash", Type: "TEXT"},
			{Name: "plan_hash", Type: "TEXT"}, {Name: "schema_change_plan", Type: "JSON"},
		},
		Rows: []result.Row{{
			"plan_id": plan.PlanID, "status": string(plan.Status), "base_schema_hash": plan.BaseSchemaHash,
			"row_snapshot_hash": plan.RowSnapshotHash, "plan_hash": plan.Hash, "schema_change_plan": plan,
		}},
	}, nil
}

func decodeSchemaChangeProposal(value any) (schemachangeplan.Proposal, error) {
	if proposal, ok := value.(schemachangeplan.Proposal); ok {
		return proposal, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) == 0 || len(encoded) > maximumSchemaChangeProposalBytes {
		return schemachangeplan.Proposal{}, executeError(result.CodeValidation, "Schema change proposal is invalid or too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var proposal schemachangeplan.Proposal
	if err := decoder.Decode(&proposal); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return schemachangeplan.Proposal{}, executeError(result.CodeValidation, "Schema change proposal is invalid")
	}
	return proposal, nil
}

func (engine *Engine) applySchemaChange(
	ctx context.Context,
	statement *ast.ApplySchemaChangeStatement,
	bound bindings,
) (Output, error) {
	if statement == nil || statement.Plan == nil {
		return Output{}, executeError(result.CodeValidation, "APPLY SCHEMA CHANGE is incomplete")
	}
	databaseName, tableName, table, err := engine.bindTable(ctx, statement.Table)
	if err != nil {
		return Output{}, err
	}
	value, err := evaluate(statement.Plan, table, nil, bound)
	if err != nil {
		return Output{}, err
	}
	plan, err := decodeSchemaChangePlan(value)
	if err != nil {
		return Output{}, err
	}
	if err := schemachangeplan.Validate(plan); err != nil {
		return Output{}, normalizeError(err)
	}
	if err := security.RequireApproval(ctx, security.ActionApplySchemaChange, strings.TrimPrefix(plan.Hash, "sha256:")); err != nil {
		return Output{}, normalizeError(err)
	}
	applier, ok := engine.rows.(schemaPlanApplier)
	if !ok {
		return Output{}, executeError(result.CodeUnsupported, "Schema change execution is not supported by this backend")
	}
	receipt, err := applier.ApplySchemaChangePlan(ctx, databaseName, tableName, plan)
	if err != nil {
		return Output{}, normalizeError(err)
	}
	sequence := receipt.ChangeSequence
	return Output{
		Columns: []result.Column{
			{Name: "plan_id", Type: "ID"}, {Name: "status", Type: "TEXT"},
			{Name: "change_sequence", Type: "INTEGER"}, {Name: "table_revision", Type: "INTEGER"},
			{Name: "verified", Type: "BOOLEAN"}, {Name: "schema_change_receipt", Type: "JSON"},
		},
		Rows: []result.Row{{"plan_id": receipt.PlanID, "status": receipt.Status,
			"change_sequence": receipt.ChangeSequence, "table_revision": receipt.TableRevision,
			"verified": receipt.Verified, "schema_change_receipt": receipt}},
		AffectedRows: 1, CommitSequence: &sequence,
	}, nil
}

func decodeSchemaChangePlan(value any) (schemachangeplan.Plan, error) {
	if plan, ok := value.(schemachangeplan.Plan); ok {
		return plan, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) == 0 || len(encoded) > maximumSchemaChangePlanBytes {
		return schemachangeplan.Plan{}, executeError(result.CodeValidation, "Schema change plan is invalid or too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var plan schemachangeplan.Plan
	if err := decoder.Decode(&plan); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return schemachangeplan.Plan{}, executeError(result.CodeValidation, "Schema change plan is invalid")
	}
	return plan, nil
}
