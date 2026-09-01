package nativecatalog

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/change"
	"github.com/HW-Yue/Memora/internal/logical"
	"github.com/HW-Yue/Memora/internal/nativechange"
	"github.com/google/uuid"
)

type IDSource interface{ Next() (string, error) }
type Clock interface{ Now() time.Time }

// PageAuthority owns Catalog visibility after F107. The commit callback writes
// immutable bodies; PublishCatalog must not report success until the Page tree
// exposes the supplied snapshot.
type PageAuthority interface {
	BeginWrite(context.Context) (func(), error)
	NextChangeSequence(context.Context) (uint64, error)
	SnapshotCatalog(context.Context) ([]catalog.Database, error)
	ShowDatabases(context.Context) ([]catalog.Database, error)
	DescribeDatabase(context.Context, string) (catalog.Database, error)
	ShowTables(context.Context, string) ([]catalog.Table, error)
	DescribeTable(context.Context, string, string) (catalog.Table, error)
	PublishCatalog(context.Context, []catalog.Database, func() error) error
}

type ServiceOptions struct {
	IDs       IDSource
	Clock     Clock
	Authority PageAuthority
}

type Service struct {
	repository *Repository
	ids        IDSource
	clock      Clock
	authority  PageAuthority
	mu         sync.Mutex
}

func NewService(repository *Repository, options ServiceOptions) *Service {
	if options.IDs == nil {
		options.IDs = uuidSource{}
	}
	if options.Clock == nil {
		options.Clock = systemClock{}
	}
	return &Service{repository: repository, ids: options.IDs, clock: options.Clock, authority: options.Authority}
}

func (service *Service) CreateDatabase(ctx context.Context, definition catalog.DatabaseDefinition) (catalog.Database, error) {
	if err := required("database", definition.Name, "name", definition.Name); err != nil {
		return catalog.Database{}, err
	}
	if err := required("database", definition.Name, "purpose", definition.Purpose); err != nil {
		return catalog.Database{}, err
	}
	if err := required("database", definition.Name, "scope", definition.Scope); err != nil {
		return catalog.Database{}, err
	}
	var created catalog.Database
	err := service.mutate(ctx, func(databases *[]catalog.Database) error {
		if _, ok := findDatabase(*databases, definition.Name); ok {
			return catalogFailure(catalog.CodeAlreadyExists, "database", definition.Name, "")
		}
		id, err := service.nextID("db")
		if err != nil {
			return err
		}
		now := service.clock.Now().UTC()
		created = catalog.Database{ID: id, Name: definition.Name, Aliases: []string{}, Purpose: definition.Purpose, Scope: definition.Scope, AntiScope: definition.AntiScope, SchemaVersion: 1, CreatedAt: now, UpdatedAt: now, Tables: []catalog.Table{}}
		*databases = append(*databases, created)
		return nil
	})
	return created, err
}

// ShowDatabases and every other method below are the live read surface: an
// archived object never appears and never resolves. Use the Describe/Show
// Archived variants in archive.go when the caller means to see them.
func (service *Service) ShowDatabases(ctx context.Context) ([]catalog.Database, error) {
	databases, err := service.showDatabasesIncludingArchived(ctx)
	if err != nil {
		return nil, err
	}
	live := make([]catalog.Database, 0, len(databases))
	for _, database := range databases {
		if database.Archived() {
			continue
		}
		database.Tables = liveTables(database.Tables)
		live = append(live, database)
	}
	sortDatabasesByName(live)
	return live, nil
}

func (service *Service) showDatabasesIncludingArchived(ctx context.Context) ([]catalog.Database, error) {
	if service.authority != nil {
		return service.authority.ShowDatabases(ctx)
	}
	return service.read(ctx)
}

// snapshot is the archive-aware whole-Catalog read used by the archive
// surface; it deliberately does not filter.
func (service *Service) snapshot(ctx context.Context) ([]catalog.Database, error) {
	return service.showDatabasesIncludingArchived(ctx)
}

