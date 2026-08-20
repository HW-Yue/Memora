package schemachangeplan

import (
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/result"
)

// Validate proves that a plan still has the canonical identity and guarded,
// bounded action vocabulary emitted by Build. Only review_required plans are
// executable; blocked plans remain useful diagnostics but fail this boundary.
func Validate(plan Plan) error {
	if plan.Version != PlanVersion || plan.Status != StatusReviewRequired || blank(plan.PlanID) ||
		blank(plan.ProposalID) || !validMetadataText(plan.Actor, maximumActorBytes) ||
		!validMetadataText(plan.SourceEventID, maximumSourceBytes) ||
		!validMetadataText(plan.Reason, maximumReasonBytes) ||
		blank(plan.Scope.DatabaseID) || blank(plan.Scope.TableID) || plan.DatabaseRevision == 0 ||
		plan.TableRevision == 0 || blank(plan.BaseSchemaHash) || blank(plan.Hash) {
		return planError(result.CodeValidation, "plan identity, scope, provenance, revisions, and executable status are required")
	}
	if len(plan.ColumnGuards) == 0 || len(plan.ColumnGuards) > maximumColumns ||
		len(plan.Actions) == 0 || len(plan.Actions) > maximumChanges || len(plan.RowGuards) > maximumRows {
		return planError(result.CodeConstraint, "plan exceeds the local execution budget")
	}
	if !strictlySortedColumns(plan.ColumnGuards) || !strictlySortedRows(plan.RowGuards) ||
		!strictlySortedActions(plan.Actions) {
		return planError(result.CodeValidation, "plan collections are not canonical")
	}
	guards := make(map[string]ColumnShape, len(plan.ColumnGuards))
	final := make(map[string]ColumnShape, len(plan.ColumnGuards)+len(plan.Actions))
	for _, guard := range plan.ColumnGuards {
		if err := validateShape(guard); err != nil {
			return err
		}
		guards[guard.ColumnID], final[guard.ColumnID] = guard, guard
	}
	seenColumns := map[string]bool{}
	requiresRows, destructive, droppedColumns := false, false, 0
	for _, action := range plan.Actions {
		if blank(action.ChangeID) {
			return planError(result.CodeValidation, "planned action change ID is required")
		}
		switch action.Action {
		case ActionAdd:
			if action.Before != nil || action.After == nil || action.After.Revision != 1 ||
				guards[action.After.ColumnID].ColumnID != "" || final[action.After.ColumnID].ColumnID != "" {
				return planError(result.CodeValidation, "ADD_COLUMN action is invalid")
			}
			if err := validateShape(*action.After); err != nil {
				return err
			}
			final[action.After.ColumnID] = *cloneShape(*action.After)
			requiresRows = requiresRows || !action.After.Nullable
		case ActionRename, ActionAlter, ActionDrop:
			if action.Before == nil || seenColumns[action.Before.ColumnID] ||
				!reflect.DeepEqual(guards[action.Before.ColumnID], *action.Before) {
				return planError(result.CodeValidation, "existing Column action is invalid or unguarded")
			}
			seenColumns[action.Before.ColumnID] = true
			switch action.Action {
			case ActionRename:
				if action.After == nil {
					return planError(result.CodeValidation, "RENAME_COLUMN target is required")
				}
				expected := *action.Before
				expected.Name = action.After.Name
				expected.Aliases = uniqueSorted(append(expected.Aliases, action.Before.Name))
				expected.Revision++
				if !reflect.DeepEqual(expected, *action.After) || validateShape(*action.After) != nil {
					return planError(result.CodeValidation, "RENAME_COLUMN target is invalid")
				}
				final[action.Before.ColumnID] = *cloneShape(*action.After)
			case ActionAlter:
				if action.After == nil || action.After.ColumnID != action.Before.ColumnID ||
					action.After.Name != action.Before.Name || action.After.Revision != action.Before.Revision+1 ||
					!reflect.DeepEqual(action.After.Aliases, action.Before.Aliases) || validateShape(*action.After) != nil {
					return planError(result.CodeValidation, "ALTER_COLUMN target is invalid")
				}
				final[action.Before.ColumnID] = *cloneShape(*action.After)
				requiresRows = requiresRows || constraintChanged(*action.Before, *action.After)
			case ActionDrop:
				if action.After != nil {
					return planError(result.CodeValidation, "DROP_COLUMN cannot have a target")
				}
				delete(final, action.Before.ColumnID)
				requiresRows, destructive, droppedColumns = true, true, droppedColumns+1
			}
		default:
			return planError(result.CodeValidation, "planned action is unsupported")
		}
	}
	if err := validateFinalSchema(final); err != nil {
		return err
	}
	if plan.Impact.AffectedColumns != len(plan.Actions) || plan.Impact.IncompatibleRows != 0 ||
		plan.Impact.RequiresRowRewrite || len(plan.Blockers) != 0 ||
		// Destructive and Reversible are no longer opposites. DROP_COLUMN
		// archives the Column instead of removing it, so it hides the Column
		// from every read surface (destructive) while staying exactly
		// undoable (reversible). Every action a plan can carry is now
		// reversible, so the flag must be set.
		plan.Impact.Destructive != destructive || !plan.Impact.Reversible ||
		plan.Impact.PopulatedDroppedValues < 0 ||
		plan.Impact.PopulatedDroppedValues > plan.Impact.RowsScanned*droppedColumns {
		return planError(result.CodeValidation, "plan impact does not match executable actions")
	}
	if requiresRows {
		if plan.Impact.RowsScanned != len(plan.RowGuards) || blank(plan.RowSnapshotHash) {
			return planError(result.CodeValidation, "Row snapshot coverage is incomplete")
		}
		rowHash, err := digest(plan.RowGuards)
		if err != nil || rowHash != plan.RowSnapshotHash {
			return planError(result.CodeValidation, "Row snapshot hash is invalid")
		}
	} else if len(plan.RowGuards) != 0 || plan.Impact.RowsScanned != 0 || plan.RowSnapshotHash != "" ||
		plan.Impact.PopulatedDroppedValues != 0 {
		return planError(result.CodeValidation, "unnecessary Row snapshot is not canonical")
	}
	wantBase, err := digest(struct {
		Scope            Scope         `json:"scope"`
		DatabaseRevision uint64        `json:"database_revision"`
		TableRevision    uint64        `json:"table_revision"`
		Columns          []ColumnShape `json:"columns"`
	}{plan.Scope, plan.DatabaseRevision, plan.TableRevision, plan.ColumnGuards})
	if err != nil || wantBase != plan.BaseSchemaHash {
		return planError(result.CodeValidation, "base Schema hash is invalid")
	}
	seed := plan
	seed.PlanID, seed.Hash = "", ""
	identity, err := digest(seed)
	if err != nil || plan.PlanID != "schema_plan_"+strings.TrimPrefix(identity, "sha256:")[:24] {
		return planError(result.CodeValidation, "plan identity is invalid")
	}
	hashed := plan
	hashed.Hash = ""
	wantHash, err := digest(hashed)
	if err != nil || wantHash != plan.Hash {
		return planError(result.CodeValidation, "plan hash is invalid")
	}
	return nil
}

