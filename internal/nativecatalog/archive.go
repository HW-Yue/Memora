package nativecatalog

import (
	"context"
	"strings"

	"github.com/HW-Yue/Memora/internal/catalog"
)

// Archiving is Memora's only delete semantics for a container. It never
// destroys anything and never rewrites a descendant: a Table keeps every Row
// and a Database keeps every Table exactly as they were, and visibility is
// recomputed at read time from "self or any ancestor archived". A 5,000-Row
// Table must not cost 5,000 Row revisions to hide, and after F226 that write
// burst is also the shape most likely to poison the Database.

// ArchiveDatabase hides a Database and everything under it. Reason is
// mandatory: an archive with no recorded why is indistinguishable from a
// mistake six months later.
func (service *Service) ArchiveDatabase(ctx context.Context, name, reason string) (catalog.Database, error) {
	if err := required("database", name, "reason", reason); err != nil {
		return catalog.Database{}, err
	}
	var archived catalog.Database
	err := service.mutate(ctx, func(databases *[]catalog.Database) error {
		database, ok := findDatabase(*databases, name)
		if !ok {
			return catalogFailure(catalog.CodeNotFound, "database", name, "")
		}
		if database.Archived() {
			return catalogFailure(catalog.CodeAlreadyExists, "archived database", database.Name, "")
		}
		if database.ReadOnly || strings.TrimSpace(database.PackageSHA256) != "" {
			return catalogFailure(
				catalog.CodeValidation, "database", database.Name,
				"a writable, non-package Database (uninstall the package instead)",
			)
		}
		now := service.clock.Now().UTC()
		database.ArchivedAt, database.ArchivedReason = &now, strings.TrimSpace(reason)
		touchDatabase(database, now)
		archived = *database
		return nil
	})
	return archived, err
}

// UnarchiveDatabase is the exact inverse. It restores only this Database; a
// Table archived by its own earlier decision stays archived, because
// UNARCHIVE must not silently reverse a decision it did not make.
func (service *Service) UnarchiveDatabase(ctx context.Context, name string) (catalog.Database, error) {
	var restored catalog.Database
	err := service.mutate(ctx, func(databases *[]catalog.Database) error {
		database, ok := findDatabase(*databases, name)
		if !ok {
			return catalogFailure(catalog.CodeNotFound, "database", name, "")
		}
		if !database.Archived() {
			return catalogFailure(catalog.CodeValidation, "database", database.Name, "an archived Database")
		}
		now := service.clock.Now().UTC()
		database.ArchivedAt, database.ArchivedReason = nil, ""
		touchDatabase(database, now)
		restored = *database
		return nil
	})
	return restored, err
}

func (service *Service) ArchiveTable(ctx context.Context, databaseName, tableName, reason string) (catalog.Table, error) {
	if err := required("table", tableName, "reason", reason); err != nil {
		return catalog.Table{}, err
	}
	var archived catalog.Table
	err := service.mutate(ctx, func(databases *[]catalog.Database) error {
		database, table, err := locateTable(*databases, databaseName, tableName)
		if err != nil {
			return err
		}
		if database.Archived() {
			return archivedFailure("table", table.Name, database)
		}
		if table.Archived() {
			return catalogFailure(catalog.CodeAlreadyExists, "archived table", table.Name, "")
		}
		if database.ReadOnly || strings.TrimSpace(database.PackageSHA256) != "" {
			return catalogFailure(
				catalog.CodeValidation, "table", table.Name,
				"a Table in a writable, non-package Database",
			)
		}
		now := service.clock.Now().UTC()
		table.ArchivedAt, table.ArchivedReason = &now, strings.TrimSpace(reason)
		touchTable(table, now)
		touchDatabase(database, now)
		archived = *table
		return nil
	})
	return archived, err
}

func (service *Service) UnarchiveTable(ctx context.Context, databaseName, tableName string) (catalog.Table, error) {
	var restored catalog.Table
	err := service.mutate(ctx, func(databases *[]catalog.Database) error {
		database, table, err := locateTable(*databases, databaseName, tableName)
		if err != nil {
			return err
		}
		if database.Archived() {
			return archivedFailure("table", table.Name, database)
		}
		if !table.Archived() {
			return catalogFailure(catalog.CodeValidation, "table", table.Name, "an archived Table")
		}
		now := service.clock.Now().UTC()
		table.ArchivedAt, table.ArchivedReason = nil, ""
		touchTable(table, now)
		touchDatabase(database, now)
		restored = *table
		return nil
	})
	return restored, err
}

