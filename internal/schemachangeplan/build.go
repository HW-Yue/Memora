package schemachangeplan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/logical"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
)

const (
	maximumChanges     = 16
	maximumColumns     = 256
	maximumRows        = 1000
	maximumBlockers    = 32
	maximumActorBytes  = 256
	maximumSourceBytes = 256
	maximumReasonBytes = 4 << 10
)

func Build(
	ctx context.Context,
	source RowSource,
	database catalog.Database,
	table catalog.Table,
	proposal Proposal,
) (Plan, error) {
	proposal = normalizeProposal(proposal)
	if err := validateIdentity(database, table, proposal); err != nil {
		return Plan{}, err
	}
	scope := Scope{DatabaseID: database.ID, Database: database.Name, TableID: table.ID, Table: table.Name}
	guards := make([]ColumnShape, 0, len(table.Columns))
	current := make(map[string]ColumnShape, len(table.Columns))
	for _, column := range table.Columns {
		shape, err := existingShape(column)
		if err != nil || current[column.ID].ColumnID != "" {
			return Plan{}, planError(result.CodeInternal, "current Column schema is invalid or duplicated")
		}
		current[column.ID] = shape
		guards = append(guards, shape)
	}
	sort.Slice(guards, func(i, j int) bool { return guards[i].ColumnID < guards[j].ColumnID })
	actions, final, scan, err := buildActions(table, proposal, current)
	if err != nil {
		return Plan{}, err
	}
	if err := validateFinalSchema(final); err != nil {
		return Plan{}, err
	}
	plan := Plan{
		Version: PlanVersion, ProposalID: proposal.ID, Status: StatusReviewRequired, Scope: scope,
		Actor: proposal.Actor, SourceEventID: proposal.SourceEventID, Reason: proposal.Reason,
		DatabaseRevision: database.SchemaVersion, TableRevision: table.SchemaVersion,
		ColumnGuards: guards, RowGuards: []RowGuard{}, Actions: actions, Blockers: []Blocker{},
		Impact: Impact{AffectedColumns: len(actions), Reversible: true},
	}
	plan.BaseSchemaHash, err = digest(struct {
		Scope            Scope         `json:"scope"`
		DatabaseRevision uint64        `json:"database_revision"`
		TableRevision    uint64        `json:"table_revision"`
		Columns          []ColumnShape `json:"columns"`
	}{scope, database.SchemaVersion, table.SchemaVersion, guards})
	if err != nil {
		return Plan{}, planError(result.CodeInternal, "Schema snapshot could not be hashed")
	}
	if scan {
		if source == nil {
			return Plan{}, planError(result.CodeInternal, "Row compatibility source is unavailable")
		}
		rows, more, err := source.ListPage(ctx, database.Name, table.Name, maximumRows)
		if err != nil {
			return Plan{}, sourceError("scan live Rows", err)
		}
		if more || len(rows) > maximumRows {
			return Plan{}, planError(result.CodeConstraint, "Row compatibility scan is truncated at %d", maximumRows)
		}
		if err := inspectRows(&plan, rows); err != nil {
			return Plan{}, err
		}
		plan.RowSnapshotHash, err = digest(plan.RowGuards)
		if err != nil {
			return Plan{}, planError(result.CodeInternal, "Row snapshot could not be hashed")
		}
	}
	for _, action := range actions {
		if action.Action == ActionDrop {
			plan.Impact.Destructive, plan.Impact.Reversible = true, false
		}
	}
	if plan.Impact.IncompatibleRows > 0 {
		plan.Status = StatusBlocked
	}
	seed := plan
	seed.PlanID, seed.Hash = "", ""
	identity, err := digest(seed)
	if err != nil {
		return Plan{}, planError(result.CodeInternal, "plan identity could not be hashed")
	}
	plan.PlanID = "schema_plan_" + strings.TrimPrefix(identity, "sha256:")[:24]
	hashed := plan
	hashed.Hash = ""
	plan.Hash, err = digest(hashed)
	if err != nil {
		return Plan{}, planError(result.CodeInternal, "plan could not be hashed")
	}
	return plan, nil
}

