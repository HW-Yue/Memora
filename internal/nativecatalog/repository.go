package nativecatalog

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/change"
	"github.com/HW-Yue/Memora/internal/nativechange"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

const recordSchemaVersion = 1

type Repository struct{ file *nativestore.File }

func New(file *nativestore.File) *Repository { return &Repository{file: file} }

func (repository *Repository) Write(databases []catalog.Database) error {
	return repository.write(databases, nil)
}

func (repository *Repository) WriteCommitted(
	databases []catalog.Database, envelope change.Envelope,
) error {
	return repository.write(databases, &envelope)
}

func (repository *Repository) write(
	databases []catalog.Database, envelope *change.Envelope,
) error {
	if repository == nil || repository.file == nil {
		return fmt.Errorf("%w: native file is required", ErrInvalid)
	}
	if err := validateCatalog(databases); err != nil {
		return err
	}
	transaction, err := repository.file.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback() }()
	changed := false
	for databaseIndex, database := range databases {
		payload, err := encodeDatabase(database, uint64(databaseIndex))
		if err != nil {
			return err
		}
		staged, err := repository.stageVersion(transaction, nativestore.ObjectKindDatabase, database.ID, database.SchemaVersion, payload)
		if err != nil {
			return fmt.Errorf("write database %q: %w", database.ID, err)
		}
		changed = changed || staged
		for tableIndex, table := range database.Tables {
			payload, err = encodeTable(table, uint64(tableIndex))
			if err != nil {
				return err
			}
			staged, err = repository.stageVersion(transaction, nativestore.ObjectKindTable, table.ID, table.SchemaVersion, payload)
			if err != nil {
				return fmt.Errorf("write table %q: %w", table.ID, err)
			}
			changed = changed || staged
			for columnIndex, column := range table.Columns {
				payload, err = encodeColumn(column, table.ID, uint64(columnIndex))
				if err != nil {
					return err
				}
				staged, err = repository.stageVersion(transaction, nativestore.ObjectKindColumn, column.ID, column.SchemaVersion, payload)
				if err != nil {
					return fmt.Errorf("write column %q: %w", column.ID, err)
				}
				changed = changed || staged
			}
		}
	}
	if !changed {
		return nil
	}
	if envelope != nil {
		if err := nativechange.Stage(transaction, *envelope); err != nil {
			return err
		}
	}
	return transaction.Commit()
}