// ArchiveColumn hides one Column while keeping its definition and every stored
// value. This is strictly better than DROP_COLUMN, which removes the Column and
// is therefore flagged Reversible=false: nothing is destroyed here, so the
// inverse is exact.
func (service *Service) ArchiveColumn(ctx context.Context, databaseName, tableName, columnName, reason string) (catalog.Column, error) {
	if err := required("column", columnName, "reason", reason); err != nil {
		return catalog.Column{}, err
	}
	var archived catalog.Column
	err := service.mutate(ctx, func(databases *[]catalog.Database) error {
		database, table, column, err := locateColumn(*databases, databaseName, tableName, columnName)
		if err != nil {
			return err
		}
		if database.Archived() {
			return archivedFailure("column", column.Name, database)
		}
		if table.Archived() {
			return catalogFailure(catalog.CodeArchived, "column", column.Name, "Table "+table.Name)
		}
		if column.Archived() {
			return catalogFailure(catalog.CodeAlreadyExists, "archived column", column.Name, "")
		}
		// A Table with no visible Column cannot be read or written at all.
		// Emptying one Column at a time would produce that state by accident;
		// archiving the Table is the operation that actually means it.
		if len(catalog.LiveColumns(table.Columns)) <= 1 {
			return catalogFailure(
				catalog.CodeValidation, "column", column.Name,
				"a Table that keeps at least one live Column (this is the last one; archive the Table instead)",
			)
		}
		now := service.clock.Now().UTC()
		column.ArchivedAt, column.ArchivedReason = &now, strings.TrimSpace(reason)
		column.SchemaVersion++
		column.UpdatedAt = now
		touchTable(table, now)
		touchDatabase(database, now)
		archived = *column
		return nil
	})
	return archived, err
}

func (service *Service) UnarchiveColumn(ctx context.Context, databaseName, tableName, columnName string) (catalog.Column, error) {
	var restored catalog.Column
	err := service.mutate(ctx, func(databases *[]catalog.Database) error {
		database, table, column, err := locateColumn(*databases, databaseName, tableName, columnName)
		if err != nil {
			return err
		}
		if database.Archived() {
			return archivedFailure("column", column.Name, database)
		}
		if table.Archived() {
			return catalogFailure(catalog.CodeArchived, "column", column.Name, "Table "+table.Name)
		}
		if !column.Archived() {
			return catalogFailure(catalog.CodeValidation, "column", column.Name, "an archived Column")
		}
		now := service.clock.Now().UTC()
		column.ArchivedAt, column.ArchivedReason = nil, ""
		column.SchemaVersion++
		column.UpdatedAt = now
		touchTable(table, now)
		touchDatabase(database, now)
		restored = *column
		return nil
	})
	return restored, err
}

// ShowArchivedColumns lists only the archived Columns of a live Table, so the
// archive view and UNARCHIVE have something to name.
func (service *Service) ShowArchivedColumns(ctx context.Context, databaseName, tableName string) ([]catalog.Column, error) {
	table, err := service.DescribeArchivedTable(ctx, databaseName, tableName)
	if err != nil {
		return nil, err
	}
	archived := make([]catalog.Column, 0, len(table.Columns))
	for _, column := range table.Columns {
		if column.Archived() {
			archived = append(archived, column)
		}
	}
	return archived, nil
}

func locateColumn(
	databases []catalog.Database,
	databaseName, tableName, columnName string,
) (*catalog.Database, *catalog.Table, *catalog.Column, error) {
	database, table, err := locateTable(databases, databaseName, tableName)
	if err != nil {
		return nil, nil, nil, err
	}
	column, ok := findColumn(table, columnName)
	if !ok {
		return nil, nil, nil, catalogFailure(catalog.CodeNotFound, "column", columnName, "")
	}
	return database, table, column, nil
}

// DescribeArchivedDatabase and DescribeArchivedTable are the archive-aware
// reads. Everything else goes through DescribeDatabase/DescribeTable, which
// refuse an archived object so that no ordinary read or write path has to
// remember the rule on its own.
func (service *Service) DescribeArchivedDatabase(ctx context.Context, name string) (catalog.Database, error) {
	databases, err := service.snapshot(ctx)
	if err != nil {
		return catalog.Database{}, err
	}
	database, ok := findDatabase(databases, name)
	if !ok {
		return catalog.Database{}, catalogFailure(catalog.CodeNotFound, "database", name, "")
	}
	return *database, nil
}

func (service *Service) DescribeArchivedTable(ctx context.Context, databaseName, tableName string) (catalog.Table, error) {
	databases, err := service.snapshot(ctx)
	if err != nil {
		return catalog.Table{}, err
	}
	_, table, err := locateTable(databases, databaseName, tableName)
	if err != nil {
		return catalog.Table{}, err
	}
	return *table, nil
}

// ShowArchivedDatabases lists only archived Databases, and ShowArchivedTables
// only archived Tables of a live Database. They exist so the Admin UI archive
// view and the CLI have a read surface that does not widen any other read.
func (service *Service) ShowArchivedDatabases(ctx context.Context) ([]catalog.Database, error) {
	databases, err := service.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	archived := make([]catalog.Database, 0, len(databases))
	for _, database := range databases {
		if database.Archived() {
			archived = append(archived, database)
		}
	}
	sortDatabasesByName(archived)
	return archived, nil
}

func (service *Service) ShowArchivedTables(ctx context.Context, databaseName string) ([]catalog.Table, error) {
	database, err := service.DescribeArchivedDatabase(ctx, databaseName)
	if err != nil {
		return nil, err
	}
	archived := make([]catalog.Table, 0, len(database.Tables))
	for _, table := range database.Tables {
		if table.Archived() {
			archived = append(archived, table)
		}
	}
	sortTablesByName(archived)
	return archived, nil
}

// archivedFailure names the ancestor that caused the object to be hidden. A
// user seeing "Row is archived" when they archived the Table needs to be told
// which level actually holds the archive.
func archivedFailure(object, name string, blocker *catalog.Database) error {
	if blocker != nil {
		return catalogFailure(catalog.CodeArchived, object, name, "Database "+blocker.Name)
	}
	return catalogFailure(catalog.CodeArchived, object, name, "")
}
