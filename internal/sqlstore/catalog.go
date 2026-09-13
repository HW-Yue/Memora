package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/change"
	"github.com/HW-Yue/Memora/internal/logical"
)

// Table roles. Only data tables are visible to the Catalog read surface; the
// other two are the companions every data table is born with.
const (
	roleData    = "data"
	roleHistory = "history"
	roleRoutes  = "routes"
)

func dataTable(tableID string) string    { return quoteIdent("data_" + tableID) }
func historyTable(tableID string) string { return quoteIdent("history_" + tableID) }
func routeTable(tableID string) string   { return quoteIdent("routes_" + tableID) }

func catalogError(code catalog.Code, object, name, field string) error {
	return &catalog.Error{Code: code, Object: object, Name: name, Field: field}
}

// ---- loading ----

func (t *tx) loadDatabases(ctx context.Context) ([]catalog.Database, error) {
	rows, err := t.q().QueryContext(ctx, `SELECT body FROM mem_databases`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	databases := []catalog.Database{}
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		var database catalog.Database
		if err := decodeJSON(body, &database); err != nil {
			return nil, err
		}
		database.Tables = []catalog.Table{}
		databases = append(databases, database)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	tables, err := t.loadTables(ctx, "")
	if err != nil {
		return nil, err
	}
	for index := range databases {
		for _, table := range tables {
			if table.DatabaseID == databases[index].ID {
				databases[index].Tables = append(databases[index].Tables, table)
			}
		}
		sortTables(databases[index].Tables)
	}
	sort.Slice(databases, func(left, right int) bool {
		return canonical(databases[left].Name) < canonical(databases[right].Name)
	})
	return databases, nil
}

func (t *tx) loadTables(ctx context.Context, databaseID string) ([]catalog.Table, error) {
	query := `SELECT body FROM mem_tables WHERE role = 'data'`
	arguments := []any{}
	if databaseID != "" {
		query += ` AND database_id = ?`
		arguments = append(arguments, databaseID)
	}
	rows, err := t.q().QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tables := []catalog.Table{}
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		var table catalog.Table
		if err := decodeJSON(body, &table); err != nil {
			return nil, err
		}
		if table.Columns == nil {
			table.Columns = []catalog.Column{}
		}
		tables = append(tables, table)
	}
	return tables, rows.Err()
}

func sortTables(tables []catalog.Table) {
	sort.Slice(tables, func(left, right int) bool { return canonical(tables[left].Name) < canonical(tables[right].Name) })
}

func matchesName(id, name string, aliases []string, wanted string) bool {
	wanted = canonical(wanted)
	if wanted == "" {
		return false
	}
	if canonical(id) == wanted || canonical(name) == wanted {
		return true
	}
	for _, alias := range aliases {
		if canonical(alias) == wanted {
			return true
		}
	}
	return false
}

func findDatabase(databases []catalog.Database, name string) (*catalog.Database, bool) {
	for index := range databases {
		if matchesName(databases[index].ID, databases[index].Name, databases[index].Aliases, name) {
			return &databases[index], true
		}
	}
	return nil, false
}

func findTable(database *catalog.Database, name string) (*catalog.Table, bool) {
	for index := range database.Tables {
		table := &database.Tables[index]
		if matchesName(table.ID, table.Name, table.Aliases, name) {
			return table, true
		}
	}
	return nil, false
}

func findColumn(table *catalog.Table, name string) (*catalog.Column, bool) {
	for index := range table.Columns {
		column := &table.Columns[index]
		if matchesName(column.ID, column.Name, column.Aliases, name) {
			return column, true
		}
	}
	return nil, false
}

// resolveDatabase finds a Database including archived ones.
func (t *tx) resolveDatabase(ctx context.Context, name string) (catalog.Database, error) {
	databases, err := t.loadDatabases(ctx)
	if err != nil {
		return catalog.Database{}, err
	}
	database, ok := findDatabase(databases, name)
	if !ok {
		return catalog.Database{}, catalogError(catalog.CodeNotFound, "database", name, "")
	}
	return *database, nil
}