func validateIdentity(database catalog.Database, table catalog.Table, proposal Proposal) error {
	if proposal.Version != ProposalVersion || blank(proposal.ID) ||
		!validMetadataText(proposal.Actor, maximumActorBytes) ||
		!validMetadataText(proposal.SourceEventID, maximumSourceBytes) ||
		!validMetadataText(proposal.Reason, maximumReasonBytes) || proposal.ExpectedTableRevision == 0 {
		return planError(result.CodeValidation, "proposal identity, provenance, and expected Table revision are required")
	}
	if database.ID == "" || table.ID == "" || table.DatabaseID != database.ID ||
		database.SchemaVersion == 0 || table.SchemaVersion == 0 {
		return planError(result.CodeInternal, "bound Database or Table is invalid")
	}
	if table.SchemaVersion != proposal.ExpectedTableRevision {
		return planError(result.CodeRevisionConflict, "Table revision conflicts with proposal")
	}
	matchedTable := false
	for _, candidate := range database.Tables {
		if candidate.ID == table.ID {
			matchedTable = candidate.SchemaVersion == table.SchemaVersion
		}
	}
	if !matchedTable {
		return planError(result.CodeRevisionConflict, "Database and Table snapshots are inconsistent")
	}
	if len(table.Columns) == 0 || len(table.Columns) > maximumColumns {
		return planError(result.CodeConstraint, "Table Column snapshot must contain between 1 and %d Columns", maximumColumns)
	}
	if len(proposal.Changes) == 0 || len(proposal.Changes) > maximumChanges {
		return planError(result.CodeConstraint, "proposal must contain between 1 and %d changes", maximumChanges)
	}
	return nil
}

func normalizeProposal(value Proposal) Proposal {
	value.ID, value.Actor = strings.TrimSpace(value.ID), strings.TrimSpace(value.Actor)
	value.SourceEventID, value.Reason = strings.TrimSpace(value.SourceEventID), strings.TrimSpace(value.Reason)
	value.Changes = append([]ChangeProposal(nil), value.Changes...)
	for index := range value.Changes {
		change := &value.Changes[index]
		change.ID, change.ColumnID, change.NewName = strings.TrimSpace(change.ID), strings.TrimSpace(change.ColumnID), strings.TrimSpace(change.NewName)
		if change.Definition != nil {
			definition := *change.Definition
			definition.Name, definition.Type = strings.TrimSpace(definition.Name), strings.TrimSpace(definition.Type)
			definition.Purpose, definition.SemanticRole = strings.TrimSpace(definition.Purpose), strings.ToLower(strings.TrimSpace(definition.SemanticRole))
			change.Definition = &definition
		}
	}
	sort.Slice(value.Changes, func(i, j int) bool { return value.Changes[i].ID < value.Changes[j].ID })
	return value
}

func buildActions(
	table catalog.Table,
	proposal Proposal,
	current map[string]ColumnShape,
) ([]PlannedAction, map[string]ColumnShape, bool, error) {
	actions := make([]PlannedAction, 0, len(proposal.Changes))
	final := make(map[string]ColumnShape, len(current)+len(proposal.Changes))
	for id, shape := range current {
		final[id] = shape
	}
	seenChanges, seenColumns, scan := map[string]bool{}, map[string]bool{}, false
	for _, change := range proposal.Changes {
		if blank(change.ID) || seenChanges[change.ID] {
			return nil, nil, false, planError(result.CodeValidation, "change IDs must be non-empty and unique")
		}
		seenChanges[change.ID] = true
		switch change.Action {
		case ActionAdd:
			if change.ColumnID != "" || change.ExpectedRevision != 0 || change.NewName != "" || change.Definition == nil {
				return nil, nil, false, planError(result.CodeValidation, "ADD_COLUMN %q shape is invalid", change.ID)
			}
			after, err := newShape(table.ID, proposal.ID, change.ID, *change.Definition)
			if err != nil {
				return nil, nil, false, err
			}
			if final[after.ColumnID].ColumnID != "" {
				return nil, nil, false, planError(result.CodeAlreadyExists, "derived Column ID already exists")
			}
			final[after.ColumnID] = after
			actions = append(actions, PlannedAction{ChangeID: change.ID, Action: change.Action, After: cloneShape(after)})
			scan = scan || !after.Nullable
		case ActionRename, ActionAlter, ActionDrop:
			before, ok := current[change.ColumnID]
			if !ok || seenColumns[change.ColumnID] || change.ExpectedRevision == 0 || before.Revision != change.ExpectedRevision {
				return nil, nil, false, planError(result.CodeRevisionConflict, "Column %q is missing, duplicated, or stale", change.ColumnID)
			}
			seenColumns[change.ColumnID] = true
			action := PlannedAction{ChangeID: change.ID, Action: change.Action, Before: cloneShape(before)}
			switch change.Action {
			case ActionRename:
				if blank(change.NewName) || change.Definition != nil {
					return nil, nil, false, planError(result.CodeValidation, "RENAME_COLUMN %q shape is invalid", change.ID)
				}
				after := before
				after.Aliases = uniqueSorted(append(after.Aliases, before.Name))
				after.Name, after.Revision = change.NewName, before.Revision+1
				if !validColumnName(after.Name) {
					return nil, nil, false, planError(result.CodeValidation, "Column name is invalid")
				}
				final[after.ColumnID], action.After = after, cloneShape(after)
			case ActionAlter:
				if change.Definition == nil || change.NewName != "" || change.Definition.Name != before.Name {
					return nil, nil, false, planError(result.CodeValidation, "ALTER_COLUMN %q must preserve the current name", change.ID)
				}
				after, err := definitionShape(before.ColumnID, before.Aliases, before.Revision+1, *change.Definition)
				if err != nil {
					return nil, nil, false, err
				}
				final[after.ColumnID], action.After = after, cloneShape(after)
				scan = scan || constraintChanged(before, after)
			case ActionDrop:
				if change.Definition != nil || change.NewName != "" {
					return nil, nil, false, planError(result.CodeValidation, "DROP_COLUMN %q shape is invalid", change.ID)
				}
				delete(final, before.ColumnID)
				scan = true
			}
			actions = append(actions, action)
		default:
			return nil, nil, false, planError(result.CodeValidation, "change action %q is not supported", change.Action)
		}
	}
	return actions, final, scan, nil
}

