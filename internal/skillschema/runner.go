package skillschema

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/logical"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/security"
)

type Runner struct{ tool Tool }

func New(tool Tool) *Runner { return &Runner{tool: tool} }

func (runner *Runner) Run(ctx context.Context, plan Plan) (Report, error) {
	if err := validatePlan(plan); err != nil {
		return Report{}, err
	}
	if runner == nil || runner.tool == nil {
		return Report{}, schemaError(result.CodeInternal, "schema tool is not configured")
	}
	report := Report{Calls: []Call{}}
	if plan.Ensure != nil {
		return runner.ensure(ctx, plan, &report)
	}
	return runner.migrate(ctx, plan, &report)
}

func (runner *Runner) ensure(ctx context.Context, plan Plan, report *Report) (Report, error) {
	definition := plan.Ensure
	databases, err := runner.rows(ctx, report, Call{
		Phase: PhaseDiscover, ID: "discover-databases",
		Request: request(plan.ID+"-discover-databases", "SHOW DATABASES COMPACT", plan),
	})
	if err != nil {
		return *report, err
	}
	databaseRow, found := findNamed(databases, append([]string{definition.Database.Name}, definition.DatabaseSynonyms...))
	databaseAction := ObjectReused
	if !found {
		databaseAction = ObjectCreated
		source := "CREATE DATABASE " + identifier(definition.Database.Name) +
			" PURPOSE " + literal(definition.Database.Purpose) + " SCOPE " + literal(definition.Database.Scope)
		if definition.Database.AntiScope != "" {
			source += " ANTI SCOPE " + literal(definition.Database.AntiScope)
		}
		rows, applyErr := runner.rows(ctx, report, Call{
			Phase: PhaseApply, ID: "create-database",
			Request: request(plan.ID+"-create-database", source, plan),
		})
		if applyErr != nil {
			return *report, applyErr
		}
		databaseRow, err = oneRow(rows, "create database")
		if err != nil {
			return *report, err
		}
	}
	databaseName, _ := stringValue(databaseRow["name"])
	if !authorized(plan.AuthorizedDatabases, databaseName) {
		return *report, schemaError(result.CodePermissionDenied, "resolved database %q is not authorized", databaseName)
	}
	tables, err := runner.rows(ctx, report, Call{
		Phase: PhaseDiscover, ID: "discover-tables",
		Request: request(plan.ID+"-discover-tables", "SHOW TABLES FROM "+identifier(databaseName)+" COMPACT", plan),
	})
	if err != nil {
		return *report, err
	}
	tableRow, found := findNamed(tables, append([]string{definition.Table.Name}, definition.TableSynonyms...))
	tableAction := ObjectReused
	if !found {
		tableAction = ObjectCreated
		source, compileErr := createTableMSQL(databaseName, definition.Table)
		if compileErr != nil {
			return *report, compileErr
		}
		rows, applyErr := runner.rows(ctx, report, Call{
			Phase: PhaseApply, ID: "create-table",
			Request: request(plan.ID+"-create-table", source, plan),
		})
		if applyErr != nil {
			return *report, applyErr
		}
		tableRow, err = oneRow(rows, "create table")
		if err != nil {
			return *report, err
		}
	}
	report.Receipt = Receipt{
		Version: ReceiptVersion, PlanID: plan.ID, Status: ReceiptApplied,
		Database: objectReceipt(databaseRow, databaseAction, "database_id"),
		Table:    objectReceipt(tableRow, tableAction, "table_id"), Verified: true,
	}
	return *report, nil
}

func (runner *Runner) migrate(ctx context.Context, plan Plan, report *Report) (Report, error) {
	impact, _ := Preview(plan)
	migration := plan.Migration
	databaseRows, err := runner.rows(ctx, report, Call{
		Phase: PhasePreflight, ID: "describe-database",
		Request: request(plan.ID+"-describe-database", "DESCRIBE DATABASE "+identifier(migration.Database)+" COMPACT", plan),
	})
	if err != nil {
		return *report, err
	}
	if err := expectVersion(databaseRows, migration.ExpectedDatabaseVersion, "database"); err != nil {
		return *report, err
	}
	completed := make([]MigrationStep, 0, len(migration.Steps))
	for _, step := range migration.Steps {
		rows, preflightErr := runner.rows(ctx, report, Call{
			Phase: PhasePreflight, ID: "describe-" + step.ID,
			Request: request(plan.ID+"-describe-"+step.ID, describeMSQL(migration.Database, step), plan),
		})
		if preflightErr != nil {
			return *report, preflightErr
		}
		if preflightErr = expectVersion(rows, step.ExpectedVersion, step.ID); preflightErr != nil {
			return *report, preflightErr
		}
		_, applyErr := runner.rows(ctx, report, Call{
			Phase: PhaseApply, ID: step.ID,
			Request: request(plan.ID+"-"+step.ID, alterMSQL(migration.Database, step), plan),
		})
		if applyErr != nil {
			return runner.rollback(ctx, plan, report, impact, completed, applyErr)
		}
		completed = append(completed, inverse(step))
	}
	verified := true
	for index := range completed {
		forward := migration.Steps[index]
		rows, verifyErr := runner.rows(ctx, report, Call{
			Phase: PhaseVerify, ID: "verify-" + forward.ID,
			Request: request(plan.ID+"-verify-"+forward.ID, describeForwardMSQL(migration.Database, forward), plan),
		})
		if verifyErr != nil || !rowHasName(rows, forward.NewName) {
			verified = false
			break
		}
	}
	report.Receipt = Receipt{
		Version: ReceiptVersion, PlanID: plan.ID, Status: ReceiptApplied,
		Impact: &impact, Applied: append([]MigrationStep(nil), migration.Steps...), Verified: verified,
	}
	return *report, nil
}