// liveTable resolves a visible Table: neither it nor its Database is archived.
func (t *tx) liveTable(ctx context.Context, databaseName, tableName string) (catalog.Table, error) {
	database, err := t.resolveDatabase(ctx, databaseName)
	if err != nil {
		return catalog.Table{}, err
	}
	if database.Archived() {
		return catalog.Table{}, catalogError(catalog.CodeArchived, "table", tableName, "database "+database.Name)
	}
	table, ok := findTable(&database, tableName)
	if !ok {
		return catalog.Table{}, catalogError(catalog.CodeNotFound, "table", tableName, "")
	}
	if table.Archived() {
		return catalog.Table{}, catalogError(catalog.CodeArchived, "table", table.Name, "")
	}
	return *table, nil
}

func (t *tx) tableByID(ctx context.Context, tableID string) (catalog.Table, error) {
	var body string
	err := t.q().QueryRowContext(ctx, `SELECT body FROM mem_tables WHERE id = ? AND role = 'data'`, tableID).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return catalog.Table{}, catalogError(catalog.CodeNotFound, "table", tableID, "")
	}
	if err != nil {
		return catalog.Table{}, err
	}
	var table catalog.Table
	return table, decodeJSON(body, &table)
}

func (t *tx) databaseByID(ctx context.Context, databaseID string) (catalog.Database, error) {
	var body string
	err := t.q().QueryRowContext(ctx, `SELECT body FROM mem_databases WHERE id = ?`, databaseID).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return catalog.Database{}, catalogError(catalog.CodeNotFound, "database", databaseID, "")
	}
	if err != nil {
		return catalog.Database{}, err
	}
	var database catalog.Database
	return database, decodeJSON(body, &database)
}

// ---- saving ----

func (t *tx) saveDatabase(ctx context.Context, database catalog.Database) error {
	stored := database
	stored.Tables = nil
	_, err := t.q().ExecContext(ctx,
		`INSERT INTO mem_databases(id, name, body) VALUES (?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET name = excluded.name, body = excluded.body`,
		database.ID, database.Name, encodeJSON(stored))
	return err
}

func (t *tx) saveTable(ctx context.Context, table catalog.Table) error {
	table.ColumnSummaries = nil
	_, err := t.q().ExecContext(ctx,
		`INSERT INTO mem_tables(id, database_id, name, role, body) VALUES (?, ?, ?, 'data', ?)
		 ON CONFLICT(id) DO UPDATE SET name = excluded.name, body = excluded.body`,
		table.ID, table.DatabaseID, table.Name, encodeJSON(table))
	return err
}

func (t *tx) touchDatabase(ctx context.Context, databaseID string) error {
	database, err := t.databaseByID(ctx, databaseID)
	if err != nil {
		return err
	}
	database.SchemaVersion++
	database.UpdatedAt = t.now
	return t.saveDatabase(ctx, database)
}

func (t *tx) catalogChange(kind change.ObjectKind, operation change.Operation, databaseID, tableID, objectID string, before, after, schema uint64) {
	t.recordEntry(change.Entry{
		ObjectKind: kind, DatabaseID: databaseID, TableID: tableID, ObjectID: objectID,
		Operation: operation, BeforeRevision: before, AfterRevision: after, SchemaVersion: schema,
	}, change.Metadata{Actor: "system:catalog", Source: "msql", Reason: "Catalog mutation"})
}

// ---- CatalogService ----

func validateRequired(object, name, field, value string) error {
	if strings.TrimSpace(value) == "" {
		return catalogError(catalog.CodeValidation, object, name, field)
	}
	return nil
}

