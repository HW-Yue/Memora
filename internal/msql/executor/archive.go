package executor

import (
	"context"
	"time"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/result"
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