func (service *Service) DescribeDatabase(ctx context.Context, name string) (catalog.Database, error) {
	database, err := service.describeDatabaseIncludingArchived(ctx, name)
	if err != nil {
		return catalog.Database{}, err
	}
	if database.Archived() {
		return catalog.Database{}, archivedFailure("database", database.Name, nil)
	}
	database.Tables = liveTables(database.Tables)
	return database, nil
}

func (service *Service) describeDatabaseIncludingArchived(ctx context.Context, name string) (catalog.Database, error) {
	if service.authority != nil {
		return service.authority.DescribeDatabase(ctx, name)
	}
	databases, err := service.read(ctx)
	if err != nil {
		return catalog.Database{}, err
	}
	database, ok := findDatabase(databases, name)
	if !ok {
		return catalog.Database{}, catalogFailure(catalog.CodeNotFound, "database", name, "")
	}
	return *database, nil
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

func sortDatabasesByName(databases []catalog.Database) {
	sort.Slice(databases, func(left, right int) bool {
		return canonical(databases[left].Name) < canonical(databases[right].Name)
	})
}

func sortTablesByName(tables []catalog.Table) {
	sort.Slice(tables, func(left, right int) bool {
		return canonical(tables[left].Name) < canonical(tables[right].Name)
	})
}

func (service *Service) CreateTable(ctx context.Context, databaseName string, definition catalog.TableDefinition) (catalog.Table, error) {
	if err := validateTableDefinition(definition); err != nil {
		return catalog.Table{}, err
	}
	var created catalog.Table
	err := service.mutate(ctx, func(databases *[]catalog.Database) error {
		database, ok := findDatabase(*databases, databaseName)
		if !ok {
			return catalogFailure(catalog.CodeNotFound, "database", databaseName, "")
		}
		if _, ok := findTable(database, definition.Name); ok {
			return catalogFailure(catalog.CodeAlreadyExists, "table", definition.Name, "")
		}
		now := service.clock.Now().UTC()
		id, err := service.nextID("tbl")
		if err != nil {
			return err
		}
		created = catalog.Table{ID: id, DatabaseID: database.ID, Name: definition.Name, Aliases: []string{}, Purpose: definition.Purpose, Scope: definition.Scope, AntiScope: definition.AntiScope, RowSemantics: definition.RowSemantics, SchemaVersion: 1, CreatedAt: now, UpdatedAt: now, Columns: []catalog.Column{}}
		for _, definition := range definition.Columns {
			column, err := service.newColumn(definition, now)
			if err != nil {
				return err
			}
			created.Columns = append(created.Columns, column)
		}
		database.Tables = append(database.Tables, created)
		touchDatabase(database, now)
		return nil
	})
	return created, err
}

func (service *Service) ShowTables(ctx context.Context, databaseName string) ([]catalog.Table, error) {
	// DescribeDatabase already refuses an archived Database and drops archived
	// Tables, so the visibility rule lives in exactly one place.
	if _, err := service.DescribeDatabase(ctx, databaseName); err != nil {
		return nil, err
	}
	var tables []catalog.Table
	if service.authority != nil {
		fromAuthority, err := service.authority.ShowTables(ctx, databaseName)
		if err != nil {
			return nil, err
		}
		tables = liveTables(fromAuthority)
	} else {
		database, err := service.DescribeDatabase(ctx, databaseName)
		if err != nil {
			return nil, err
		}
		tables = append([]catalog.Table(nil), database.Tables...)
	}
	sortTablesByName(tables)
	return tables, nil
}

func (service *Service) DescribeTable(ctx context.Context, databaseName, tableName string) (catalog.Table, error) {
	database, err := service.describeDatabaseIncludingArchived(ctx, databaseName)
	if err != nil {
		return catalog.Table{}, err
	}
	if database.Archived() {
		return catalog.Table{}, archivedFailure("table", tableName, &database)
	}
	table, err := service.describeTableIncludingArchived(ctx, databaseName, tableName, database)
	if err != nil {
		return catalog.Table{}, err
	}
	if table.Archived() {
		return catalog.Table{}, archivedFailure("table", table.Name, nil)
	}
	return table, nil
}

func (service *Service) describeTableIncludingArchived(
	ctx context.Context,
	databaseName, tableName string,
	database catalog.Database,
) (catalog.Table, error) {
	if service.authority != nil {
		return service.authority.DescribeTable(ctx, databaseName, tableName)
	}
	table, ok := findTable(&database, tableName)
	if !ok {
		return catalog.Table{}, catalogFailure(catalog.CodeNotFound, "table", tableName, "")
	}
	return *table, nil
}

func (service *Service) read(ctx context.Context) ([]catalog.Database, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.authority != nil {
		return service.authority.SnapshotCatalog(ctx)
	}
	databases, err := service.repository.Read()
	if err != nil {
		return nil, err
	}
	for databaseIndex := range databases {
		database := &databases[databaseIndex]
		if database.Aliases == nil {
			database.Aliases = []string{}
		}
		for tableIndex := range database.Tables {
			table := &database.Tables[tableIndex]
			if table.Aliases == nil {
				table.Aliases = []string{}
			}
			for columnIndex := range table.Columns {
				if table.Columns[columnIndex].Aliases == nil {
					table.Columns[columnIndex].Aliases = []string{}
				}
			}
		}
	}
	return databases, nil
}

func (service *Service) mutate(ctx context.Context, mutation func(*[]catalog.Database) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.authority != nil {
		release, err := service.authority.BeginWrite(ctx)
		if err != nil {
			return err
		}
		defer release()
	}
	databases, err := service.currentCatalog(ctx)
	if err != nil {
		return err
	}
	before := catalogRevisionMap(databases)
	// Kept whole, not just as revisions: the write stages only what moved, and
	// deciding that needs the bytes each object was last published with.
	previous := cloneCatalog(databases)
	if err := mutation(&databases); err != nil {
		return err
	}
	sequence, err := service.nextChangeSequence(ctx)
	if err != nil {
		return err
	}
	envelope, err := changeEnvelope(sequence, service.clock.Now().UTC(), before, databases, change.Metadata{
		Actor: "system:direct-api", Source: "direct-api", Reason: "Catalog mutation",
	})
	if err != nil {
		return err
	}
	if service.authority != nil {
		return service.authority.PublishCatalog(ctx, databases, func() error {
			return service.repository.WriteCommitted(previous, databases, envelope)
		})
	}
	return service.repository.WriteCommitted(previous, databases, envelope)
}

// currentCatalog reads the Catalog the write is about to move on from.
//
// Through the Authority when there is one, which reads it out of the Catalog
// Tree and the objects Tree — two B+Tree walks, nothing touching the record
// log. The record-log rebuild stays for a Database with no generation, which is
// the only case with no Tree to read.
func (service *Service) currentCatalog(ctx context.Context) ([]catalog.Database, error) {
	if service.authority != nil {
		return service.authority.SnapshotCatalog(ctx)
	}
	return service.repository.Read()
}

// cloneCatalog copies deeply enough that a mutation of the copy cannot reach
// back into the original: the slices are what mutations append to.
func cloneCatalog(databases []catalog.Database) []catalog.Database {
	result := make([]catalog.Database, 0, len(databases))
	for _, database := range databases {
		next := database
		next.Aliases = append([]string(nil), database.Aliases...)
		next.Tables = make([]catalog.Table, 0, len(database.Tables))
		for _, table := range database.Tables {
			copied := table
			copied.Aliases = append([]string(nil), table.Aliases...)
			copied.Columns = make([]catalog.Column, 0, len(table.Columns))
			for _, column := range table.Columns {
				value := column
				value.Aliases = append([]string(nil), column.Aliases...)
				copied.Columns = append(copied.Columns, value)
			}
			next.Tables = append(next.Tables, copied)
		}
		result = append(result, next)
	}
	return result
}

type catalogRevision struct {
	kind       change.ObjectKind
	databaseID string
	tableID    string
	revision   uint64
}

func catalogRevisionMap(databases []catalog.Database) map[string]catalogRevision {
	result := make(map[string]catalogRevision)
	for _, database := range databases {
		result[string(change.ObjectDatabase)+"\x00"+database.ID] = catalogRevision{
			kind: change.ObjectDatabase, databaseID: database.ID, revision: database.SchemaVersion,
		}
		for _, table := range database.Tables {
			result[string(change.ObjectTable)+"\x00"+table.ID] = catalogRevision{
				kind: change.ObjectTable, databaseID: database.ID, tableID: table.ID,
				revision: table.SchemaVersion,
			}
			for _, column := range table.Columns {
				result[string(change.ObjectColumn)+"\x00"+column.ID] = catalogRevision{
					kind: change.ObjectColumn, databaseID: database.ID, tableID: table.ID,
					revision: column.SchemaVersion,
				}
			}
		}
	}
	return result
}

func changeEnvelope(
	sequence uint64,
	committedAt time.Time,
	before map[string]catalogRevision,
	databases []catalog.Database,
	metadata change.Metadata,
) (change.Envelope, error) {
	after := catalogRevisionMap(databases)
	entries := make([]change.Entry, 0)
	for key, current := range after {
		previous, existed := before[key]
		if existed && previous.revision == current.revision {
			continue
		}
		operation := change.OperationInsert
		beforeRevision := uint64(0)
		if existed {
			operation = change.OperationUpdate
			beforeRevision = previous.revision
		}
		objectID := strings.SplitN(key, "\x00", 2)[1]
		entries = append(entries, change.Entry{
			ObjectKind: current.kind, DatabaseID: current.databaseID, TableID: current.tableID,
			ObjectID: objectID, Operation: operation, BeforeRevision: beforeRevision,
			AfterRevision: current.revision, SchemaVersion: current.revision,
		})
	}
	for key, previous := range before {
		if _, exists := after[key]; exists {
			continue
		}
		objectID := strings.SplitN(key, "\x00", 2)[1]
		entries = append(entries, change.Entry{
			ObjectKind: previous.kind, DatabaseID: previous.databaseID, TableID: previous.tableID,
			ObjectID: objectID, Operation: change.OperationDelete, BeforeRevision: previous.revision,
			AfterRevision: previous.revision + 1, SchemaVersion: previous.revision + 1,
		})
	}
	return change.NewEnvelope(sequence, committedAt, metadata, entries)
}

func (service *Service) nextChangeSequence(ctx context.Context) (uint64, error) {
	if service.authority != nil {
		return service.authority.NextChangeSequence(ctx)
	}
	return nativechange.New(service.repository.file).NextSequence(0)
}

func (service *Service) newColumn(definition catalog.ColumnDefinition, now time.Time) (catalog.Column, error) {
	parsed, err := logical.ParseDeclaration(definition.Type)
	if err != nil {
		return catalog.Column{}, err
	}
	id, err := service.nextID("col")
	if err != nil {
		return catalog.Column{}, err
	}
	return catalog.Column{ID: id, Name: definition.Name, Aliases: []string{}, Type: string(parsed.Kind), MaxCharacters: parsed.MaxCharacters, Nullable: definition.Nullable, Purpose: definition.Purpose, SemanticRole: normalizeSemanticRole(definition.SemanticRole), SchemaVersion: 1, CreatedAt: now, UpdatedAt: now}, nil
}

func (service *Service) nextID(prefix string) (string, error) {
	id, err := service.ids.Next()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(id) == "" {
		return "", catalogFailure(catalog.CodeValidation, prefix, "", "non-empty generated ID")
	}
	return prefix + "_" + id, nil
}

func validateTableDefinition(definition catalog.TableDefinition) error {
	if err := required("table", definition.Name, "name", definition.Name); err != nil {
		return err
	}
	if err := required("table", definition.Name, "purpose", definition.Purpose); err != nil {
		return err
	}
	if err := required("table", definition.Name, "row semantics", definition.RowSemantics); err != nil {
		return err
	}
	seen := map[string]struct{}{}
	displayRoles := map[string]string{}
	for _, column := range definition.Columns {
		if err := validateColumnDefinition(column); err != nil {
			return err
		}
		key := canonical(column.Name)
		if _, ok := seen[key]; ok {
			return catalogFailure(catalog.CodeAlreadyExists, "column", column.Name, "")
		}
		seen[key] = struct{}{}
		role := normalizeSemanticRole(column.SemanticRole)
		if role == "title" || role == "summary" {
			if first, ok := displayRoles[role]; ok {
				return catalogFailure(catalog.CodeValidation, "column", column.Name, "unique "+role+" role; already used by "+first)
			}
			displayRoles[role] = column.Name
		}
	}
	return nil
}

func validateColumnDefinition(definition catalog.ColumnDefinition) error {
	if err := required("column", definition.Name, "name", definition.Name); err != nil {
		return err
	}
	if err := required("column", definition.Name, "type", definition.Type); err != nil {
		return err
	}
	if err := required("column", definition.Name, "purpose", definition.Purpose); err != nil {
		return err
	}
	if !validSemanticRole(definition.SemanticRole) {
		return catalogFailure(catalog.CodeValidation, "column", definition.Name, "a supported semantic role")
	}
	_, err := logical.ParseDeclaration(definition.Type)
	return err
}

func normalizeSemanticRole(value string) string { return canonical(value) }

func validSemanticRole(value string) bool {
	switch normalizeSemanticRole(value) {
	case "", "title", "summary", "identity", "status", "fact", "rationale":
		return true
	default:
		return false
	}
}

func locateTable(databases []catalog.Database, databaseName, tableName string) (*catalog.Database, *catalog.Table, error) {
	database, ok := findDatabase(databases, databaseName)
	if !ok {
		return nil, nil, catalogFailure(catalog.CodeNotFound, "database", databaseName, "")
	}
	table, ok := findTable(database, tableName)
	if !ok {
		return nil, nil, catalogFailure(catalog.CodeNotFound, "table", tableName, "")
	}
	return database, table, nil
}

func findDatabase(databases []catalog.Database, name string) (*catalog.Database, bool) {
	for index := range databases {
		if matches(databases[index].ID, nil, name) || matches(databases[index].Name, databases[index].Aliases, name) {
			return &databases[index], true
		}
	}
	return nil, false
}

func findTable(database *catalog.Database, name string) (*catalog.Table, bool) {
	for index := range database.Tables {
		if matches(database.Tables[index].ID, nil, name) || matches(database.Tables[index].Name, database.Tables[index].Aliases, name) {
			return &database.Tables[index], true
		}
	}
	return nil, false
}

func findColumn(table *catalog.Table, name string) (*catalog.Column, bool) {
	for index := range table.Columns {
		if matches(table.Columns[index].ID, nil, name) || matches(table.Columns[index].Name, table.Columns[index].Aliases, name) {
			return &table.Columns[index], true
		}
	}
	return nil, false
}

func matches(name string, aliases []string, candidate string) bool {
	if canonical(name) == canonical(candidate) {
		return true
	}
	for _, alias := range aliases {
		if canonical(alias) == canonical(candidate) {
			return true
		}
	}
	return false
}

func addAlias(aliases []string, name string) []string {
	if matches(name, aliases, name) && canonical(name) != "" {
		for _, alias := range aliases {
			if canonical(alias) == canonical(name) {
				return aliases
			}
		}
	}
	return append(aliases, name)
}

func touchDatabase(database *catalog.Database, now time.Time) {
	database.SchemaVersion++
	database.UpdatedAt = now.UTC()
}
func touchTable(table *catalog.Table, now time.Time) {
	table.SchemaVersion++
	table.UpdatedAt = now.UTC()
}
func canonical(value string) string { return strings.ToLower(strings.TrimSpace(value)) }
func required(object, name, field, value string) error {
	if strings.TrimSpace(value) == "" {
		return catalogFailure(catalog.CodeValidation, object, name, field)
	}
	return nil
}
func catalogFailure(code catalog.Code, object, name, field string) error {
	return &catalog.Error{Code: code, Object: object, Name: name, Field: field}
}

type uuidSource struct{}

func (uuidSource) Next() (string, error) { return uuid.NewString(), nil }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }
