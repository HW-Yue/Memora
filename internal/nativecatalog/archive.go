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
