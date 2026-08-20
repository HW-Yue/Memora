package executor

import (
	"context"
	"time"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/relation"
	"github.com/HW-Yue/Memora/internal/result"
	datarow "github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/security"
)

// archiveCatalog is the optional container-archive capability. A backend that
// does not implement it simply reports ARCHIVE as unsupported instead of
// silently doing nothing.
type archiveCatalog interface {
	ArchiveDatabase(context.Context, string, string) (catalog.Database, error)
	UnarchiveDatabase(context.Context, string) (catalog.Database, error)
	ArchiveTable(context.Context, string, string, string) (catalog.Table, error)
	UnarchiveTable(context.Context, string, string) (catalog.Table, error)
	ArchiveColumn(context.Context, string, string, string, string) (catalog.Column, error)
	UnarchiveColumn(context.Context, string, string, string) (catalog.Column, error)
}

func (engine *Engine) archiveColumn(
	ctx context.Context,
	statement *ast.ArchiveStatement,
	bound bindings,
) (Output, error) {
	service, names, reason, err := engine.archiveTarget(ctx, statement, bound, 3)
	if err != nil {
		return Output{}, err
	}
	action := func() (catalog.Column, error) {
		return service.ArchiveColumn(ctx, names[0], names[1], names[2], reason)
	}
	if statement.Restore {
		action = func() (catalog.Column, error) {
			return service.UnarchiveColumn(ctx, names[0], names[1], names[2])
		}
	}
	column, err := action()
	if err != nil {
		return Output{}, normalizeError(err)
	}
	return archiveOutput(
		"COLUMN", column.ID, names[0]+"."+names[1]+"."+column.Name,
		column.ArchivedAt, column.ArchivedReason,
	), nil
}

func (engine *Engine) archiveDatabase(
	ctx context.Context,
	statement *ast.ArchiveStatement,
	bound bindings,
) (Output, error) {
	service, name, reason, err := engine.archiveTarget(ctx, statement, bound, 1)
	if err != nil {
		return Output{}, err
	}
	action := func() (catalog.Database, error) { return service.ArchiveDatabase(ctx, name[0], reason) }
	if statement.Restore {
		action = func() (catalog.Database, error) { return service.UnarchiveDatabase(ctx, name[0]) }
	}
	database, err := action()
	if err != nil {
		return Output{}, normalizeError(err)
	}
	return archiveOutput("DATABASE", database.ID, database.Name, database.ArchivedAt, database.ArchivedReason), nil
}

func (engine *Engine) archiveTable(
	ctx context.Context,
	statement *ast.ArchiveStatement,
	bound bindings,
) (Output, error) {
	service, names, reason, err := engine.archiveTarget(ctx, statement, bound, 2)
	if err != nil {
		return Output{}, err
	}
	action := func() (catalog.Table, error) { return service.ArchiveTable(ctx, names[0], names[1], reason) }
	if statement.Restore {
		action = func() (catalog.Table, error) { return service.UnarchiveTable(ctx, names[0], names[1]) }
	}
	table, err := action()
	if err != nil {
		return Output{}, normalizeError(err)
	}
	return archiveOutput("TABLE", table.ID, names[0]+"."+table.Name, table.ArchivedAt, table.ArchivedReason), nil
}

// archiveTarget resolves the object name, checks the reason and authorizes the
// target Database at L2. Archiving a container hides everything under it, so
// it is authorized exactly like structural DDL, never like a Row write.
func (engine *Engine) archiveTarget(
	ctx context.Context,
	statement *ast.ArchiveStatement,
	bound bindings,
	parts int,
) (archiveCatalog, []string, string, error) {
	service, ok := engine.catalog.(archiveCatalog)
	if !ok {
		return nil, nil, "", executeError(result.CodeUnsupported, "ARCHIVE is not supported by this backend")
	}
	names, err := archiveNameParts(statement.Name, parts)
	if err != nil {
		return nil, nil, "", err
	}
	reason := ""
	if !statement.Restore {
		if reason, err = routerString(statement.Reason, bound, "archive reason"); err != nil {
			return nil, nil, "", err
		}
	}
	if err := engine.authorizeDatabaseReferenceAtLevel(ctx, security.LevelStructural, names[0]); err != nil {
		return nil, nil, "", err
	}
	return service, names, reason, nil
}

// archiveRowRows and archiveRelationRows are the identity-preserving inverses.
// Re-creating the object with RELATE or INSERT would mint a new ID and break
// every reference to the old one, so UNARCHIVE has to land on the same object.
type archiveRowRows interface {
	RestoreRow(context.Context, string, string, string, datarow.WriteOptions) (datarow.Row, error)
}

