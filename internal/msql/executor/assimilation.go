package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/msql/parser"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/security"
	protocolmsql "github.com/HW-Yue/Memora/protocol/msql"
)

func (engine *Engine) executeAssimilation(
	ctx context.Context,
	statement *ast.AssimilationStatement,
	bound bindings,
) (Output, error) {
	if statement == nil || statement.Value == nil || len(statement.Database.Parts) != 1 {
		return Output{}, executeError(result.CodeValidation, "assimilation statement is incomplete")
	}
	authorization, err := security.RequireAuthorization(ctx)
	if err != nil {
		return Output{}, normalizeError(err)
	}
	databaseReference := statement.Database.Parts[0].Value
	database, err := engine.catalog.DescribeDatabase(ctx, databaseReference)
	if err != nil {
		return Output{}, normalizeError(err)
	}
	value, err := evaluate(statement.Value, catalog.Table{}, nil, bound)
	if err != nil {
		return Output{}, err
	}

	switch statement.Action {
	case "REVIEW":
		proposal, err := decodeAssimilationProposal(value)
		if err != nil {
			return Output{}, err
		}
		plan, err := engine.reviewAssimilationProposal(databaseReference, database, authorization.Actor, proposal)
		if err != nil {
			return Output{}, err
		}
		return Output{
			Columns: []result.Column{
				{Name: "plan_id", Type: "ID"}, {Name: "status", Type: "TEXT"},
				{Name: "plan_sha256", Type: "TEXT"}, {Name: "assimilation_plan", Type: "JSON"},
			},
			Rows: []result.Row{{
				"plan_id": plan.PlanID, "status": plan.Status,
				"plan_sha256": plan.SHA256, "assimilation_plan": plan,
			}},
		}, nil
	case "SUBMIT":
		plan, err := decodeAssimilationPlan(value)
		if err != nil {
			return Output{}, err
		}
		reviewed, err := engine.reviewAssimilationProposal(
			databaseReference, database, authorization.Actor, plan.Proposal,
		)
		if err != nil || reviewed.SHA256 != plan.SHA256 || plan.Validate() != nil {
			return Output{}, executeError(result.CodeValidation, "assimilation plan is invalid or no longer reviewable")
		}
		if err := security.RequireApproval(
			ctx, protocolmsql.AssimilationSubmitAction, strings.TrimPrefix(plan.SHA256, "sha256:"),
		); err != nil {
			return Output{}, normalizeError(err)
		}
		if engine.assimilation == nil {
			return Output{}, executeError(result.CodeUnsupported, "assimilation submitter is not configured")
		}
		receipt, err := engine.assimilation.SubmitAssimilation(ctx, plan)
		if err != nil {
			return Output{}, normalizeError(err)
		}
		if receipt.Validate() != nil || receipt.PlanID != plan.PlanID ||
			receipt.PlanSHA256 != plan.SHA256 || !strings.EqualFold(receipt.Database, database.Name) {
			return Output{}, executeError(result.CodeInternal, "assimilation submitter returned an invalid receipt")
		}
		return assimilationReceiptOutput(receipt, true), nil
	case "RECEIPT":
		receiptID, ok := value.(string)
		if !ok || strings.TrimSpace(receiptID) == "" || len(receiptID) > 200 {
			return Output{}, executeError(result.CodeValidation, "bounded assimilation receipt ID is required")
		}
		if engine.assimilation == nil {
			return Output{}, executeError(result.CodeUnsupported, "assimilation receipt store is not configured")
		}
		receipt, err := engine.assimilation.AssimilationReceipt(ctx, database.Name, receiptID)
		if err != nil {
			return Output{}, normalizeError(err)
		}
		if receipt.Validate() != nil || receipt.ReceiptID != receiptID ||
			!strings.EqualFold(receipt.Database, database.Name) {
			return Output{}, executeError(result.CodeInternal, "assimilation receipt store returned an invalid receipt")
		}
		return assimilationReceiptOutput(receipt, false), nil
	default:
		return Output{}, executeError(result.CodeUnsupported, "assimilation action is not supported")
	}
}

func (engine *Engine) reviewAssimilationProposal(
	databaseReference string,
	database catalog.Database,
	actor string,
	proposal protocolmsql.AssimilationProposal,
) (protocolmsql.AssimilationPlan, error) {
	if proposal.Validate() != nil || proposal.Author != actor ||
		!assimilationDatabaseMatches(proposal.Database, databaseReference, database) {
		return protocolmsql.AssimilationPlan{}, executeError(result.CodeValidation, "assimilation proposal identity, author, coverage, or source is invalid")
	}
	proposal.Database = database.Name
	for _, statement := range proposal.Statements {
		document, err := parser.Parse(statement.MSQL)
		if err != nil || !mutationStatement(document.Statement) ||
			statementRiskLevel(document.Statement) != security.LevelWrite || document.Statement.Assimilation != nil {
			return protocolmsql.AssimilationPlan{}, executeError(
				result.CodeValidation, "assimilation proposal statement is not an allowed L1 data mutation",
			)
		}
		databases := statementDatabaseNames(document.Statement)
		if len(databases) == 0 {
			return protocolmsql.AssimilationPlan{}, executeError(result.CodeValidation, "assimilation mutation must use a qualified target Database")
		}
		for _, nestedDatabase := range databases {
			if !assimilationDatabaseMatches(nestedDatabase, databaseReference, database) {
				return protocolmsql.AssimilationPlan{}, executeError(result.CodePermissionDenied, "assimilation mutation crosses its target Database")
			}
		}
		parameters := assimilationParameters(statement.Parameters)
		if _, err := bindParameters(document.Statement, parameters); err != nil {
			return protocolmsql.AssimilationPlan{}, executeError(result.CodeValidation, "assimilation mutation parameters are invalid")
		}
		mutation := assimilationMutation(statement.Mutation)
		if err := validateSourceProvenance(mutation); err != nil {
			return protocolmsql.AssimilationPlan{}, err
		}
		if err := validateAssimilationMutationGuards(document.Statement, mutation); err != nil {
			return protocolmsql.AssimilationPlan{}, err
		}
	}
	plan, err := protocolmsql.SealAssimilationPlan(proposal)
	if err != nil {
		return protocolmsql.AssimilationPlan{}, executeError(result.CodeValidation, "assimilation proposal cannot be sealed")
	}
	return plan, nil
}

