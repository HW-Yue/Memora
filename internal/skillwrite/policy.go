package skillwrite

import (
	"fmt"
	"strings"

	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/msql/parser"
	"github.com/HW-Yue/Memora/internal/result"
)

func (plan Plan) Validate() error {
	if plan.Version != PlanVersion {
		return mutationError(result.CodeValidation, "plan version = %q, want %q", plan.Version, PlanVersion)
	}
	if blank(plan.ID) || blank(plan.Database) || blank(plan.Table) ||
		blank(plan.Actor) || blank(plan.SourceEventID) || blank(plan.Reason) {
		return mutationError(result.CodeValidation, "plan identity, scope, and provenance are required")
	}
	allowed := make(map[string]bool, len(plan.AuthorizedDatabases))
	for _, database := range plan.AuthorizedDatabases {
		allowed[canonical(database)] = true
	}
	if !allowed[canonical(plan.Database)] {
		return mutationError(result.CodePermissionDenied, "database %q is outside the authorized scope", plan.Database)
	}
	if len(plan.Preflight) == 0 {
		return mutationError(result.CodeValidation, "plan requires at least one preflight query")
	}
	for index, check := range plan.Preflight {
		if err := validateCheck(check); err != nil {
			return mutationError(result.CodeValidation, "preflight %d: %v", index, err)
		}
	}
	if plan.Decision == DecisionIgnore {
		if len(plan.Steps) != 0 {
			return mutationError(result.CodeValidation, "IGNORE cannot contain mutation steps")
		}
		return nil
	}
	if len(plan.Steps) == 0 || len(plan.Steps) > 8 {
		return mutationError(result.CodeValidation, "mutation plan must contain 1 to 8 steps")
	}
	if len(plan.Verify) == 0 {
		return mutationError(result.CodeValidation, "mutation plan requires a verification query")
	}
	for index, check := range plan.Verify {
		if err := validateCheck(check); err != nil {
			return mutationError(result.CodeValidation, "verification %d: %v", index, err)
		}
	}
	kinds := make(map[string]int)
	seenSteps := make(map[string]bool)
	for index, step := range plan.Steps {
		if seenSteps[step.ID] || blank(step.ID) || blank(step.Target) {
			return mutationError(result.CodeValidation, "mutation step %d has an empty or duplicate identity", index)
		}
		seenSteps[step.ID] = true
		kind, databases, err := validateStep(step, plan)
		if err != nil {
			return mutationError(result.CodeValidation, "mutation step %d: %v", index, err)
		}
		for _, database := range databases {
			if !allowed[canonical(database)] {
				return mutationError(result.CodePermissionDenied, "step %q targets unauthorized database %q", step.ID, database)
			}
		}
		kinds[kind]++
	}
	if err := validateDecisionShape(plan.Decision, kinds, len(plan.Steps)); err != nil {
		return err
	}
	return nil
}

func validateCheck(check Check) error {
	if blank(check.ID) || blank(check.MSQL) {
		return fmt.Errorf("check identity and MSQL are required")
	}
	if check.ExpectRows == nil && len(check.RowContains) == 0 {
		return fmt.Errorf("check requires an explicit Row expectation")
	}
	batch, err := parser.ParseBatch(check.MSQL)
	if err != nil || len(batch.Statements) != 1 {
		return fmt.Errorf("check must contain one valid MSQL statement")
	}
	switch batch.Statements[0].Kind {
	case "SHOW", "DESCRIBE", "SELECT", "MATCH", "OPEN_ROUTE":
		return nil
	default:
		return fmt.Errorf("check contains mutation %q", batch.Statements[0].Kind)
	}
}

func validateStep(step Step, plan Plan) (string, []string, error) {
	batch, err := parser.ParseBatch(step.MSQL)
	if err != nil || len(batch.Statements) != 1 {
		return "", nil, fmt.Errorf("step must contain one valid MSQL statement")
	}
	statement := batch.Statements[0]
	kind := strings.ToUpper(strings.TrimSpace(step.Kind))
	if statement.Kind != kind {
		return "", nil, fmt.Errorf("declared kind %q does not match MSQL kind %q", kind, statement.Kind)
	}
	if kind != "INSERT" && kind != "UPDATE" && kind != "DELETE" && kind != "RELATE" &&
		kind != "SPLIT" && kind != "MERGE" {
		return "", nil, fmt.Errorf("unsupported mutation kind %q", kind)
	}
	options := step.Input.Mutation
	if options.Actor != plan.Actor || options.Source != plan.SourceEventID || options.Reason != plan.Reason {
		return "", nil, fmt.Errorf("mutation guard or provenance does not match the plan")
	}
	if kind != "SPLIT" && kind != "MERGE" && options.MaxAffectedRows != 1 {
		return "", nil, fmt.Errorf("mutation guard or provenance does not match the plan")
	}
	switch kind {
	case "INSERT", "UPDATE":
		if options.ExpectedSchemaVersion == 0 {
			return "", nil, fmt.Errorf("row mutation requires expected schema version")
		}
		if kind == "UPDATE" && options.ExpectedRevision == 0 {
			return "", nil, fmt.Errorf("UPDATE requires expected revision")
		}
		if options.RouteLeafIDs == nil {
			return "", nil, fmt.Errorf("row mutation requires a complete Route snapshot")
		}
		if err := validateSnapshot(options.RouteLeafIDs, 32, "Route memberships"); err != nil {
			return "", nil, err
		}
	case "DELETE":
		if options.ExpectedSchemaVersion == 0 || options.ExpectedRevision == 0 {
			return "", nil, fmt.Errorf("DELETE requires expected schema and Row revisions")
		}
	case "SPLIT", "MERGE":
		if options.ExpectedSchemaVersion == 0 || options.MaxAffectedRows < 3 {
			return "", nil, fmt.Errorf("reshape requires schema guard and a bounded source/target budget")
		}
		if kind == "SPLIT" && options.ExpectedRevision == 0 {
			return "", nil, fmt.Errorf("SPLIT requires expected source revision")
		}
		if kind == "MERGE" && len(options.SourceRevisions) < 2 {
			return "", nil, fmt.Errorf("MERGE requires source revisions")
		}
		if len(options.TargetRouteLeafIDs) == 0 {
			return "", nil, fmt.Errorf("reshape requires complete target Route snapshots")
		}
		for index, snapshot := range options.TargetRouteLeafIDs {
			if err := validateSnapshot(snapshot, 32, fmt.Sprintf("target %d Route memberships", index+1)); err != nil {
				return "", nil, err
			}
		}
	}
	databases := statementDatabases(statement)
	if len(databases) == 0 {
		return "", nil, fmt.Errorf("mutation tables must use database-qualified names")
	}
	return kind, databases, nil
}

func validateSnapshot(values []string, maximum int, label string) error {
	if len(values) > maximum {
		return fmt.Errorf("%s exceed the %d item Policy limit", label, maximum)
	}
	seen := map[string]bool{}
	for _, value := range values {
		normalized := canonical(value)
		if normalized == "" || seen[normalized] {
			return fmt.Errorf("%s must be non-empty and deduplicated", label)
		}
		seen[normalized] = true
	}
	return nil
}

func validateDecisionShape(decision Decision, kinds map[string]int, steps int) error {
	valid := false
	switch decision {
	case DecisionInsert:
		valid = steps == 1 && kinds["INSERT"] == 1
	case DecisionRevise, DecisionMove:
		valid = steps == 1 && kinds["UPDATE"] == 1
	case DecisionMerge:
		valid = (steps == 1 && kinds["MERGE"] == 1) ||
			(steps >= 2 && kinds["UPDATE"] == 1 && kinds["DELETE"] == steps-1)
	case DecisionSplit:
		valid = (steps == 1 && kinds["SPLIT"] == 1) ||
			(steps >= 2 && kinds["UPDATE"] == 1 && kinds["INSERT"] == steps-1)
	case DecisionRelate:
		valid = steps == 1 && kinds["RELATE"] == 1
	}
	if !valid {
		return mutationError(result.CodeValidation, "decision %q does not match its mutation step shape", decision)
	}
	return nil
}

func statementDatabases(statement ast.Statement) []string {
	var names []ast.Name
	switch {
	case statement.Insert != nil:
		names = append(names, statement.Insert.Table)
	case statement.Update != nil:
		names = append(names, statement.Update.Table)
	case statement.Delete != nil:
		names = append(names, statement.Delete.Table)
	case statement.Relate != nil:
		names = append(names, statement.Relate.SourceTable, statement.Relate.TargetTable)
	case statement.Reshape != nil:
		names = append(names, statement.Reshape.Table)
	}
	values := make([]string, 0, len(names))
	for _, name := range names {
		if len(name.Parts) == 2 {
			values = append(values, name.Parts[0].Value)
		}
	}
	return values
}

func blank(value string) bool       { return strings.TrimSpace(value) == "" }
func canonical(value string) string { return strings.ToLower(strings.TrimSpace(value)) }