func (db *DB) CreateDatabase(ctx context.Context, definition catalog.DatabaseDefinition) (catalog.Database, error) {
	for _, field := range [][2]string{{"name", definition.Name}, {"purpose", definition.Purpose}, {"scope", definition.Scope}} {
		if err := validateRequired("database", definition.Name, field[0], field[1]); err != nil {
			return catalog.Database{}, err
		}
	}
	var created catalog.Database
	err := db.update(ctx, func(t *tx) error {
		databases, err := t.loadDatabases(ctx)
		if err != nil {
			return err
		}
		if _, exists := findDatabase(databases, definition.Name); exists {
			return catalogError(catalog.CodeAlreadyExists, "database", definition.Name, "")
		}
		created = catalog.Database{
			ID: newID("db_"), Name: strings.TrimSpace(definition.Name), Aliases: []string{},
			Purpose: definition.Purpose, Scope: definition.Scope, AntiScope: definition.AntiScope,
			SchemaVersion: 1, CreatedAt: t.now, UpdatedAt: t.now, Tables: []catalog.Table{},
		}
		t.catalogChange(change.ObjectDatabase, change.OperationInsert, created.ID, "", created.ID, 0, 1, 1)
		return t.saveDatabase(ctx, created)
	})
	return created, err
}

func (db *DB) ShowDatabases(ctx context.Context) ([]catalog.Database, error) {
	return db.showDatabases(ctx, false)
}

func (db *DB) ShowArchivedDatabases(ctx context.Context) ([]catalog.Database, error) {
	return db.showDatabases(ctx, true)
}

func (db *DB) showDatabases(ctx context.Context, includeArchived bool) ([]catalog.Database, error) {
	var databases []catalog.Database
	err := db.view(ctx, func(t *tx) error {
		all, err := t.loadDatabases(ctx)
		if err != nil {
			return err
		}
		databases = make([]catalog.Database, 0, len(all))
		for _, database := range all {
			if database.Archived() && !includeArchived {
				continue
			}
			if !includeArchived {
				database.Tables = liveTables(database.Tables)
			}
			databases = append(databases, database)
		}
		return nil
	})
	return databases, err
}

func liveTables(tables []catalog.Table) []catalog.Table {
	live := make([]catalog.Table, 0, len(tables))
	for _, table := range tables {
		if !table.Archived() {
			live = append(live, table)
		}
	}
	return live
}

func (db *DB) DescribeDatabase(ctx context.Context, name string) (catalog.Database, error) {
	var database catalog.Database
	err := db.view(ctx, func(t *tx) error {
		var err error
		database, err = t.resolveDatabase(ctx, name)
		if err != nil {
			return err
		}
		if database.Archived() {
			return catalogError(catalog.CodeArchived, "database", database.Name, "")
		}
		database.Tables = liveTables(database.Tables)
		return nil
	})
	return database, err
}

func (db *DB) DescribeArchivedDatabase(ctx context.Context, name string) (catalog.Database, error) {
	var database catalog.Database
	err := db.view(ctx, func(t *tx) error {
		var err error
		database, err = t.resolveDatabase(ctx, name)
		return err
	})
	return database, err
}

func (db *DB) RenameDatabase(ctx context.Context, name, newName string) (catalog.Database, error) {
	if err := validateRequired("database", newName, "name", newName); err != nil {
		return catalog.Database{}, err
	}
	var renamed catalog.Database
	err := db.update(ctx, func(t *tx) error {
		databases, err := t.loadDatabases(ctx)
		if err != nil {
			return err
		}
		database, ok := findDatabase(databases, name)
		if !ok {
			return catalogError(catalog.CodeNotFound, "database", name, "")
		}
		if other, exists := findDatabase(databases, newName); exists && other.ID != database.ID {
			return catalogError(catalog.CodeAlreadyExists, "database", newName, "")
		}
		database.Name = strings.TrimSpace(newName)
		database.SchemaVersion++
		database.UpdatedAt = t.now
		renamed = *database
		t.catalogChange(change.ObjectDatabase, change.OperationUpdate, database.ID, "", database.ID,
			database.SchemaVersion-1, database.SchemaVersion, database.SchemaVersion)
		return t.saveDatabase(ctx, *database)
	})
	return renamed, err
}