func validateFinalSchema(columns map[string]ColumnShape) error {
	if len(columns) == 0 {
		return planError(result.CodeConstraint, "Table must retain at least one Column")
	}
	surfaces, roles := map[string]string{}, map[string]string{}
	ids := make([]string, 0, len(columns))
	for id := range columns {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		column := columns[id]
		for _, surface := range append([]string{column.Name}, column.Aliases...) {
			key := strings.ToLower(strings.TrimSpace(surface))
			if previous := surfaces[key]; key == "" || (previous != "" && previous != id) {
				return planError(result.CodeConstraint, "Column names or aliases conflict")
			}
			surfaces[key] = id
		}
		if column.SemanticRole == "title" || column.SemanticRole == "summary" {
			if roles[column.SemanticRole] != "" {
				return planError(result.CodeConstraint, "Column semantic role %q is duplicated", column.SemanticRole)
			}
			roles[column.SemanticRole] = id
		}
	}
	return nil
}

func inspectRows(plan *Plan, values []row.Row) error {
	sort.Slice(values, func(i, j int) bool { return values[i].ID < values[j].ID })
	seen, incompatible := map[string]bool{}, map[string]bool{}
	for _, value := range values {
		if value.ID == "" || value.Revision == 0 || seen[value.ID] {
			return planError(result.CodeInternal, "Row compatibility snapshot is invalid or duplicated")
		}
		seen[value.ID] = true
		plan.RowGuards = append(plan.RowGuards, RowGuard{RowID: value.ID, Revision: value.Revision})
		for _, action := range plan.Actions {
			switch action.Action {
			case ActionAdd:
				if action.After != nil && !action.After.Nullable {
					incompatible[value.ID] = true
					plan.Impact.RequiresRowRewrite = true
					appendBlocker(plan, action.ChangeID, value.ID, "ADD_NOT_NULL_REQUIRES_VALUE", "existing Row has no value for a new NOT NULL Column")
				}
			case ActionAlter:
				if action.Before == nil || action.After == nil || !constraintChanged(*action.Before, *action.After) {
					continue
				}
				definition := logical.Definition{Kind: logical.Kind(action.After.Type), MaxCharacters: action.After.MaxCharacters}
				existing := rowValue(value, *action.Before)
				if _, err := logical.Validate(logical.Constraint{Name: action.After.Name, Definition: definition, Nullable: action.After.Nullable}, existing); err != nil {
					incompatible[value.ID] = true
					plan.Impact.RequiresRowRewrite = true
					appendBlocker(plan, action.ChangeID, value.ID, "ROW_VALUE_INCOMPATIBLE", "existing Row value does not satisfy the proposed constraint")
				}
			case ActionDrop:
				if action.Before != nil {
					if existing, present := rowValuePresent(value, *action.Before); present && existing != nil {
						plan.Impact.PopulatedDroppedValues++
					}
				}
			}
		}
	}
	plan.Impact.RowsScanned, plan.Impact.IncompatibleRows = len(values), len(incompatible)
	return nil
}

