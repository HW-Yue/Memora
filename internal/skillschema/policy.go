package skillschema

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/HW-Yue/Memora/internal/logical"
	"github.com/HW-Yue/Memora/internal/result"
)

func Preview(plan Plan) (Impact, error) {
	if err := validateCommon(plan); err != nil {
		return Impact{}, err
	}
	if plan.Migration == nil || plan.Ensure != nil {
		return Impact{}, schemaError(result.CodeValidation, "preview requires exactly one migration")
	}
	migration := plan.Migration
	if strings.TrimSpace(migration.Database) == "" || migration.ExpectedDatabaseVersion == 0 {
		return Impact{}, schemaError(result.CodeValidation, "migration database and expected version are required")
	}
	if !authorized(plan.AuthorizedDatabases, migration.Database) {
		return Impact{}, schemaError(result.CodePermissionDenied, "database %q is not authorized", migration.Database)
	}
	if len(migration.Steps) == 0 || migration.MaxAffectedObjects <= 0 {
		return Impact{}, schemaError(result.CodeValidation, "migration steps and a positive impact limit are required")
	}
	if len(migration.Steps) > migration.MaxAffectedObjects {
		return Impact{}, schemaError(result.CodeValidation, "migration affects %d objects; limit is %d", len(migration.Steps), migration.MaxAffectedObjects)
	}
	seen := map[string]struct{}{}
	rollback := make([]MigrationStep, len(migration.Steps))
	for index, step := range migration.Steps {
		if strings.TrimSpace(step.ID) == "" || strings.TrimSpace(step.Table) == "" ||
			strings.TrimSpace(step.NewName) == "" || step.ExpectedVersion == 0 {
			return Impact{}, schemaError(result.CodeValidation, "migration step %d is incomplete", index)
		}
		if _, exists := seen[step.ID]; exists {
			return Impact{}, schemaError(result.CodeValidation, "duplicate migration step id %q", step.ID)
		}
		seen[step.ID] = struct{}{}
		switch step.Action {
		case RenameTable:
			rollback[len(rollback)-1-index] = MigrationStep{
				ID: "rollback-" + step.ID, Action: RenameTable,
				Table: step.NewName, NewName: step.Table,
			}
		case RenameColumn:
			if strings.TrimSpace(step.Column) == "" {
				return Impact{}, schemaError(result.CodeValidation, "rename column step %q requires a column", step.ID)
			}
			rollback[len(rollback)-1-index] = MigrationStep{
				ID: "rollback-" + step.ID, Action: RenameColumn, Table: step.Table,
				Column: step.NewName, NewName: step.Column,
			}
		default:
			return Impact{}, schemaError(result.CodeUnsupported, "migration action %q is not reversible or supported", step.Action)
		}
	}
	hashInput, _ := json.Marshal(struct {
		Database string          `json:"database"`
		Version  uint64          `json:"expected_database_version"`
		Steps    []MigrationStep `json:"steps"`
	}{migration.Database, migration.ExpectedDatabaseVersion, migration.Steps})
	digest := sha256.Sum256(hashInput)
	return Impact{
		AffectedObjects: len(migration.Steps), AffectedRows: 0, Reversible: true,
		Rollback: rollback, Hash: hex.EncodeToString(digest[:]),
	}, nil
}

func validatePlan(plan Plan) error {
	if err := validateCommon(plan); err != nil {
		return err
	}
	if (plan.Ensure == nil) == (plan.Migration == nil) {
		return schemaError(result.CodeValidation, "plan requires exactly one of ensure or migration")
	}
	if plan.Migration != nil {
		_, err := Preview(plan)
		return err
	}
	ensure := plan.Ensure
	if strings.TrimSpace(ensure.Database.Name) == "" || strings.TrimSpace(ensure.Database.Purpose) == "" || strings.TrimSpace(ensure.Database.Scope) == "" {
		return schemaError(result.CodeValidation, "database name, purpose, and scope are required")
	}
	if strings.TrimSpace(ensure.Table.Name) == "" || strings.TrimSpace(ensure.Table.Purpose) == "" || strings.TrimSpace(ensure.Table.RowSemantics) == "" {
		return schemaError(result.CodeValidation, "table name, purpose, and row semantics are required")
	}
	if !anyAuthorized(plan.AuthorizedDatabases, append([]string{ensure.Database.Name}, ensure.DatabaseSynonyms...)) {
		return schemaError(result.CodePermissionDenied, "database candidates are not authorized")
	}
	if len(ensure.Table.Columns) == 0 {
		return schemaError(result.CodeValidation, "table requires at least one column")
	}
	for _, column := range ensure.Table.Columns {
		if strings.TrimSpace(column.Name) == "" || strings.TrimSpace(column.Purpose) == "" {
			return schemaError(result.CodeValidation, "column name, type, and purpose are required")
		}
		if _, err := logical.ParseDeclaration(column.Type); err != nil {
			return schemaError(result.CodeValidation, "column %q has invalid type: %v", column.Name, err)
		}
	}
	return nil
}

func validateCommon(plan Plan) error {
	if plan.Version != PlanVersion {
		return schemaError(result.CodeValidation, "unsupported plan version %q", plan.Version)
	}
	if strings.TrimSpace(plan.ID) == "" || strings.TrimSpace(plan.Actor) == "" ||
		strings.TrimSpace(plan.SourceEventID) == "" || strings.TrimSpace(plan.Reason) == "" {
		return schemaError(result.CodeValidation, "id, actor, source_event_id, and reason are required")
	}
	if len(plan.AuthorizedDatabases) == 0 {
		return schemaError(result.CodePermissionDenied, "authorized_databases is empty")
	}
	return nil
}

func authorized(allowed []string, name string) bool {
	for _, candidate := range allowed {
		if strings.EqualFold(strings.TrimSpace(candidate), strings.TrimSpace(name)) {
			return true
		}
	}
	return false
}

func anyAuthorized(allowed, candidates []string) bool {
	for _, candidate := range candidates {
		if authorized(allowed, candidate) {
			return true
		}
	}
	return false
}

func schemaError(code result.Code, format string, arguments ...any) error {
	return &Error{Code: code, Message: fmt.Sprintf(format, arguments...)}
}
