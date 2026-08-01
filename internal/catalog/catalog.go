package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/HW-Yue/Memora/internal/logical"
	"github.com/HW-Yue/Memora/internal/store"
	"github.com/google/uuid"
)

const (
	catalogBucket = "catalog"
	catalogKey    = "snapshot"
)

type IDSource interface {
	Next() (string, error)
}

type Clock interface {
	Now() time.Time
}

type Options struct {
	IDs   IDSource
	Clock Clock
}

type Service struct {
	store store.Store
	ids   IDSource
	clock Clock
	mu    sync.Mutex
}

func New(database store.Store, options Options) *Service {
	if options.IDs == nil {
		options.IDs = uuidSource{}
	}
	if options.Clock == nil {
		options.Clock = systemClock{}
	}
	return &Service{store: database, ids: options.IDs, clock: options.Clock}
}

func (service *Service) CreateDatabase(ctx context.Context, definition DatabaseDefinition) (Database, error) {
	if err := require("database", definition.Name, "name", definition.Name); err != nil {
		return Database{}, err
	}
	if err := require("database", definition.Name, "purpose", definition.Purpose); err != nil {
		return Database{}, err
	}
	if err := require("database", definition.Name, "scope", definition.Scope); err != nil {
		return Database{}, err
	}
	var created Database
	err := service.mutate(ctx, func(state *snapshot) error {
		if _, exists := findDatabase(state, definition.Name); exists {
			return duplicate("database", definition.Name)
		}
		id, err := service.nextID("db")
		if err != nil {
			return err
		}
		now := service.clock.Now().UTC()
		created = Database{
			ID: id, Name: definition.Name, Aliases: []string{},
			Purpose: definition.Purpose, Scope: definition.Scope, AntiScope: definition.AntiScope,
			SchemaVersion: 1, CreatedAt: now, UpdatedAt: now, Tables: []Table{},
		}
		state.Databases = append(state.Databases, created)
		return nil
	})
	return created, err
}

func (service *Service) ShowDatabases(ctx context.Context) ([]Database, error) {
	state, err := service.read(ctx)
	if err != nil {
		return nil, err
	}
	databases := append([]Database{}, state.Databases...)
	sort.Slice(databases, func(left, right int) bool {
		return canonical(databases[left].Name) < canonical(databases[right].Name)
	})
	return databases, nil
}

func (service *Service) DescribeDatabase(ctx context.Context, name string) (Database, error) {
	state, err := service.read(ctx)
	if err != nil {
		return Database{}, err
	}
	database, found := findDatabase(&state, name)
	if !found {
		return Database{}, missing("database", name)
	}
	return *database, nil
}

// DescribeDatabaseIn resolves a Database from the caller's Store transaction.
func (service *Service) DescribeDatabaseIn(
	ctx context.Context,
	tx store.Tx,
	name string,
) (Database, error) {
	state, err := load(ctx, tx)
	if err != nil {
		return Database{}, err
	}
	database, found := findDatabase(&state, name)
	if !found {
		return Database{}, missing("database", name)
	}
	return *database, nil
}

func (service *Service) RenameDatabase(ctx context.Context, name, newName string) (Database, error) {
	if err := require("database", name, "new name", newName); err != nil {
		return Database{}, err
	}
	var renamed Database
	err := service.mutate(ctx, func(state *snapshot) error {
		database, found := findDatabase(state, name)
		if !found {
			return missing("database", name)
		}
		if conflict, exists := findDatabase(state, newName); exists && conflict.ID != database.ID {
			return duplicate("database", newName)
		}
		if database.Name == newName {
			renamed = *database
			return nil
		}
		database.Aliases = addAlias(database.Aliases, database.Name)
		database.Name = newName
		touchDatabase(database, service.clock.Now())
		renamed = *database
		return nil
	})
	return renamed, err
}

