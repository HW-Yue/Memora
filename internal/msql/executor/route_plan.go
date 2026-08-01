package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"io"

	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/routemutationplan"
)

const maximumRouteProposalBytes = 64 * 1024

func (engine *Engine) planRouteMutation(
	ctx context.Context,
	statement *ast.PlanRouteMutationStatement,
	bound bindings,
) (Output, error) {
	if statement == nil || statement.Proposal == nil {
		return Output{}, executeError(result.CodeValidation, "PLAN ROUTE MUTATION is incomplete")
	}
	databaseName, tableName, table, err := engine.bindTable(ctx, statement.Table)
	if err != nil {
		return Output{}, err
	}
	value, err := evaluate(statement.Proposal, table, nil, bound)
	if err != nil {
		return Output{}, err
	}
	proposal, err := decodeRouteProposal(value)
	if err != nil {
		return Output{}, err
	}
	source, ok := engine.rows.(routemutationplan.Source)
	if !ok {
		return Output{}, executeError(result.CodeUnsupported, "Route mutation planning is not supported by this backend")
	}
	plan, err := routemutationplan.Build(ctx, source, routemutationplan.Scope{
		DatabaseID: table.DatabaseID, Database: databaseName, TableID: table.ID, Table: tableName,
	}, proposal)
	if err != nil {
		return Output{}, normalizeError(err)
	}
	return Output{
		Columns: []result.Column{
			{Name: "plan_id", Type: "ID"},
			{Name: "status", Type: "TEXT"},
			{Name: "base_snapshot_hash", Type: "TEXT"},
			{Name: "plan_hash", Type: "TEXT"},
			{Name: "route_mutation_plan", Type: "JSON"},
		},
		Rows: []result.Row{{
			"plan_id": plan.PlanID, "status": string(plan.Status),
			"base_snapshot_hash": plan.BaseSnapshotHash, "plan_hash": plan.Hash,
			"route_mutation_plan": plan,
		}},
	}, nil
}

func decodeRouteProposal(value any) (routemutationplan.Proposal, error) {
	if proposal, ok := value.(routemutationplan.Proposal); ok {
		return proposal, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) == 0 || len(encoded) > maximumRouteProposalBytes {
		return routemutationplan.Proposal{}, executeError(result.CodeValidation, "Route mutation proposal is invalid or too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var proposal routemutationplan.Proposal
	if err := decoder.Decode(&proposal); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return routemutationplan.Proposal{}, executeError(result.CodeValidation, "Route mutation proposal is invalid")
	}
	return proposal, nil
}
