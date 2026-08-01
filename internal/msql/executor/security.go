package executor

import (
	"context"
	"strings"

	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/security"
)

func (engine *Engine) authorizeStatement(ctx context.Context, statement ast.Statement) error {
	authorization, present := security.AuthorizationFrom(ctx)
	if !present {
		return nil
	}
	if err := authorization.Validate(); err != nil {
		return normalizeError(err)
	}
	for _, database := range statementDatabaseNames(statement) {
		if err := engine.authorizeDatabaseReference(ctx, database); err != nil {
			return err
		}
	}
	return nil
}

func (engine *Engine) authorizeDatabaseReference(ctx context.Context, reference string) error {
	authorization, present := security.AuthorizationFrom(ctx)
	if !present {
		return nil
	}
	if err := authorization.Validate(); err != nil {
		return normalizeError(err)
	}
	if security.AllowsAnyDatabase(authorization, reference) {
		return nil
	}
	if engine != nil && engine.catalog != nil {
		database, err := engine.catalog.DescribeDatabase(ctx, reference)
		if err == nil && security.AllowsAnyDatabase(authorization, database.ID, database.Name) {
			return nil
		}
	}
	return normalizeError(security.RequireAnyDatabase(ctx, reference))
}

func statementDatabaseNames(statement ast.Statement) []string {
	databases := []string{}
	appendDatabase := func(name ast.Name) {
		if len(name.Parts) >= 1 {
			databases = append(databases, name.Parts[0].Value)
		}
	}
	appendQualifiedTable := func(name ast.Name) {
		if len(name.Parts) >= 2 {
			databases = append(databases, name.Parts[0].Value)
		}
	}
	switch {
	case statement.Show != nil:
		if statement.Show.Database != nil {
			appendDatabase(*statement.Show.Database)
		}
		if statement.Show.Table != nil {
			appendQualifiedTable(*statement.Show.Table)
		}
	case statement.Describe != nil:
		if strings.EqualFold(statement.Describe.Object, "DATABASE") {
			appendDatabase(statement.Describe.Name)
		} else {
			appendQualifiedTable(statement.Describe.Name)
		}
	case statement.Create != nil:
		if strings.EqualFold(statement.Create.Object, "DATABASE") {
			appendDatabase(statement.Create.Name)
		} else {
			appendQualifiedTable(statement.Create.Name)
		}
	case statement.Alter != nil:
		if strings.EqualFold(statement.Alter.Object, "DATABASE") {
			appendDatabase(statement.Alter.Name)
		} else {
			appendQualifiedTable(statement.Alter.Name)
		}
	case statement.Select != nil:
		appendQualifiedTable(statement.Select.From)
	case statement.Insert != nil:
		appendQualifiedTable(statement.Insert.Table)
	case statement.Update != nil:
		appendQualifiedTable(statement.Update.Table)
	case statement.Delete != nil:
		appendQualifiedTable(statement.Delete.Table)
	case statement.Restore != nil:
		appendQualifiedTable(statement.Restore.Table)
	case statement.Reshape != nil:
		appendQualifiedTable(statement.Reshape.Table)
	case statement.PlanRoute != nil:
		appendQualifiedTable(statement.PlanRoute.Table)
	case statement.ApplyRoute != nil:
		appendQualifiedTable(statement.ApplyRoute.Table)
	case statement.Relate != nil:
		appendQualifiedTable(statement.Relate.SourceTable)
		appendQualifiedTable(statement.Relate.TargetTable)
	case statement.Package != nil && statement.Package.Action == "PACK":
		appendDatabase(statement.Package.Database)
	}
	return databases
}