type archiveRelationRows interface {
	RestoreRelation(context.Context, string, uint64) (relation.Relation, error)
}

func (engine *Engine) archiveRow(
	ctx context.Context,
	statement *ast.ArchiveStatement,
	bound bindings,
	options MutationOptions,
) (Output, error) {
	databaseName, tableName, _, err := engine.bindTable(ctx, statement.Name)
	if err != nil {
		return Output{}, err
	}
	rowID, err := routerString(statement.Target, bound, "Row ID")
	if err != nil {
		return Output{}, err
	}
	if !statement.Restore {
		reason, err := routerString(statement.Reason, bound, "archive reason")
		if err != nil {
			return Output{}, err
		}
		if options.Reason == "" {
			options.Reason = reason
		}
		value, err := engine.rows.Delete(ctx, databaseName, tableName, rowID, datarow.WriteOptions{
			ExpectedSchemaVersion: options.ExpectedSchemaVersion,
			ExpectedRevision:      options.ExpectedRevision,
			Metadata:              mutationMetadata(options),
		})
		if err != nil {
			return Output{}, normalizeError(err)
		}
		return archiveOutput("ROW", value.ID, databaseName+"."+tableName, nil, ""), nil
	}
	service, ok := engine.rows.(archiveRowRows)
	if !ok {
		return Output{}, executeError(result.CodeUnsupported, "UNARCHIVE ROW is not supported by this backend")
	}
	value, err := service.RestoreRow(ctx, databaseName, tableName, rowID, datarow.WriteOptions{
		ExpectedSchemaVersion: options.ExpectedSchemaVersion,
		ExpectedRevision:      options.ExpectedRevision,
		RouteLeafIDs:          options.RouteLeafIDs,
		Metadata:              mutationMetadata(options),
	})
	if err != nil {
		return Output{}, normalizeError(err)
	}
	return archiveOutput("ROW", value.ID, databaseName+"."+tableName, nil, ""), nil
}

func (engine *Engine) archiveRelation(
	ctx context.Context,
	statement *ast.ArchiveStatement,
	bound bindings,
	options MutationOptions,
) (Output, error) {
	relationID, err := routerString(statement.Target, bound, "relation ID")
	if err != nil {
		return Output{}, err
	}
	if !statement.Restore {
		if _, err := routerString(statement.Reason, bound, "archive reason"); err != nil {
			return Output{}, err
		}
		value, err := engine.rows.DeleteRelation(ctx, relationID, options.ExpectedRevision)
		if err != nil {
			return Output{}, normalizeError(err)
		}
		return archiveOutput("RELATION", value.ID, value.Type, nil, ""), nil
	}
	service, ok := engine.rows.(archiveRelationRows)
	if !ok {
		return Output{}, executeError(result.CodeUnsupported, "UNARCHIVE RELATION is not supported by this backend")
	}
	value, err := service.RestoreRelation(ctx, relationID, options.ExpectedRevision)
	if err != nil {
		return Output{}, normalizeError(err)
	}
	return archiveOutput("RELATION", value.ID, value.Type, nil, ""), nil
}

func archiveNameParts(name ast.Name, parts int) ([]string, error) {
	if len(name.Parts) != parts {
		return nil, executeError(result.CodeValidation, "ARCHIVE target name is not qualified correctly")
	}
	values := make([]string, 0, parts)
	for _, part := range name.Parts {
		if part.Value == "" {
			return nil, executeError(result.CodeValidation, "ARCHIVE target name is empty")
		}
		values = append(values, part.Value)
	}
	return values, nil
}

func archiveOutput(object, id, name string, archivedAt *time.Time, reason string) Output {
	row := result.Row{
		"object": object, "id": id, "name": name,
		"archived": archivedAt != nil, "archived_at": nil, "archived_reason": nil,
	}
	if archivedAt != nil {
		row["archived_at"] = archivedAt.UTC().Format(time.RFC3339Nano)
	}
	if reason != "" {
		row["archived_reason"] = reason
	}
	return Output{
		Columns: []result.Column{
			{Name: "object", Type: "TEXT"},
			{Name: "id", Type: "ID"},
			{Name: "name", Type: "TEXT"},
			{Name: "archived", Type: "BOOLEAN"},
			{Name: "archived_at", Type: "TIMESTAMP", Nullable: true},
			{Name: "archived_reason", Type: "TEXT", Nullable: true},
		},
		Rows:         []result.Row{row},
		AffectedRows: 1,
	}
}
