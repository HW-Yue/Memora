package executor

import (
	"context"
	"strings"

	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/security"
)

func (engine *Engine) authorizeStatement(ctx context.Context, statement ast.Statement) error {
	authorization, present := security.AuthorizationFrom(ctx)
	if present {
		if err := authorization.Validate(); err != nil {
			return normalizeError(err)
		}
	}
	level := statementRiskLevel(statement)
	databases := statementDatabaseNames(statement)
	for _, database := range databases {
		if present {
			if err := engine.authorizeDatabaseReferenceAtLevel(ctx, level, database); err != nil {
				return err
			}
		}
		if level != security.LevelRead {
			if err := engine.requireWritableDatabaseReference(ctx, database); err != nil {
				return err
			}
		}
	}
	if present && len(databases) == 0 && level != security.LevelRead {
		if err := security.RequireLevel(ctx, level); err != nil {
			return normalizeError(err)
		}
	}
	return nil
}

func (engine *Engine) requireWritableDatabaseReference(ctx context.Context, reference string) error {
	if engine == nil || engine.catalog == nil {
		return nil
	}
	database, err := engine.catalog.DescribeDatabase(ctx, reference)
	if err != nil {
		return nil
	}
	if database.ReadOnly {
		return executeError(result.CodePermissionDenied, "installed Database is read-only; fork it before mutation")
	}
	return nil
}

func (engine *Engine) authorizeDatabaseReference(ctx context.Context, reference string) error {
	return engine.authorizeDatabaseReferenceAtLevel(ctx, security.LevelRead, reference)
}

func (engine *Engine) authorizeDatabaseReferenceAtLevel(ctx context.Context, level security.RiskLevel, reference string) error {
	authorization, present := security.AuthorizationFrom(ctx)
	if !present {
		return nil
	}
	if err := authorization.Validate(); err != nil {
		return normalizeError(err)
	}
	if security.AllowsAnyDatabaseLevel(authorization, level, reference) {
		return nil
	}
	if engine != nil && engine.catalog != nil {
		database, err := engine.catalog.DescribeDatabase(ctx, reference)
		selectors := append([]string{database.ID, database.Name}, database.Aliases...)
		if err == nil && security.AllowsAnyDatabaseLevel(authorization, level, selectors...) {
			return nil
		}
	}
	return normalizeError(security.RequireAnyDatabaseLevel(ctx, level, reference))
}

func statementRiskLevel(statement ast.Statement) security.RiskLevel {
	switch {
	case statement.Insert != nil, statement.Update != nil, statement.Delete != nil,
		statement.Restore != nil, statement.Relate != nil, statement.Unrelate != nil,
		// Archiving a Row or a Relation is a bounded, reversible write on one
		// object; archiving a container hides everything beneath it and is
		// classified with structural DDL below.
		statement.Archive != nil && (statement.Archive.Object == "ROW" || statement.Archive.Object == "RELATION"),
		statement.Assimilation != nil && statement.Assimilation.Action == "SUBMIT":
		return security.LevelWrite
	case statement.Create != nil, statement.Alter != nil, statement.Reshape != nil,
		statement.CreateRoute != nil, statement.RenameRoute != nil,
		statement.UpdateRoute != nil, statement.DeleteRoute != nil, statement.Archive != nil,
		statement.ApplyRoute != nil, statement.ApplySchema != nil,
		statement.Configuration != nil,
		statement.Rebuild != nil,
		statement.Package != nil && statement.Package.Action == "INSTALL":
		return security.LevelStructural
	default:
		return security.LevelRead
	}
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
	case statement.Archive != nil:
		if len(statement.Archive.Name.Parts) > 0 {
			appendDatabase(statement.Archive.Name)
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
	case statement.PlanSchema != nil:
		appendQualifiedTable(statement.PlanSchema.Table)
	case statement.ApplyRoute != nil:
		appendQualifiedTable(statement.ApplyRoute.Table)
	case statement.ApplySchema != nil:
		appendQualifiedTable(statement.ApplySchema.Table)
	case statement.Relate != nil:
		appendQualifiedTable(statement.Relate.SourceTable)
		appendQualifiedTable(statement.Relate.TargetTable)
	case statement.CreateRoute != nil && statement.CreateRoute.Table != nil:
		appendQualifiedTable(*statement.CreateRoute.Table)
	case statement.Package != nil && statement.Package.Action == "PACK":
		appendDatabase(statement.Package.Database)
	case statement.Assimilation != nil:
		appendDatabase(statement.Assimilation.Database)
	}
	return databases
}
