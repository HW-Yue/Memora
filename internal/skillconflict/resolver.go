package skillconflict

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/msql/parser"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/skillwrite"
)

func Resolve(view View, resolution Resolution) (skillwrite.Plan, error) {
	validated, err := validateView(view)
	if err != nil {
		return skillwrite.Plan{}, err
	}
	if resolution.Version != ResolutionVersion || resolution.ConflictID != validated.ID {
		return skillwrite.Plan{}, conflictError(result.CodeValidation, "resolution version or conflict identity does not match")
	}
	if blank(resolution.Instruction) || blank(resolution.Actor) || blank(resolution.SourceEventID) {
		return skillwrite.Plan{}, conflictError(result.CodeValidation, "explicit user instruction and resolution provenance are required")
	}
	if resolution.SourceEventID == validated.Proposal.Source.SourceEventID {
		return skillwrite.Plan{}, conflictError(result.CodeValidation, "resolution requires a new source event")
	}
	viewAllowed, _ := validateAuthorization(validated.AuthorizedDatabases)
	resolutionAllowed, err := validateAuthorization(resolution.AuthorizedDatabases)
	if err != nil {
		return skillwrite.Plan{}, err
	}
	for database := range resolutionAllowed {
		if !viewAllowed[database] {
			return skillwrite.Plan{}, conflictError(result.CodePermissionDenied, "resolution expands database authorization to %q", database)
		}
	}
	plan := resolution.Plan
	if !strings.EqualFold(plan.Database, validated.Database) ||
		!strings.EqualFold(plan.Table, validated.Table) ||
		plan.Actor != resolution.Actor || plan.SourceEventID != resolution.SourceEventID ||
		plan.Reason != resolution.Instruction {
		return skillwrite.Plan{}, conflictError(result.CodeValidation, "Mutation Plan scope or provenance does not match the resolution")
	}
	for _, database := range plan.AuthorizedDatabases {
		if !resolutionAllowed[canonical(database)] {
			return skillwrite.Plan{}, conflictError(result.CodePermissionDenied, "Mutation Plan expands resolution authorization to %q", database)
		}
	}
	if err := plan.Validate(); err != nil {
		return skillwrite.Plan{}, conflictError(result.CodeValidation, "Mutation Plan is invalid: %v", err)
	}
	rows := make(map[string]RowSnapshot, len(validated.Existing))
	for _, snapshot := range validated.Existing {
		rows[snapshot.RowID] = snapshot
	}
	kept, found := rows[resolution.KeepRowID]
	if !found {
		return skillwrite.Plan{}, conflictError(result.CodeValidation, "kept Row %q was not shown in the conflict view", resolution.KeepRowID)
	}
	switch resolution.Action {
	case ActionRetain:
		if len(resolution.RemoveRowIDs) != 0 || plan.Decision != skillwrite.DecisionIgnore {
			return skillwrite.Plan{}, conflictError(result.CodeValidation, "RETAIN requires an IGNORE Plan and no removed Rows")
		}
	case ActionRewrite:
		if len(resolution.RemoveRowIDs) != 0 || plan.Decision != skillwrite.DecisionRevise || len(plan.Steps) != 1 ||
			!matchesStep(plan.Steps[0], "UPDATE", kept, validated.Database, validated.Table) {
			return skillwrite.Plan{}, conflictError(result.CodeValidation, "REWRITE must revise the shown Row at its displayed revision")
		}
	case ActionRemove:
		if plan.Decision != skillwrite.DecisionMerge {
			return skillwrite.Plan{}, conflictError(result.CodeValidation, "REMOVE requires a MERGE Plan")
		}
		if err := validateRemoval(
			plan, kept, resolution.RemoveRowIDs, rows, validated.Database, validated.Table,
		); err != nil {
			return skillwrite.Plan{}, err
		}
	default:
		return skillwrite.Plan{}, conflictError(result.CodeValidation, "unsupported conflict action %q", resolution.Action)
	}
	return plan, nil
}

func validateView(view View) (View, error) {
	if view.Version != ViewVersion {
		return View{}, conflictError(result.CodeValidation, "conflict view version = %q, want %q", view.Version, ViewVersion)
	}
	rebuilt, err := Build(Request{
		ID: view.ID, Database: view.Database, Table: view.Table,
		AuthorizedDatabases: view.AuthorizedDatabases,
		Proposal:            view.Proposal, Existing: view.Existing,
	})
	if err != nil {
		return View{}, err
	}
	encodedView, viewErr := json.Marshal(view)
	encodedRebuilt, rebuiltErr := json.Marshal(rebuilt)
	if viewErr != nil || rebuiltErr != nil || !bytes.Equal(encodedView, encodedRebuilt) {
		return View{}, conflictError(result.CodeValidation, "conflict view was modified after construction")
	}
	return rebuilt, nil
}