func (service *Service) CreateTable(ctx context.Context, databaseName string, definition TableDefinition) (Table, error) {
	if err := validateTableDefinition(definition); err != nil {
		return Table{}, err
	}
	var created Table
	err := service.mutate(ctx, func(state *snapshot) error {
		database, found := findDatabase(state, databaseName)
		if !found {
			return missing("database", databaseName)
		}
		if _, exists := findTable(database, definition.Name); exists {
			return duplicate("table", definition.Name)
		}
		if err := uniqueColumnDefinitions(definition.Columns); err != nil {
			return err
		}
		tableID, err := service.nextID("tbl")
		if err != nil {
			return err
		}
		now := service.clock.Now().UTC()
		created = Table{
			ID: tableID, DatabaseID: database.ID, Name: definition.Name, Aliases: []string{},
			Purpose: definition.Purpose, Scope: definition.Scope, AntiScope: definition.AntiScope,
			RowSemantics: definition.RowSemantics, SchemaVersion: 1,
			CreatedAt: now, UpdatedAt: now, Columns: []Column{},
		}
		for _, columnDefinition := range definition.Columns {
			column, err := service.newColumn(columnDefinition, now)
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

func (service *Service) ShowTables(ctx context.Context, databaseName string) ([]Table, error) {
	database, err := service.DescribeDatabase(ctx, databaseName)
	if err != nil {
		return nil, err
	}
	tables := append([]Table{}, database.Tables...)
	sort.Slice(tables, func(left, right int) bool {
		return canonical(tables[left].Name) < canonical(tables[right].Name)
	})
	return tables, nil
}

func (service *Service) DescribeTable(ctx context.Context, databaseName, tableName string) (Table, error) {
	database, err := service.DescribeDatabase(ctx, databaseName)
	if err != nil {
		return Table{}, err
	}
	table, found := findTable(&database, tableName)
	if !found {
		return Table{}, missing("table", tableName)
	}
	return *table, nil
}

// DescribeTableIn resolves a Table from the caller's Store transaction so
// schema validation and Row changes can share one consistent snapshot.
func (service *Service) DescribeTableIn(ctx context.Context, tx store.Tx, databaseName, tableName string) (Table, error) {
	state, err := load(ctx, tx)
	if err != nil {
		return Table{}, err
	}
	database, found := findDatabase(&state, databaseName)
	if !found {
		return Table{}, missing("database", databaseName)
	}
	table, found := findTable(database, tableName)
	if !found {
		return Table{}, missing("table", tableName)
	}
	return *table, nil
}

func (service *Service) RenameTable(ctx context.Context, databaseName, tableName, newName string) (Table, error) {
	if err := require("table", tableName, "new name", newName); err != nil {
		return Table{}, err
	}
	var renamed Table
	err := service.mutate(ctx, func(state *snapshot) error {
		database, found := findDatabase(state, databaseName)
		if !found {
			return missing("database", databaseName)
		}
		table, found := findTable(database, tableName)
		if !found {
			return missing("table", tableName)
		}
		if conflict, exists := findTable(database, newName); exists && conflict.ID != table.ID {
			return duplicate("table", newName)
		}
		if table.Name == newName {
			renamed = *table
			return nil
		}
		table.Aliases = addAlias(table.Aliases, table.Name)
		table.Name = newName
		touchTable(table, service.clock.Now())
		touchDatabase(database, service.clock.Now())
		renamed = *table
		return nil
	})
	return renamed, err
}

func (service *Service) nextID(prefix string) (string, error) {
	id, err := service.ids.Next()
	if err != nil {
		return "", fmt.Errorf("allocate catalog %s ID: %w", prefix, err)
	}
	if strings.TrimSpace(id) == "" {
		return "", fmt.Errorf("allocate catalog %s ID: empty ID", prefix)
	}
	return prefix + "_" + id, nil
}

func (service *Service) read(ctx context.Context) (snapshot, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	tx, err := service.store.Begin(ctx, store.ReadOnly)
	if err != nil {
		return snapshot{}, fmt.Errorf("read catalog: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	return load(ctx, tx)
}

func (service *Service) mutate(ctx context.Context, change func(*snapshot) error) error {
	service.mu.Lock()
	defer service.mu.Unlock()
	tx, err := service.store.Begin(ctx, store.ReadWrite)
	if err != nil {
		return fmt.Errorf("update catalog: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	state, err := load(ctx, tx)
	if err != nil {
		return err
	}
	if err := change(&state); err != nil {
		return err
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode catalog: %w", err)
	}
	if err := tx.Put(ctx, catalogBucket, catalogKey, encoded); err != nil {
		return fmt.Errorf("persist catalog: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit catalog: %w", err)
	}
	return nil
}

func load(ctx context.Context, tx store.Tx) (snapshot, error) {
	encoded, err := tx.Get(ctx, catalogBucket, catalogKey)
	if errors.Is(err, store.ErrNotFound) {
		return snapshot{Version: Version, Databases: []Database{}}, nil
	}
	if err != nil {
		return snapshot{}, fmt.Errorf("load catalog: %w", err)
	}
	var state snapshot
	if err := json.Unmarshal(encoded, &state); err != nil {
		return snapshot{}, fmt.Errorf("decode catalog: %w", err)
	}
	if state.Version != Version {
		return snapshot{}, fmt.Errorf("unsupported catalog version %q", state.Version)
	}
	if state.Databases == nil {
		state.Databases = []Database{}
	}
	for databaseIndex := range state.Databases {
		database := &state.Databases[databaseIndex]
		if database.Aliases == nil {
			database.Aliases = []string{}
		}
		if database.Tables == nil {
			database.Tables = []Table{}
		}
		for tableIndex := range database.Tables {
			table := &database.Tables[tableIndex]
			if table.DatabaseID == "" {
				table.DatabaseID = database.ID
			}
			if table.Aliases == nil {
				table.Aliases = []string{}
			}
			if table.Columns == nil {
				table.Columns = []Column{}
			}
			for columnIndex := range table.Columns {
				column := &table.Columns[columnIndex]
				if column.Aliases == nil {
					column.Aliases = []string{}
				}
				definition, err := logical.ParseDeclaration(column.Type)
				if err != nil {
					return snapshot{}, fmt.Errorf("decode catalog column type: %w", err)
				}
				if definition.Kind == logical.KindText && column.MaxCharacters > 0 {
					definition.MaxCharacters = column.MaxCharacters
				}
				column.Type = string(definition.Kind)
				column.MaxCharacters = definition.MaxCharacters
			}
		}
	}
	return state, nil
}

func validateTableDefinition(definition TableDefinition) error {
	if err := require("table", definition.Name, "name", definition.Name); err != nil {
		return err
	}
	if err := require("table", definition.Name, "purpose", definition.Purpose); err != nil {
		return err
	}
	if err := require("table", definition.Name, "row semantics", definition.RowSemantics); err != nil {
		return err
	}
	for _, column := range definition.Columns {
		if err := validateColumnDefinition(column); err != nil {
			return err
		}
	}
	return uniqueDisplayRoles(definition.Columns)
}

func locateTable(state *snapshot, databaseName, tableName string) (*Database, *Table, error) {
	database, found := findDatabase(state, databaseName)
	if !found {
		return nil, nil, missing("database", databaseName)
	}
	table, found := findTable(database, tableName)
	if !found {
		return nil, nil, missing("table", tableName)
	}
	return database, table, nil
}

func findDatabase(state *snapshot, name string) (*Database, bool) {
	for index := range state.Databases {
		if canonical(state.Databases[index].ID) == canonical(name) ||
			hasName(state.Databases[index].Name, state.Databases[index].Aliases, name) {
			return &state.Databases[index], true
		}
	}
	return nil, false
}

func findTable(database *Database, name string) (*Table, bool) {
	for index := range database.Tables {
		if hasName(database.Tables[index].Name, database.Tables[index].Aliases, name) {
			return &database.Tables[index], true
		}
	}
	return nil, false
}

func hasName(current string, aliases []string, candidate string) bool {
	key := canonical(candidate)
	if canonical(current) == key {
		return true
	}
	for _, alias := range aliases {
		if canonical(alias) == key {
			return true
		}
	}
	return false
}

func canonical(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func addAlias(aliases []string, name string) []string {
	for _, alias := range aliases {
		if canonical(alias) == canonical(name) {
			return aliases
		}
	}
	return append(aliases, name)
}

func touchDatabase(database *Database, now time.Time) {
	database.SchemaVersion++
	database.UpdatedAt = now.UTC()
}

func touchTable(table *Table, now time.Time) {
	table.SchemaVersion++
	table.UpdatedAt = now.UTC()
}

func require(object, name, field, value string) error {
	if strings.TrimSpace(value) == "" {
		return &Error{Code: CodeValidation, Object: object, Name: name, Field: field}
	}
	return nil
}

func duplicate(object, name string) error {
	return &Error{Code: CodeAlreadyExists, Object: object, Name: name}
}

func missing(object, name string) error {
	return &Error{Code: CodeNotFound, Object: object, Name: name}
}

type uuidSource struct{}

func (uuidSource) Next() (string, error) { return uuid.NewString(), nil }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }
