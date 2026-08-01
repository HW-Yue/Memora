package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"

	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/routemutationplan"
	"github.com/HW-Yue/Memora/internal/security"
)

const (
	maximumRouteProposalBytes = 64 * 1024
	maximumRoutePlanBytes     = 512 * 1024
)

type routePlanApplier interface {
	ApplyRouteMutationPlan(context.Context, string, string, routemutationplan.Plan) (routemutationplan.Receipt, error)
}

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

func (engine *Engine) applyRouteMutation(
	ctx context.Context,
	statement *ast.ApplyRouteMutationStatement,
	bound bindings,
) (Output, error) {
	if statement == nil || statement.Plan == nil {
		return Output{}, executeError(result.CodeValidation, "APPLY ROUTE MUTATION is incomplete")
	}
	databaseName, tableName, table, err := engine.bindTable(ctx, statement.Table)
	if err != nil {
		return Output{}, err
	}
	value, err := evaluate(statement.Plan, table, nil, bound)
	if err != nil {
		return Output{}, err
	}
	plan, err := decodeRoutePlan(value)
	if err != nil {
		return Output{}, err
	}
	if err := routemutationplan.Validate(plan); err != nil {
		return Output{}, normalizeError(err)
	}
	if err := security.RequireApproval(
		ctx, security.ActionApplyRouteMutation, strings.TrimPrefix(plan.Hash, "sha256:"),
	); err != nil {
		return Output{}, normalizeError(err)
	}
	applier, ok := engine.rows.(routePlanApplier)
	if !ok {
		return Output{}, executeError(result.CodeUnsupported, "Route mutation execution is not supported by this backend")
	}
	receipt, err := applier.ApplyRouteMutationPlan(ctx, databaseName, tableName, plan)
	if err != nil {
		return Output{}, normalizeError(err)
	}
	sequence := receipt.ChangeSequence
	return Output{
		Columns: []result.Column{
			{Name: "plan_id", Type: "ID"}, {Name: "status", Type: "TEXT"},
			{Name: "change_sequence", Type: "INTEGER"}, {Name: "verified", Type: "BOOLEAN"},
			{Name: "route_mutation_receipt", Type: "JSON"},
		},
		Rows: []result.Row{{
			"plan_id": receipt.PlanID, "status": receipt.Status, "change_sequence": receipt.ChangeSequence,
			"verified": receipt.Verified, "route_mutation_receipt": receipt,
		}},
		AffectedRows: 1, CommitSequence: &sequence,
	}, nil
}

func decodeRoutePlan(value any) (routemutationplan.Plan, error) {
	if plan, ok := value.(routemutationplan.Plan); ok {
		return plan, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) == 0 || len(encoded) > maximumRoutePlanBytes {
		return routemutationplan.Plan{}, executeError(result.CodeValidation, "Route mutation plan is invalid or too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var plan routemutationplan.Plan
	if err := decoder.Decode(&plan); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return routemutationplan.Plan{}, executeError(result.CodeValidation, "Route mutation plan is invalid")
	}
	return plan, nil
}