func validateRemoval(
	plan skillwrite.Plan,
	kept RowSnapshot,
	removeRowIDs []string,
	rows map[string]RowSnapshot,
	database string,
	table string,
) error {
	if len(removeRowIDs) == 0 || len(plan.Steps) != len(removeRowIDs)+1 {
		return conflictError(result.CodeValidation, "REMOVE requires one survivor and one or more removed Rows")
	}
	removed := make(map[string]RowSnapshot, len(removeRowIDs))
	for _, rowID := range removeRowIDs {
		if rowID == kept.RowID || removed[rowID].RowID != "" {
			return conflictError(result.CodeValidation, "removed Rows must be unique and cannot include the survivor")
		}
		snapshot, found := rows[rowID]
		if !found {
			return conflictError(result.CodeValidation, "removed Row %q was not shown in the conflict view", rowID)
		}
		removed[rowID] = snapshot
	}
	updated := false
	deleted := map[string]bool{}
	for _, step := range plan.Steps {
		switch strings.ToUpper(strings.TrimSpace(step.Kind)) {
		case "UPDATE":
			if updated || !matchesStep(step, "UPDATE", kept, database, table) {
				return conflictError(result.CodeValidation, "REMOVE must update only the displayed survivor revision")
			}
			updated = true
		case "DELETE":
			snapshot, found := removed[step.Target]
			if !found || deleted[step.Target] || !matchesStep(step, "DELETE", snapshot, database, table) {
				return conflictError(result.CodeValidation, "REMOVE deletes a stale, duplicate, or unshown Row")
			}
			deleted[step.Target] = true
		default:
			return conflictError(result.CodeValidation, "REMOVE contains an unsupported mutation step")
		}
	}
	if !updated || len(deleted) != len(removed) {
		return conflictError(result.CodeValidation, "REMOVE Plan does not match the user-selected Rows")
	}
	return nil
}

func matchesStep(
	step skillwrite.Step,
	kind string,
	snapshot RowSnapshot,
	database string,
	table string,
) bool {
	target, found := mutationRowTarget(step, database, table)
	return strings.EqualFold(strings.TrimSpace(step.Kind), kind) &&
		step.Target == snapshot.RowID &&
		step.Input.Mutation.ExpectedRevision == snapshot.Revision &&
		found && target == snapshot.RowID
}

func mutationRowTarget(step skillwrite.Step, database, table string) (string, bool) {
	batch, err := parser.ParseBatch(step.MSQL)
	if err != nil || len(batch.Statements) != 1 {
		return "", false
	}
	statement := batch.Statements[0]
	var where *ast.Expression
	switch {
	case statement.Update != nil:
		if !matchesTable(statement.Update.Table, database, table) {
			return "", false
		}
		where = statement.Update.Where
	case statement.Delete != nil:
		if !matchesTable(statement.Delete.Table, database, table) {
			return "", false
		}
		where = statement.Delete.Where
	default:
		return "", false
	}
	return exactRowTarget(statement, where, step.Input.Parameters)
}

func matchesTable(name ast.Name, database, table string) bool {
	return len(name.Parts) == 2 &&
		strings.EqualFold(name.Parts[0].Value, database) &&
		strings.EqualFold(name.Parts[1].Value, table)
}

func exactRowTarget(
	statement ast.Statement,
	expression *ast.Expression,
	parameters executor.Parameters,
) (string, bool) {
	if expression == nil {
		return "", false
	}
	if expression.Operator == "AND" {
		left, leftFound := exactRowTarget(statement, expression.Left, parameters)
		right, rightFound := exactRowTarget(statement, expression.Right, parameters)
		if leftFound && rightFound && left != right {
			return "", false
		}
		if leftFound {
			return left, true
		}
		return right, rightFound
	}
	if expression.Operator != "=" {
		return "", false
	}
	for _, pair := range [][2]*ast.Expression{
		{expression.Left, expression.Right},
		{expression.Right, expression.Left},
	} {
		if !isRowID(pair[0]) {
			continue
		}
		if value, ok := stringValue(statement, pair[1], parameters); ok && value != "" {
			return value, true
		}
	}
	return "", false
}

func isRowID(expression *ast.Expression) bool {
	return expression != nil && expression.Kind == "identifier" && expression.Name != nil &&
		len(expression.Name.Parts) == 1 && strings.EqualFold(expression.Name.Parts[0].Value, "row_id")
}

func stringValue(
	statement ast.Statement,
	expression *ast.Expression,
	parameters executor.Parameters,
) (string, bool) {
	if expression == nil {
		return "", false
	}
	if expression.Literal != nil && expression.Literal.Kind == "string" {
		return expression.Literal.Value, true
	}
	if expression.Parameter == nil {
		return "", false
	}
	parameter := expression.Parameter
	var value any
	var found bool
	switch parameter.Style {
	case "named":
		value, found = parameters.Named[parameter.Name]
	case "positional":
		position := 0
		for _, occurrence := range (ast.Document{Statement: statement}).Parameters() {
			if occurrence.Style != "positional" {
				continue
			}
			if occurrence.Ordinal == parameter.Ordinal {
				if position < len(parameters.Positional) {
					value, found = parameters.Positional[position], true
				}
				break
			}
			position++
		}
	}
	stringValue, ok := value.(string)
	return stringValue, found && ok
}