func validateAssimilationMutationGuards(statement ast.Statement, options MutationOptions) error {
	switch {
	case statement.Insert != nil:
		return validateMutationOptions(options, false)
	case statement.Update != nil, statement.Delete != nil, statement.Restore != nil:
		return validateMutationOptions(options, true)
	case statement.Relate != nil:
		return validateRelationshipMutationOptions(options, false)
	case statement.Unrelate != nil:
		return validateRelationshipMutationOptions(options, true)
	default:
		return executeError(result.CodeValidation, "assimilation mutation guard policy is undefined")
	}
}

func decodeAssimilationProposal(value any) (protocolmsql.AssimilationProposal, error) {
	var proposal protocolmsql.AssimilationProposal
	if typed, ok := value.(protocolmsql.AssimilationProposal); ok {
		proposal = typed
	} else if err := strictAssimilationValue(value, &proposal); err != nil {
		return protocolmsql.AssimilationProposal{}, err
	}
	if proposal.Validate() != nil {
		return protocolmsql.AssimilationProposal{}, executeError(result.CodeValidation, "assimilation proposal is invalid")
	}
	return proposal, nil
}

func decodeAssimilationPlan(value any) (protocolmsql.AssimilationPlan, error) {
	var plan protocolmsql.AssimilationPlan
	if typed, ok := value.(protocolmsql.AssimilationPlan); ok {
		plan = typed
	} else if err := strictAssimilationValue(value, &plan); err != nil {
		return protocolmsql.AssimilationPlan{}, err
	}
	if plan.Validate() != nil {
		return protocolmsql.AssimilationPlan{}, executeError(result.CodeValidation, "assimilation plan is invalid")
	}
	return plan, nil
}

func strictAssimilationValue(value any, target any) error {
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) == 0 || len(encoded) > protocolmsql.MaxAssimilationPlanBytes {
		return executeError(result.CodeValidation, "assimilation value is invalid or too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return executeError(result.CodeValidation, "assimilation value is invalid")
	}
	return nil
}

func assimilationDatabaseMatches(reference, statementReference string, database catalog.Database) bool {
	want := strings.ToLower(strings.TrimSpace(reference))
	for _, candidate := range append([]string{statementReference, database.ID, database.Name}, database.Aliases...) {
		if strings.ToLower(strings.TrimSpace(candidate)) == want {
			return true
		}
	}
	return false
}

func assimilationParameters(value protocolmsql.Parameters) Parameters {
	return Parameters{Named: cloneAssimilationMap(value.Named), Positional: append([]any(nil), value.Positional...)}
}

func assimilationMutation(value protocolmsql.MutationOptions) MutationOptions {
	updates := make([]row.RouteUpdate, len(value.RouteUpdates))
	for index, update := range value.RouteUpdates {
		updates[index] = row.RouteUpdate{
			RouteID: update.RouteID, ExpectedRevision: update.ExpectedRevision,
			Purpose: update.Purpose, Synopsis: cloneAssimilationString(update.Synopsis),
		}
	}
	return MutationOptions{
		ExpectedSchemaVersion: value.ExpectedSchemaVersion, ExpectedRevision: value.ExpectedRevision,
		SourceRevisions: cloneAssimilationMap(value.SourceRevisions), MaxAffectedRows: value.MaxAffectedRows,
		Actor: value.Actor, Source: value.Source, Reason: value.Reason,
		SourceKind: history.SourceKind(value.SourceKind), SourceReceiptID: value.SourceReceiptID,
		SourceLocator: value.SourceLocator, SourceContentHash: value.SourceContentHash,
		RouteLeafIDs:           append([]string(nil), value.RouteLeafIDs...),
		TargetRouteLeafIDs:     cloneAssimilationStrings(value.TargetRouteLeafIDs),
		RelationTargetOrdinals: cloneAssimilationMap(value.RelationTargetOrdinals), RouteUpdates: updates,
	}
}

func assimilationReceiptOutput(receipt protocolmsql.AssimilationReceipt, affected bool) Output {
	output := Output{
		Columns: []result.Column{
			{Name: "receipt_id", Type: "ID"}, {Name: "status", Type: "TEXT"},
			{Name: "plan_sha256", Type: "TEXT"}, {Name: "assimilation_receipt", Type: "JSON"},
		},
		Rows: []result.Row{{
			"receipt_id": receipt.ReceiptID, "status": receipt.Status,
			"plan_sha256": receipt.PlanSHA256, "assimilation_receipt": receipt,
		}},
	}
	if affected {
		output.AffectedRows = 1
	}
	return output
}

func cloneAssimilationMap[K comparable, V any](value map[K]V) map[K]V {
	if value == nil {
		return nil
	}
	cloned := make(map[K]V, len(value))
	for key, item := range value {
		cloned[key] = item
	}
	return cloned
}

func cloneAssimilationStrings(value [][]string) [][]string {
	if value == nil {
		return nil
	}
	cloned := make([][]string, len(value))
	for index := range value {
		cloned[index] = append([]string(nil), value[index]...)
	}
	return cloned
}

func cloneAssimilationString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
