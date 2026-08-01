package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"io"

	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/schemachangeplan"
)

const maximumSchemaChangeProposalBytes = 128 * 1024

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