func (runner *Runner) rollback(ctx context.Context, plan Plan, report *Report, impact Impact, completed []MigrationStep, cause error) (Report, error) {
	rolledBack := make([]MigrationStep, 0, len(completed))
	rollbackOK := true
	for index := len(completed) - 1; index >= 0; index-- {
		step := completed[index]
		if _, err := runner.rows(ctx, report, Call{
			Phase: PhaseRollback, ID: step.ID,
			Request: request(plan.ID+"-"+step.ID, alterMSQL(plan.Migration.Database, step), plan),
		}); err != nil {
			rollbackOK = false
			break
		}
		rolledBack = append(rolledBack, step)
	}
	verified := rollbackOK
	if rollbackOK {
		for _, step := range rolledBack {
			rows, err := runner.rows(ctx, report, Call{
				Phase: PhaseVerify, ID: "verify-" + step.ID,
				Request: request(plan.ID+"-verify-"+step.ID, describeAfterMSQL(plan.Migration.Database, step), plan),
			})
			if err != nil || !rowHasName(rows, step.NewName) {
				verified = false
				break
			}
		}
	}
	status := ReceiptRolledBack
	if !rollbackOK {
		status = ReceiptRollbackFailed
	}
	report.Receipt = Receipt{
		Version: ReceiptVersion, PlanID: plan.ID, Status: status, Impact: &impact,
		Applied:   append([]MigrationStep(nil), plan.Migration.Steps[:len(completed)]...),
		Rollbacks: rolledBack, Verified: verified,
	}
	return *report, cause
}

func (runner *Runner) rows(ctx context.Context, report *Report, call Call) ([]result.Row, error) {
	if err := ctx.Err(); err != nil {
		return nil, schemaError(result.CodeCancelled, "schema operation cancelled: %v", err)
	}
	report.Calls = append(report.Calls, call)
	envelope, err := runner.tool.Invoke(ctx, call)
	if err != nil {
		return nil, schemaError(result.CodeInternal, "%s tool call failed: %v", call.ID, err)
	}
	if err := envelope.Validate(); err != nil {
		return nil, schemaError(result.CodeInternal, "%s returned invalid envelope: %v", call.ID, err)
	}
	if !envelope.OK {
		return nil, envelopeError(envelope, call.ID+" failed")
	}
	if len(envelope.Results) != 1 || envelope.Results[0].Status != result.StatusSucceeded {
		return nil, schemaError(result.CodeInternal, "%s returned an unexpected result", call.ID)
	}
	return envelope.Results[0].Rows, nil
}

func request(id, source string, plan Plan) executor.BatchRequest {
	return executor.BatchRequest{RequestID: id, Source: source, Statements: []executor.StatementInput{{
		Authorization: security.Authorization{
			Version: security.AuthorizationVersion, Actor: plan.Actor,
			AuthorizedDatabases: append([]string{}, plan.AuthorizedDatabases...),
			DefaultLevel:        security.LevelStructural,
		},
	}}}
}

func findNamed(rows []result.Row, candidates []string) (result.Row, bool) {
	for _, row := range rows {
		names := []string{}
		if name, ok := stringValue(row["name"]); ok {
			names = append(names, name)
		}
		names = append(names, stringSlice(row["aliases"])...)
		for _, name := range names {
			for _, candidate := range candidates {
				if strings.EqualFold(strings.TrimSpace(name), strings.TrimSpace(candidate)) {
					return row, true
				}
			}
		}
	}
	return nil, false
}

func oneRow(rows []result.Row, operation string) (result.Row, error) {
	if len(rows) != 1 {
		return nil, schemaError(result.CodeInternal, "%s returned %d rows", operation, len(rows))
	}
	return rows[0], nil
}

func objectReceipt(row result.Row, action ObjectAction, idKey string) ObjectReceipt {
	id, _ := stringValue(row[idKey])
	name, _ := stringValue(row["name"])
	return ObjectReceipt{Action: action, ID: id, Name: name}
}

func expectVersion(rows []result.Row, want uint64, object string) error {
	row, err := oneRow(rows, "describe "+object)
	if err != nil {
		return err
	}
	got, ok := uintValue(row["schema_version"])
	if !ok || got != want {
		return schemaError(result.CodeRevisionConflict, "%s schema version = %d, want %d", object, got, want)
	}
	return nil
}

func rowHasName(rows []result.Row, want string) bool {
	return len(rows) == 1 && strings.EqualFold(fmt.Sprint(rows[0]["name"]), want)
}

func createTableMSQL(database string, table catalog.TableDefinition) (string, error) {
	parts := make([]string, len(table.Columns))
	for index, column := range table.Columns {
		declaration, err := logical.ParseDeclaration(column.Type)
		if err != nil {
			return "", schemaError(result.CodeValidation, "column %q has invalid type", column.Name)
		}
		typeSource := string(declaration.Kind)
		if declaration.Kind == logical.KindText && declaration.MaxCharacters != logical.DefaultTextCharacters {
			typeSource += "(" + strconv.Itoa(declaration.MaxCharacters) + ")"
		}
		nullability := " NOT NULL"
		if column.Nullable {
			nullability = " NULL"
		}
		parts[index] = identifier(column.Name) + " " + typeSource + nullability + " PURPOSE " + literal(column.Purpose)
		if column.SemanticRole != "" {
			parts[index] += " ROLE " + column.SemanticRole
		}
	}
	source := "CREATE TABLE " + identifier(database) + "." + identifier(table.Name) +
		" PURPOSE " + literal(table.Purpose) + " ROW SEMANTICS " + literal(table.RowSemantics)
	if table.Scope != "" {
		source += " SCOPE " + literal(table.Scope)
	}
	if table.AntiScope != "" {
		source += " ANTI SCOPE " + literal(table.AntiScope)
	}
	return source + " (" + strings.Join(parts, ", ") + ")", nil
}

func describeMSQL(database string, step MigrationStep) string {
	if step.Action == RenameColumn {
		return "DESCRIBE COLUMN " + identifier(database) + "." + identifier(step.Table) + "." + identifier(step.Column) + " COMPACT"
	}
	return "DESCRIBE TABLE " + identifier(database) + "." + identifier(step.Table) + " COMPACT"
}

func describeAfterMSQL(database string, inverse MigrationStep) string {
	if inverse.Action == RenameColumn {
		return "DESCRIBE COLUMN " + identifier(database) + "." + identifier(inverse.Table) + "." + identifier(inverse.NewName) + " COMPACT"
	}
	return "DESCRIBE TABLE " + identifier(database) + "." + identifier(inverse.NewName) + " COMPACT"
}

func describeForwardMSQL(database string, step MigrationStep) string {
	if step.Action == RenameColumn {
		return "DESCRIBE COLUMN " + identifier(database) + "." + identifier(step.Table) + "." + identifier(step.NewName) + " COMPACT"
	}
	return "DESCRIBE TABLE " + identifier(database) + "." + identifier(step.NewName) + " COMPACT"
}

func alterMSQL(database string, step MigrationStep) string {
	if step.Action == RenameColumn {
		return "ALTER TABLE " + identifier(database) + "." + identifier(step.Table) + " RENAME COLUMN " + identifier(step.Column) + " TO " + identifier(step.NewName)
	}
	return "ALTER TABLE " + identifier(database) + "." + identifier(step.Table) + " RENAME TO " + identifier(step.NewName)
}

func inverse(step MigrationStep) MigrationStep {
	if step.Action == RenameColumn {
		return MigrationStep{ID: "rollback-" + step.ID, Action: step.Action, Table: step.Table, Column: step.NewName, NewName: step.Column}
	}
	return MigrationStep{ID: "rollback-" + step.ID, Action: step.Action, Table: step.NewName, NewName: step.Table}
}

func identifier(value string) string { return "`" + strings.ReplaceAll(value, "`", "``") + "`" }
func literal(value string) string    { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }

func stringValue(value any) (string, bool) {
	text, ok := value.(string)
	return text, ok
}

func stringSlice(value any) []string {
	switch values := value.(type) {
	case []string:
		return values
	case []any:
		result := make([]string, 0, len(values))
		for _, value := range values {
			if text, ok := value.(string); ok {
				result = append(result, text)
			}
		}
		return result
	default:
		return nil
	}
}

func uintValue(value any) (uint64, bool) {
	switch typed := value.(type) {
	case json.Number:
		parsed, err := strconv.ParseUint(string(typed), 10, 64)
		return parsed, err == nil
	case uint64:
		return typed, true
	case int:
		return uint64(typed), typed >= 0
	case float64:
		return uint64(typed), typed >= 0 && typed == float64(uint64(typed))
	default:
		return 0, false
	}
}

func envelopeError(envelope result.Envelope, message string) error {
	if envelope.Error != nil {
		return schemaError(envelope.Error.Code, "%s: %s", message, envelope.Error.Message)
	}
	for _, statement := range envelope.Results {
		if statement.Error != nil {
			return schemaError(statement.Error.Code, "%s: %s", message, statement.Error.Message)
		}
	}
	return schemaError(result.CodeInternal, "%s", message)
}