// ApplyToSnapshot revalidates the full Catalog guard and materializes all
// actions in memory. Persistence remains the native Catalog authority's job.
func ApplyToSnapshot(plan Plan, database catalog.Database, table catalog.Table, now time.Time) (catalog.Database, catalog.Table, error) {
	if err := Validate(plan); err != nil {
		return catalog.Database{}, catalog.Table{}, err
	}
	if now.IsZero() || database.ID != plan.Scope.DatabaseID || table.ID != plan.Scope.TableID ||
		table.DatabaseID != database.ID || database.SchemaVersion != plan.DatabaseRevision ||
		table.SchemaVersion != plan.TableRevision ||
		(plan.Scope.Database != "" && !strings.EqualFold(plan.Scope.Database, database.Name)) ||
		(plan.Scope.Table != "" && !strings.EqualFold(plan.Scope.Table, table.Name)) {
		return catalog.Database{}, catalog.Table{}, planError(result.CodeRevisionConflict, "bound Catalog snapshot changed after planning")
	}
	current := make([]ColumnShape, 0, len(table.Columns))
	for _, column := range catalog.LiveColumns(table.Columns) {
		shape, err := existingShape(column)
		if err != nil {
			return catalog.Database{}, catalog.Table{}, planError(result.CodeInternal, "current Column schema is invalid")
		}
		current = append(current, shape)
	}
	sort.Slice(current, func(i, j int) bool { return current[i].ColumnID < current[j].ColumnID })
	if !reflect.DeepEqual(current, plan.ColumnGuards) {
		return catalog.Database{}, catalog.Table{}, planError(result.CodeRevisionConflict, "Column guards changed after planning")
	}
	actions := make(map[string]PlannedAction, len(plan.Actions))
	adds := make([]PlannedAction, 0)
	for _, action := range plan.Actions {
		if action.Action == ActionAdd {
			adds = append(adds, action)
		} else {
			actions[action.Before.ColumnID] = action
		}
	}
	now = now.UTC()
	columns := make([]catalog.Column, 0, len(table.Columns)+len(adds))
	for _, column := range table.Columns {
		action, changed := actions[column.ID]
		if !changed {
			columns = append(columns, column)
			continue
		}
		if action.Action == ActionDrop {
			// DROP_COLUMN archives rather than removes. Nothing is destroyed:
			// the definition and every Row's stored value stay put, the Column
			// simply stops resolving by name. That is what makes the action
			// reversible, and it also keeps existing Rows readable — removing
			// the Column outright left a value the Catalog no longer listed.
			archived := column
			archived.ArchivedAt, archived.ArchivedReason = &now, "DROP_COLUMN"
			archived.SchemaVersion++
			archived.UpdatedAt = now
			columns = append(columns, archived)
			continue
		}
		columns = append(columns, columnFromShape(*action.After, column.CreatedAt, now))
	}
	for _, action := range adds {
		columns = append(columns, columnFromShape(*action.After, now, now))
	}
	table.Columns = columns
	table.SchemaVersion++
	table.UpdatedAt = now
	database.SchemaVersion++
	database.UpdatedAt = now
	database.Tables = append([]catalog.Table(nil), database.Tables...)
	found := false
	for index := range database.Tables {
		if database.Tables[index].ID == table.ID {
			if database.Tables[index].SchemaVersion != plan.TableRevision {
				return catalog.Database{}, catalog.Table{}, planError(result.CodeRevisionConflict, "Database Table snapshot changed after planning")
			}
			database.Tables[index], found = table, true
		}
	}
	if !found {
		return catalog.Database{}, catalog.Table{}, planError(result.CodeRevisionConflict, "planned Table is absent from Database snapshot")
	}
	return database, table, nil
}

// CompensationProposal returns an inverse intent, never an executable token.
// Callers must submit it through PLAN again against the supplied Table revision.
func CompensationProposal(plan Plan, expectedTableRevision uint64) (Proposal, bool) {
	if Validate(plan) != nil || expectedTableRevision == 0 || plan.Impact.Destructive {
		return Proposal{}, false
	}
	changes := make([]ChangeProposal, 0, len(plan.Actions))
	for _, action := range plan.Actions {
		change := ChangeProposal{ID: "compensate_" + action.ChangeID}
		switch action.Action {
		case ActionAdd:
			change.Action, change.ColumnID, change.ExpectedRevision = ActionDrop, action.After.ColumnID, action.After.Revision
		case ActionRename:
			change.Action, change.ColumnID, change.ExpectedRevision = ActionRename, action.After.ColumnID, action.After.Revision
			change.NewName = action.Before.Name
		case ActionAlter:
			definition := definitionFromShape(*action.Before)
			change.Action, change.ColumnID, change.ExpectedRevision, change.Definition = ActionAlter, action.After.ColumnID, action.After.Revision, &definition
		default:
			return Proposal{}, false
		}
		changes = append(changes, change)
	}
	return Proposal{Version: ProposalVersion, ID: "compensate_" + plan.PlanID, Actor: plan.Actor,
		SourceEventID: "schema-compensation:" + plan.PlanID, Reason: "Compensate Schema plan " + plan.PlanID,
		ExpectedTableRevision: expectedTableRevision, Changes: changes}, true
}

func validateShape(shape ColumnShape) error {
	definition := definitionFromShape(shape)
	want, err := definitionShape(shape.ColumnID, shape.Aliases, shape.Revision, definition)
	if err != nil || !reflect.DeepEqual(want, shape) {
		return planError(result.CodeValidation, "Column shape is invalid or non-canonical")
	}
	return nil
}

func definitionFromShape(shape ColumnShape) catalog.ColumnDefinition {
	declaration := shape.Type
	if shape.Type == "TEXT" && shape.MaxCharacters > 0 {
		declaration = "TEXT(" + strconv.Itoa(shape.MaxCharacters) + ")"
	}
	return catalog.ColumnDefinition{Name: shape.Name, Type: declaration, Nullable: shape.Nullable,
		Purpose: shape.Purpose, SemanticRole: shape.SemanticRole}
}

func columnFromShape(shape ColumnShape, createdAt, updatedAt time.Time) catalog.Column {
	return catalog.Column{ID: shape.ColumnID, Name: shape.Name, Aliases: append([]string(nil), shape.Aliases...),
		Type: shape.Type, MaxCharacters: shape.MaxCharacters, Nullable: shape.Nullable, Purpose: shape.Purpose,
		SemanticRole: shape.SemanticRole, SchemaVersion: shape.Revision,
		CreatedAt: createdAt.UTC(), UpdatedAt: updatedAt.UTC()}
}

func strictlySortedColumns(values []ColumnShape) bool {
	for index, value := range values {
		if blank(value.ColumnID) || (index > 0 && values[index-1].ColumnID >= value.ColumnID) {
			return false
		}
	}
	return true
}

func strictlySortedRows(values []RowGuard) bool {
	for index, value := range values {
		if blank(value.RowID) || value.Revision == 0 || (index > 0 && values[index-1].RowID >= value.RowID) {
			return false
		}
	}
	return true
}

func strictlySortedActions(values []PlannedAction) bool {
	for index, value := range values {
		if blank(value.ChangeID) || (index > 0 && values[index-1].ChangeID >= value.ChangeID) {
			return false
		}
	}
	return true
}