// StageSnapshot writes a validated Catalog into an empty native snapshot transaction.
func (repository *Repository) StageSnapshot(transaction *nativestore.Transaction, databases []catalog.Database) error {
	if repository == nil || repository.file == nil || transaction == nil {
		return fmt.Errorf("%w: native file and transaction are required", ErrInvalid)
	}
	if err := validateCatalog(databases); err != nil {
		return err
	}
	for databaseIndex, database := range databases {
		payload, err := encodeDatabase(database, uint64(databaseIndex))
		if err != nil {
			return err
		}
		if err := transaction.Put(nativestore.ObjectKindDatabase, recordSchemaVersion, database.ID, payload); err != nil {
			return err
		}
		for tableIndex, table := range database.Tables {
			payload, err = encodeTable(table, uint64(tableIndex))
			if err != nil {
				return err
			}
			if err := transaction.Put(nativestore.ObjectKindTable, recordSchemaVersion, table.ID, payload); err != nil {
				return err
			}
			for columnIndex, column := range table.Columns {
				payload, err = encodeColumn(column, table.ID, uint64(columnIndex))
				if err != nil {
					return err
				}
				if err := transaction.Put(nativestore.ObjectKindColumn, recordSchemaVersion, column.ID, payload); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (repository *Repository) stageVersion(
	transaction *nativestore.Transaction,
	kind nativestore.ObjectKind,
	id string,
	version uint64,
	payload []byte,
) (bool, error) {
	ids, err := repository.file.IDs(kind)
	if err != nil {
		return false, err
	}
	latest := uint64(0)
	for _, recordID := range ids {
		if recordID != id && !strings.HasPrefix(recordID, id+"@") {
			continue
		}
		existing, err := repository.file.Get(kind, recordID)
		if err != nil {
			return false, err
		}
		fields, err := decodeFields(existing)
		if err != nil {
			return false, err
		}
		logicalID, err := fields.text(1)
		if err != nil || logicalID != id {
			continue
		}
		fieldID := uint16(10)
		if kind == nativestore.ObjectKindDatabase {
			fieldID = 8
		}
		existingVersion, err := fields.uint64(fieldID)
		if err != nil {
			return false, err
		}
		if existingVersion == version {
			if bytes.Equal(existing, payload) {
				return false, nil
			}
			return false, fmt.Errorf("%w: object %q changes without schema version", ErrInvalid, id)
		}
		if existingVersion > latest {
			latest = existingVersion
		}
	}
	if latest >= version {
		return false, fmt.Errorf("%w: object %q schema version is stale", ErrInvalid, id)
	}
	recordID := id
	if latest > 0 {
		recordID = fmt.Sprintf("%s@%020d", id, version)
	}
	if err := transaction.Put(kind, recordSchemaVersion, recordID, payload); err != nil {
		return false, err
	}
	return true, nil
}

func (repository *Repository) Read() ([]catalog.Database, error) {
	if repository == nil || repository.file == nil {
		return nil, fmt.Errorf("%w: native file is required", ErrInvalid)
	}
	databases, err := repository.readDatabases()
	if err != nil {
		return nil, err
	}
	tables, err := repository.readTables()
	if err != nil {
		return nil, err
	}
	columns, err := repository.readColumns()
	if err != nil {
		return nil, err
	}

	databaseByID := make(map[string]*databaseRecord, len(databases))
	for index := range databases {
		databaseByID[databases[index].value.ID] = &databases[index]
	}
	tableByID := make(map[string]*tableRecord, len(tables))
	for index := range tables {
		table := &tables[index]
		parent, ok := databaseByID[table.value.DatabaseID]
		if !ok {
			return nil, fmt.Errorf("%w: table %q has missing database", ErrCorrupt, table.value.ID)
		}
		parent.tables = append(parent.tables, table)
		tableByID[table.value.ID] = table
	}
	for index := range columns {
		column := columns[index]
		parent, ok := tableByID[column.tableID]
		if !ok {
			return nil, fmt.Errorf("%w: column %q has missing table", ErrCorrupt, column.value.ID)
		}
		parent.columns = append(parent.columns, column)
	}

	sort.Slice(databases, func(left, right int) bool { return databases[left].order < databases[right].order })
	result := make([]catalog.Database, 0, len(databases))
	for _, database := range databases {
		sort.Slice(database.tables, func(left, right int) bool { return database.tables[left].order < database.tables[right].order })
		for _, table := range database.tables {
			sort.Slice(table.columns, func(left, right int) bool { return table.columns[left].order < table.columns[right].order })
			displayRoles := map[string]struct{}{}
			for _, column := range table.columns {
				role := column.value.SemanticRole
				if !validSemanticRole(role) || role != normalizeSemanticRole(role) {
					return nil, fmt.Errorf("%w: column %q has invalid semantic role", ErrCorrupt, column.value.ID)
				}
				if role == "title" || role == "summary" {
					if _, exists := displayRoles[role]; exists {
						return nil, fmt.Errorf("%w: table %q has duplicate %s role", ErrCorrupt, table.value.ID, role)
					}
					displayRoles[role] = struct{}{}
				}
				table.value.Columns = append(table.value.Columns, column.value)
			}
			database.value.Tables = append(database.value.Tables, table.value)
		}
		result = append(result, database.value)
	}
	return result, nil
}

func (repository *Repository) readDatabaseRevision(id string, revision uint64) (databaseRecord, error) {
	payload, err := repository.readVersionRecord(nativestore.ObjectKindDatabase, id, revision)
	if err != nil {
		return databaseRecord{}, err
	}
	record, err := decodeDatabase(payload)
	if err != nil {
		return databaseRecord{}, fmt.Errorf("%w: decode database %q revision %d", ErrCorrupt, id, revision)
	}
	if record.value.ID != id || record.value.SchemaVersion != revision {
		return databaseRecord{}, fmt.Errorf("%w: database revision identity mismatch", ErrCorrupt)
	}
	return record, nil
}

func (repository *Repository) readTableRevision(id string, revision uint64) (tableRecord, error) {
	payload, err := repository.readVersionRecord(nativestore.ObjectKindTable, id, revision)
	if err != nil {
		return tableRecord{}, err
	}
	record, err := decodeTable(payload)
	if err != nil {
		return tableRecord{}, fmt.Errorf("%w: decode table %q revision %d", ErrCorrupt, id, revision)
	}
	if record.value.ID != id || record.value.SchemaVersion != revision {
		return tableRecord{}, fmt.Errorf("%w: table revision identity mismatch", ErrCorrupt)
	}
	return record, nil
}

func (repository *Repository) readColumnRevision(id string, revision uint64) (columnRecord, error) {
	payload, err := repository.readVersionRecord(nativestore.ObjectKindColumn, id, revision)
	if err != nil {
		return columnRecord{}, err
	}
	record, err := decodeColumn(payload)
	if err != nil {
		return columnRecord{}, fmt.Errorf("%w: decode column %q revision %d", ErrCorrupt, id, revision)
	}
	if record.value.ID != id || record.value.SchemaVersion != revision {
		return columnRecord{}, fmt.Errorf("%w: column revision identity mismatch", ErrCorrupt)
	}
	return record, nil
}

func (repository *Repository) readVersionRecord(
	kind nativestore.ObjectKind,
	id string,
	revision uint64,
) ([]byte, error) {
	if repository == nil || repository.file == nil || id == "" || revision == 0 {
		return nil, fmt.Errorf("%w: native file, object ID, and revision are required", ErrInvalid)
	}
	versionedID := fmt.Sprintf("%s@%020d", id, revision)
	payload, err := repository.file.Get(kind, versionedID)
	if err == nil {
		return payload, nil
	}
	if !errors.Is(err, nativestore.ErrNotFound) {
		return nil, err
	}
	return repository.file.Get(kind, id)
}

type databaseRecord struct {
	order  uint64
	value  catalog.Database
	tables []*tableRecord
}

type tableRecord struct {
	order   uint64
	value   catalog.Table
	columns []columnRecord
}

type columnRecord struct {
	order   uint64
	tableID string
	value   catalog.Column
}

func (repository *Repository) readDatabases() ([]databaseRecord, error) {
	ids, err := repository.file.IDs(nativestore.ObjectKindDatabase)
	if err != nil {
		return nil, err
	}
	latest := make(map[string]databaseRecord)
	for _, recordID := range ids {
		payload, err := repository.file.Get(nativestore.ObjectKindDatabase, recordID)
		if err != nil {
			return nil, err
		}
		record, err := decodeDatabase(payload)
		if err != nil {
			return nil, fmt.Errorf("%w: decode database %q", ErrCorrupt, recordID)
		}
		current, ok := latest[record.value.ID]
		if !ok || record.value.SchemaVersion > current.value.SchemaVersion {
			latest[record.value.ID] = record
		}
	}
	result := make([]databaseRecord, 0, len(latest))
	for _, record := range latest {
		result = append(result, record)
	}
	return result, nil
}

func (repository *Repository) readTables() ([]tableRecord, error) {
	ids, err := repository.file.IDs(nativestore.ObjectKindTable)
	if err != nil {
		return nil, err
	}
	latest := make(map[string]tableRecord)
	for _, recordID := range ids {
		payload, err := repository.file.Get(nativestore.ObjectKindTable, recordID)
		if err != nil {
			return nil, err
		}
		record, err := decodeTable(payload)
		if err != nil {
			return nil, fmt.Errorf("%w: decode table %q", ErrCorrupt, recordID)
		}
		current, ok := latest[record.value.ID]
		if !ok || record.value.SchemaVersion > current.value.SchemaVersion {
			latest[record.value.ID] = record
		}
	}
	result := make([]tableRecord, 0, len(latest))
	for _, record := range latest {
		result = append(result, record)
	}
	return result, nil
}

func (repository *Repository) readColumns() ([]columnRecord, error) {
	ids, err := repository.file.IDs(nativestore.ObjectKindColumn)
	if err != nil {
		return nil, err
	}
	latest := make(map[string]columnRecord)
	for _, recordID := range ids {
		payload, err := repository.file.Get(nativestore.ObjectKindColumn, recordID)
		if err != nil {
			return nil, err
		}
		record, err := decodeColumn(payload)
		if err != nil {
			return nil, fmt.Errorf("%w: decode column %q", ErrCorrupt, recordID)
		}
		current, ok := latest[record.value.ID]
		if !ok || record.value.SchemaVersion > current.value.SchemaVersion {
			latest[record.value.ID] = record
		}
	}
	result := make([]columnRecord, 0, len(latest))
	for _, record := range latest {
		result = append(result, record)
	}
	return result, nil
}

func validateCatalog(databases []catalog.Database) error {
	seen := make(map[string]struct{})
	for _, database := range databases {
		if err := validateIdentity(database.ID, database.Name, database.CreatedAt, database.UpdatedAt, seen); err != nil {
			return err
		}
		for _, table := range database.Tables {
			if table.DatabaseID != database.ID {
				return fmt.Errorf("%w: table %q has wrong database", ErrInvalid, table.ID)
			}
			if err := validateIdentity(table.ID, table.Name, table.CreatedAt, table.UpdatedAt, seen); err != nil {
				return err
			}
			displayRoles := map[string]string{}
			for _, column := range table.Columns {
				if err := validateIdentity(column.ID, column.Name, column.CreatedAt, column.UpdatedAt, seen); err != nil {
					return err
				}
				if !validSemanticRole(column.SemanticRole) || column.SemanticRole != normalizeSemanticRole(column.SemanticRole) {
					return fmt.Errorf("%w: column %q has invalid semantic role", ErrInvalid, column.ID)
				}
				role := normalizeSemanticRole(column.SemanticRole)
				if role == "title" || role == "summary" {
					if _, exists := displayRoles[role]; exists {
						return fmt.Errorf("%w: table %q has duplicate %s role", ErrInvalid, table.ID, role)
					}
					displayRoles[role] = column.ID
				}
			}
		}
	}
	return nil
}

func validateIdentity(id, name string, createdAt, updatedAt time.Time, seen map[string]struct{}) error {
	if id == "" || name == "" || createdAt.IsZero() || updatedAt.IsZero() {
		return fmt.Errorf("%w: ID, name, and timestamps are required", ErrInvalid)
	}
	if _, exists := seen[id]; exists {
		return fmt.Errorf("%w: duplicate object ID %q", ErrInvalid, id)
	}
	seen[id] = struct{}{}
	return nil
}

func encodeDatabase(value catalog.Database, order uint64) ([]byte, error) {
	return encodeObject([]fieldValue{
		{1, value.ID}, {2, order}, {3, value.Name}, {4, value.Aliases}, {5, value.Purpose},
		{6, value.Scope}, {7, value.AntiScope}, {8, value.SchemaVersion},
		{9, value.CreatedAt.UTC().UnixNano()}, {10, value.UpdatedAt.UTC().UnixNano()},
	})
}

func encodeTable(value catalog.Table, order uint64) ([]byte, error) {
	return encodeObject([]fieldValue{
		{1, value.ID}, {2, order}, {3, value.DatabaseID}, {4, value.Name}, {5, value.Aliases},
		{6, value.Purpose}, {7, value.Scope}, {8, value.AntiScope}, {9, value.RowSemantics},
		{10, value.SchemaVersion}, {11, value.CreatedAt.UTC().UnixNano()}, {12, value.UpdatedAt.UTC().UnixNano()},
	})
}

func encodeColumn(value catalog.Column, tableID string, order uint64) ([]byte, error) {
	values := []fieldValue{
		{1, value.ID}, {2, order}, {3, tableID}, {4, value.Name}, {5, value.Aliases},
		{6, value.Type}, {7, int64(value.MaxCharacters)}, {8, value.Nullable}, {9, value.Purpose},
		{10, value.SchemaVersion}, {11, value.CreatedAt.UTC().UnixNano()}, {12, value.UpdatedAt.UTC().UnixNano()},
	}
	if value.SemanticRole != "" {
		values = append(values, fieldValue{13, value.SemanticRole})
	}
	return encodeObject(values)
}

type fieldValue struct {
	id    uint16
	value any
}

func encodeObject(values []fieldValue) ([]byte, error) {
	fields := make([]field, 0, len(values))
	for _, value := range values {
		var item field
		var err error
		switch typed := value.value.(type) {
		case string:
			item, err = text(value.id, typed)
		case []string:
			item, err = textList(value.id, typed)
		case uint64:
			item = uintValue(value.id, typed)
		case int64:
			item = intValue(value.id, typed)
		case bool:
			item = boolValue(value.id, typed)
		default:
			return nil, fmt.Errorf("%w: unsupported field %d", ErrInvalid, value.id)
		}
		if err != nil {
			return nil, err
		}
		fields = append(fields, item)
	}
	return encodeFields(fields...)
}

func decodeDatabase(payload []byte) (databaseRecord, error) {
	fields, err := decodeFields(payload)
	if err != nil {
		return databaseRecord{}, err
	}
	id, e1 := fields.text(1)
	order, e2 := fields.uint64(2)
	name, e3 := fields.text(3)
	aliases, e4 := fields.textList(4)
	purpose, e5 := fields.text(5)
	scope, e6 := fields.text(6)
	anti, e7 := fields.text(7)
	schema, e8 := fields.uint64(8)
	created, e9 := fields.int64(9)
	updated, e10 := fields.int64(10)
	if err := firstError(e1, e2, e3, e4, e5, e6, e7, e8, e9, e10); err != nil {
		return databaseRecord{}, err
	}
	return databaseRecord{order: order, value: catalog.Database{ID: id, Name: name, Aliases: aliases, Purpose: purpose, Scope: scope, AntiScope: anti, SchemaVersion: schema, CreatedAt: time.Unix(0, created).UTC(), UpdatedAt: time.Unix(0, updated).UTC()}}, nil
}

func decodeTable(payload []byte) (tableRecord, error) {
	fields, err := decodeFields(payload)
	if err != nil {
		return tableRecord{}, err
	}
	id, e1 := fields.text(1)
	order, e2 := fields.uint64(2)
	databaseID, e3 := fields.text(3)
	name, e4 := fields.text(4)
	aliases, e5 := fields.textList(5)
	purpose, e6 := fields.text(6)
	scope, e7 := fields.text(7)
	anti, e8 := fields.text(8)
	semantics, e9 := fields.text(9)
	schema, e10 := fields.uint64(10)
	created, e11 := fields.int64(11)
	updated, e12 := fields.int64(12)
	if err := firstError(e1, e2, e3, e4, e5, e6, e7, e8, e9, e10, e11, e12); err != nil {
		return tableRecord{}, err
	}
	return tableRecord{order: order, value: catalog.Table{ID: id, DatabaseID: databaseID, Name: name, Aliases: aliases, Purpose: purpose, Scope: scope, AntiScope: anti, RowSemantics: semantics, SchemaVersion: schema, CreatedAt: time.Unix(0, created).UTC(), UpdatedAt: time.Unix(0, updated).UTC()}}, nil
}

func decodeColumn(payload []byte) (columnRecord, error) {
	fields, err := decodeFields(payload)
	if err != nil {
		return columnRecord{}, err
	}
	id, e1 := fields.text(1)
	order, e2 := fields.uint64(2)
	tableID, e3 := fields.text(3)
	name, e4 := fields.text(4)
	aliases, e5 := fields.textList(5)
	kind, e6 := fields.text(6)
	maxChars, e7 := fields.int64(7)
	nullable, e8 := fields.bool(8)
	purpose, e9 := fields.text(9)
	schema, e10 := fields.uint64(10)
	created, e11 := fields.int64(11)
	updated, e12 := fields.int64(12)
	semanticRole, e13 := fields.optionalText(13)
	if err := firstError(e1, e2, e3, e4, e5, e6, e7, e8, e9, e10, e11, e12, e13); err != nil {
		return columnRecord{}, err
	}
	if maxChars < 0 || maxChars > math.MaxInt {
		return columnRecord{}, fmt.Errorf("%w: invalid max characters", ErrCorrupt)
	}
	if !validSemanticRole(semanticRole) || semanticRole != normalizeSemanticRole(semanticRole) {
		return columnRecord{}, fmt.Errorf("%w: invalid semantic role", ErrCorrupt)
	}
	return columnRecord{order: order, tableID: tableID, value: catalog.Column{ID: id, Name: name, Aliases: aliases, Type: kind, MaxCharacters: int(maxChars), Nullable: nullable, Purpose: purpose, SemanticRole: semanticRole, SchemaVersion: schema, CreatedAt: time.Unix(0, created).UTC(), UpdatedAt: time.Unix(0, updated).UTC()}}, nil
}

func firstError(errors ...error) error {
	for _, err := range errors {
		if err != nil {
			return err
		}
	}
	return nil
}