func rowValue(value row.Row, column ColumnShape) any {
	existing, _ := rowValuePresent(value, column)
	return existing
}

func rowValuePresent(value row.Row, column ColumnShape) (any, bool) {
	if existing, present := value.Values[column.ColumnID]; present {
		return existing, true
	}
	existing, present := value.Values[column.Name]
	return existing, present
}

func appendBlocker(plan *Plan, changeID, rowID, code, message string) {
	if len(plan.Blockers) < maximumBlockers {
		plan.Blockers = append(plan.Blockers, Blocker{Code: code, ChangeID: changeID, RowID: rowID, Message: message})
	}
}

func existingShape(column catalog.Column) (ColumnShape, error) {
	declaration := string(column.Type)
	if logical.Kind(column.Type) == logical.KindText && column.MaxCharacters > 0 {
		declaration = fmt.Sprintf("TEXT(%d)", column.MaxCharacters)
	}
	return definitionShape(column.ID, column.Aliases, column.SchemaVersion, catalog.ColumnDefinition{
		Name: column.Name, Type: declaration, Nullable: column.Nullable,
		Purpose: column.Purpose, SemanticRole: column.SemanticRole,
	})
}

func newShape(tableID, proposalID, changeID string, definition catalog.ColumnDefinition) (ColumnShape, error) {
	digest := sha256.Sum256([]byte(tableID + "\x00" + proposalID + "\x00" + changeID))
	return definitionShape("col_"+hex.EncodeToString(digest[:16]), []string{}, 1, definition)
}

func definitionShape(id string, aliases []string, revision uint64, definition catalog.ColumnDefinition) (ColumnShape, error) {
	if id == "" || revision == 0 || !validColumnName(definition.Name) || blank(definition.Purpose) || !validRole(definition.SemanticRole) {
		return ColumnShape{}, planError(result.CodeValidation, "Column definition is invalid")
	}
	parsed, err := logical.ParseDeclaration(definition.Type)
	if err != nil {
		return ColumnShape{}, planError(result.CodeValidation, "Column type declaration is invalid")
	}
	return ColumnShape{ColumnID: id, Name: definition.Name, Aliases: uniqueSorted(aliases),
		Type: string(parsed.Kind), MaxCharacters: parsed.MaxCharacters, Nullable: definition.Nullable,
		Purpose: definition.Purpose, SemanticRole: strings.ToLower(strings.TrimSpace(definition.SemanticRole)), Revision: revision}, nil
}

func constraintChanged(left, right ColumnShape) bool {
	return left.Type != right.Type || left.MaxCharacters != right.MaxCharacters || left.Nullable != right.Nullable
}

func validColumnName(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || !utf8.ValidString(value) || utf8.RuneCountInString(value) > 128 {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	switch strings.ToLower(value) {
	case "row_id", "database_id", "table_id", "schema_version", "revision", "commit_sequence", "row_state", "created_at", "updated_at":
		return false
	}
	return true
}

func validRole(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "title", "summary", "identity", "status":
		return true
	default:
		return false
	}
}

func cloneShape(value ColumnShape) *ColumnShape {
	value.Aliases = append([]string{}, value.Aliases...)
	return &value
}

func uniqueSorted(values []string) []string {
	result, seen := make([]string, 0, len(values)), map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func digest(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(hash[:]), nil
}

func sourceError(action string, err error) error {
	if stable, ok := err.(interface{ StableCode() string }); ok {
		return &Error{Code: result.Code(stable.StableCode()), Message: fmt.Sprintf("%s: %v", action, err)}
	}
	return planError(result.CodeInternal, "%s: %v", action, err)
}

func planError(code result.Code, format string, arguments ...any) error {
	return &Error{Code: code, Message: fmt.Sprintf(format, arguments...)}
}

func blank(value string) bool { return strings.TrimSpace(value) == "" }

func validMetadataText(value string, maximumBytes int) bool {
	return !blank(value) && utf8.ValidString(value) && len(value) <= maximumBytes
}