func validateColumnDefinition(definition catalog.ColumnDefinition) error {
	for _, field := range [][2]string{{"name", definition.Name}, {"type", definition.Type}, {"purpose", definition.Purpose}} {
		if err := validateRequired("column", definition.Name, field[0], field[1]); err != nil {
			return err
		}
	}
	switch canonical(definition.SemanticRole) {
	case "", "title", "summary", "identity", "status", "fact", "rationale":
	default:
		return catalogError(catalog.CodeValidation, "column", definition.Name, "a supported semantic role")
	}
	_, err := logical.ParseDeclaration(definition.Type)
	return err
}

func newColumn(definition catalog.ColumnDefinition, now time.Time) (catalog.Column, error) {
	parsed, err := logical.ParseDeclaration(definition.Type)
	if err != nil {
		return catalog.Column{}, err
	}
	return catalog.Column{
		ID: newID("col_"), Name: strings.TrimSpace(definition.Name), Aliases: []string{},
		Type: string(parsed.Kind), MaxCharacters: parsed.MaxCharacters, Nullable: definition.Nullable,
		Purpose: definition.Purpose, SemanticRole: canonical(definition.SemanticRole),
		SchemaVersion: 1, CreatedAt: now, UpdatedAt: now,
	}, nil
}

func (db *DB) CreateTable(ctx context.Context, databaseName string, definition catalog.TableDefinition) (catalog.Table, error) {
	for _, field := range [][2]string{{"name", definition.Name}, {"purpose", definition.Purpose}, {"row semantics", definition.RowSemantics}} {
		if err := validateRequired("table", definition.Name, field[0], field[1]); err != nil {
			return catalog.Table{}, err
		}
	}
	seen := map[string]bool{}
	roles := map[string]bool{}
	for _, column := range definition.Columns {
		if err := validateColumnDefinition(column); err != nil {
			return catalog.Table{}, err
		}
		if seen[canonical(column.Name)] {
			return catalog.Table{}, catalogError(catalog.CodeAlreadyExists, "column", column.Name, "")
		}
		seen[canonical(column.Name)] = true
		if role := canonical(column.SemanticRole); role == "title" || role == "summary" {
			if roles[role] {
				return catalog.Table{}, catalogError(catalog.CodeValidation, "column", column.Name, "unique "+role+" role")
			}
			roles[role] = true
		}
	}
	var created catalog.Table
	err := db.update(ctx, func(t *tx) error {
		database, err := t.resolveDatabase(ctx, databaseName)
		if err != nil {
			return err
		}
		if database.Archived() {
			return catalogError(catalog.CodeArchived, "database", database.Name, "")
		}
		if _, exists := findTable(&database, definition.Name); exists {
			return catalogError(catalog.CodeAlreadyExists, "table", definition.Name, "")
		}
		created = catalog.Table{
			ID: newID("tbl_"), DatabaseID: database.ID, Name: strings.TrimSpace(definition.Name), Aliases: []string{},
			Purpose: definition.Purpose, Scope: definition.Scope, AntiScope: definition.AntiScope,
			RowSemantics: definition.RowSemantics, SchemaVersion: 1, CreatedAt: t.now, UpdatedAt: t.now,
			Columns: []catalog.Column{},
		}
		for _, columnDefinition := range definition.Columns {
			column, err := newColumn(columnDefinition, t.now)
			if err != nil {
				return err
			}
			created.Columns = append(created.Columns, column)
		}
		if err := t.saveTable(ctx, created); err != nil {
			return err
		}
		if err := t.createCompanions(ctx, created); err != nil {
			return err
		}
		t.catalogChange(change.ObjectTable, change.OperationInsert, database.ID, created.ID, created.ID, 0, 1, 1)
		return t.touchDatabase(ctx, database.ID)
	})
	return created, err
}

// createCompanions creates the physical data table and its two companions, and
// registers the companions in the Catalog with their role so they are never
// mistaken for data tables.
func (t *tx) createCompanions(ctx context.Context, table catalog.Table) error {
	statements := []string{
		fmt.Sprintf(`CREATE TABLE %s (
			row_id TEXT PRIMARY KEY,
			revision INTEGER NOT NULL,
			schema_version INTEGER NOT NULL,
			commit_sequence INTEGER NOT NULL,
			row_state TEXT NOT NULL,
			values_json TEXT NOT NULL,
			route_leaf_ids TEXT NOT NULL DEFAULT '[]',
			links TEXT NOT NULL DEFAULT '[]',
			successor_ids TEXT NOT NULL DEFAULT '[]',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			ordinal INTEGER NOT NULL
		)`, dataTable(table.ID)),
		fmt.Sprintf(`CREATE INDEX %s ON %s(ordinal)`, quoteIdent("data_"+table.ID+"_ordinal"), dataTable(table.ID)),
		fmt.Sprintf(`CREATE TABLE %s (
			row_id TEXT NOT NULL,
			revision INTEGER NOT NULL,
			commit_sequence INTEGER NOT NULL,
			body TEXT NOT NULL,
			PRIMARY KEY(row_id, revision)
		)`, historyTable(table.ID)),
		fmt.Sprintf(`CREATE TABLE %s (
			route_id TEXT PRIMARY KEY,
			parent_id TEXT,
			kind TEXT NOT NULL,
			deprecated INTEGER NOT NULL DEFAULT 0,
			body TEXT NOT NULL
		)`, routeTable(table.ID)),
	}
	for _, statement := range statements {
		if _, err := t.q().ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("create physical tables for %q: %w", table.Name, err)
		}
	}
	for _, companion := range []struct{ role, prefix string }{{roleHistory, "_memora_history_"}, {roleRoutes, "_memora_routes_"}} {
		if _, err := t.q().ExecContext(ctx,
			`INSERT INTO mem_tables(id, database_id, name, role, owner_table_id, body) VALUES (?, ?, ?, ?, ?, '{}')`,
			companion.role+"_"+table.ID, table.DatabaseID, companion.prefix+table.ID, companion.role, table.ID); err != nil {
			return err
		}
	}
	return nil
}

func (db *DB) ShowTables(ctx context.Context, databaseName string) ([]catalog.Table, error) {
	database, err := db.DescribeDatabase(ctx, databaseName)
	if err != nil {
		return nil, err
	}
	return database.Tables, nil
}

func (db *DB) ShowArchivedTables(ctx context.Context, databaseName string) ([]catalog.Table, error) {
	database, err := db.DescribeArchivedDatabase(ctx, databaseName)
	if err != nil {
		return nil, err
	}
	return database.Tables, nil
}

func (db *DB) DescribeTable(ctx context.Context, databaseName, tableName string) (catalog.Table, error) {
	var table catalog.Table
	err := db.view(ctx, func(t *tx) error {
		var err error
		table, err = t.liveTable(ctx, databaseName, tableName)
		return err
	})
	return table, err
}

func (db *DB) DescribeArchivedTable(ctx context.Context, databaseName, tableName string) (catalog.Table, error) {
	var table catalog.Table
	err := db.view(ctx, func(t *tx) error {
		database, err := t.resolveDatabase(ctx, databaseName)
		if err != nil {
			return err
		}
		found, ok := findTable(&database, tableName)
		if !ok {
			return catalogError(catalog.CodeNotFound, "table", tableName, "")
		}
		table = *found
		return nil
	})
	return table, err
}

// mutateTable loads a live Table, lets fn change it, bumps its schema version,
// and saves it together with its Database.
func (db *DB) mutateTable(ctx context.Context, databaseName, tableName string, allowArchived bool, fn func(*tx, *catalog.Database, *catalog.Table) error) (catalog.Table, error) {
	var updated catalog.Table
	err := db.update(ctx, func(t *tx) error {
		database, err := t.resolveDatabase(ctx, databaseName)
		if err != nil {
			return err
		}
		if database.Archived() {
			return catalogError(catalog.CodeArchived, "table", tableName, "database "+database.Name)
		}
		table, ok := findTable(&database, tableName)
		if !ok {
			return catalogError(catalog.CodeNotFound, "table", tableName, "")
		}
		if table.Archived() && !allowArchived {
			return catalogError(catalog.CodeArchived, "table", table.Name, "")
		}
		before := table.SchemaVersion
		if err := fn(t, &database, table); err != nil {
			return err
		}
		table.SchemaVersion++
		table.UpdatedAt = t.now
		updated = *table
		if err := t.saveTable(ctx, *table); err != nil {
			return err
		}
		t.catalogChange(change.ObjectTable, change.OperationUpdate, database.ID, table.ID, table.ID, before, table.SchemaVersion, table.SchemaVersion)
		return t.touchDatabase(ctx, database.ID)
	})
	return updated, err
}

func (db *DB) RenameTable(ctx context.Context, databaseName, tableName, newName string) (catalog.Table, error) {
	if err := validateRequired("table", newName, "name", newName); err != nil {
		return catalog.Table{}, err
	}
	return db.mutateTable(ctx, databaseName, tableName, false, func(_ *tx, database *catalog.Database, table *catalog.Table) error {
		if other, exists := findTable(database, newName); exists && other.ID != table.ID {
			return catalogError(catalog.CodeAlreadyExists, "table", newName, "")
		}
		table.Name = strings.TrimSpace(newName)
		return nil
	})
}

func (db *DB) AddColumn(ctx context.Context, databaseName, tableName string, definition catalog.ColumnDefinition) (catalog.Column, error) {
	if err := validateColumnDefinition(definition); err != nil {
		return catalog.Column{}, err
	}
	var created catalog.Column
	_, err := db.mutateTable(ctx, databaseName, tableName, false, func(t *tx, _ *catalog.Database, table *catalog.Table) error {
		if _, exists := findColumn(table, definition.Name); exists {
			return catalogError(catalog.CodeAlreadyExists, "column", definition.Name, "")
		}
		if role := canonical(definition.SemanticRole); role == "title" || role == "summary" {
			for _, existing := range table.Columns {
				if canonical(existing.SemanticRole) == role && !existing.Archived() {
					return catalogError(catalog.CodeValidation, "column", definition.Name, "unique "+role+" role")
				}
			}
		}
		if !definition.Nullable {
			var count int
			if err := t.q().QueryRowContext(ctx, `SELECT COUNT(*) FROM `+dataTable(table.ID)).Scan(&count); err != nil {
				return err
			}
			if count > 0 {
				return catalogError(catalog.CodeValidation, "column", definition.Name, "NULL allowed because the table already has rows")
			}
		}
		column, err := newColumn(definition, t.now)
		if err != nil {
			return err
		}
		table.Columns = append(table.Columns, column)
		created = column
		return nil
	})
	return created, err
}

func (db *DB) ShowColumns(ctx context.Context, databaseName, tableName string) ([]catalog.Column, error) {
	table, err := db.DescribeTable(ctx, databaseName, tableName)
	if err != nil {
		return nil, err
	}
	return catalog.LiveColumns(table.Columns), nil
}

func (db *DB) ShowArchivedColumns(ctx context.Context, databaseName, tableName string) ([]catalog.Column, error) {
	table, err := db.DescribeArchivedTable(ctx, databaseName, tableName)
	if err != nil {
		return nil, err
	}
	return table.Columns, nil
}

func (db *DB) DescribeColumn(ctx context.Context, databaseName, tableName, columnName string) (catalog.Column, error) {
	table, err := db.DescribeTable(ctx, databaseName, tableName)
	if err != nil {
		return catalog.Column{}, err
	}
	column, ok := findColumn(&table, columnName)
	if !ok {
		return catalog.Column{}, catalogError(catalog.CodeNotFound, "column", columnName, "")
	}
	if column.Archived() {
		return catalog.Column{}, catalogError(catalog.CodeArchived, "column", column.Name, "")
	}
	return *column, nil
}

func (db *DB) RenameColumn(ctx context.Context, databaseName, tableName, columnName, newName string) (catalog.Column, error) {
	if err := validateRequired("column", newName, "name", newName); err != nil {
		return catalog.Column{}, err
	}
	var renamed catalog.Column
	_, err := db.mutateTable(ctx, databaseName, tableName, false, func(t *tx, _ *catalog.Database, table *catalog.Table) error {
		column, ok := findColumn(table, columnName)
		if !ok {
			return catalogError(catalog.CodeNotFound, "column", columnName, "")
		}
		if other, exists := findColumn(table, newName); exists && other.ID != column.ID {
			return catalogError(catalog.CodeAlreadyExists, "column", newName, "")
		}
		column.Name = strings.TrimSpace(newName)
		column.SchemaVersion++
		column.UpdatedAt = t.now
		renamed = *column
		return nil
	})
	return renamed, err
}

// ---- archive ----

func (db *DB) ArchiveDatabase(ctx context.Context, name, reason string) (catalog.Database, error) {
	return db.setDatabaseArchived(ctx, name, reason, true)
}

func (db *DB) UnarchiveDatabase(ctx context.Context, name string) (catalog.Database, error) {
	return db.setDatabaseArchived(ctx, name, "", false)
}

func (db *DB) setDatabaseArchived(ctx context.Context, name, reason string, archived bool) (catalog.Database, error) {
	var updated catalog.Database
	err := db.update(ctx, func(t *tx) error {
		database, err := t.resolveDatabase(ctx, name)
		if err != nil {
			return err
		}
		if database.Archived() == archived {
			updated = database
			return nil
		}
		if archived {
			now := t.now
			database.ArchivedAt, database.ArchivedReason = &now, reason
		} else {
			database.ArchivedAt, database.ArchivedReason = nil, ""
		}
		database.SchemaVersion++
		database.UpdatedAt = t.now
		updated = database
		t.catalogChange(change.ObjectDatabase, change.OperationUpdate, database.ID, "", database.ID,
			database.SchemaVersion-1, database.SchemaVersion, database.SchemaVersion)
		return t.saveDatabase(ctx, database)
	})
	return updated, err
}

func (db *DB) ArchiveTable(ctx context.Context, databaseName, tableName, reason string) (catalog.Table, error) {
	return db.mutateTable(ctx, databaseName, tableName, true, func(t *tx, _ *catalog.Database, table *catalog.Table) error {
		if table.Archived() {
			return nil
		}
		now := t.now
		table.ArchivedAt, table.ArchivedReason = &now, reason
		return nil
	})
}

func (db *DB) UnarchiveTable(ctx context.Context, databaseName, tableName string) (catalog.Table, error) {
	return db.mutateTable(ctx, databaseName, tableName, true, func(_ *tx, _ *catalog.Database, table *catalog.Table) error {
		table.ArchivedAt, table.ArchivedReason = nil, ""
		return nil
	})
}

func (db *DB) ArchiveColumn(ctx context.Context, databaseName, tableName, columnName, reason string) (catalog.Column, error) {
	return db.setColumnArchived(ctx, databaseName, tableName, columnName, reason, true)
}

func (db *DB) UnarchiveColumn(ctx context.Context, databaseName, tableName, columnName string) (catalog.Column, error) {
	return db.setColumnArchived(ctx, databaseName, tableName, columnName, "", false)
}

func (db *DB) setColumnArchived(ctx context.Context, databaseName, tableName, columnName, reason string, archived bool) (catalog.Column, error) {
	var updated catalog.Column
	_, err := db.mutateTable(ctx, databaseName, tableName, false, func(t *tx, _ *catalog.Database, table *catalog.Table) error {
		column, ok := findColumn(table, columnName)
		if !ok {
			return catalogError(catalog.CodeNotFound, "column", columnName, "")
		}
		if archived {
			if !column.Nullable {
				return catalogError(catalog.CodeValidation, "column", column.Name, "nullable before it can be archived")
			}
			now := t.now
			column.ArchivedAt, column.ArchivedReason = &now, reason
		} else {
			column.ArchivedAt, column.ArchivedReason = nil, ""
		}
		column.SchemaVersion++
		column.UpdatedAt = t.now
		updated = *column
		return nil
	})
	return updated, err
}
