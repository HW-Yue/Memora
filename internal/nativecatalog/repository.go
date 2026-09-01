package nativecatalog

import (
	"bytes"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/change"
	"github.com/HW-Yue/Memora/internal/nativechange"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

const recordSchemaVersion = 1

type Repository struct{ file *nativestore.File }

func New(file *nativestore.File) *Repository { return &Repository{file: file} }

// Write publishes next, given the Catalog that was published before it.
//
// previous is what lets this stage only what changed without reading anything
// back: it says which revision each object already sits at and what its bytes
// were. Pass nil for the first Catalog a Database directory ever holds.
func (repository *Repository) Write(previous, next []catalog.Database) error {
	return repository.write(previous, next, nil)
}

func (repository *Repository) WriteCommitted(
	previous, next []catalog.Database, envelope change.Envelope,
) error {
	return repository.write(previous, next, &envelope)
}

func (repository *Repository) write(
	previous, databases []catalog.Database, envelope *change.Envelope,
) error {
	if repository == nil || repository.file == nil {
		return fmt.Errorf("%w: native file is required", ErrInvalid)
	}
	if err := validateCatalog(databases); err != nil {
		return err
	}
	// Where every object stood after the last publication. This is what replaces
	// the sweep stageVersion used to do, once per object written.
	prior, err := catalogPayloads(previous)
	if err != nil {
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
		staged, err := repository.stageVersion(transaction, nativestore.ObjectKindDatabase, database.ID, database.SchemaVersion, payload, prior)
		if err != nil {
			return fmt.Errorf("write database %q: %w", database.ID, err)
		}
		changed = changed || staged
		for tableIndex, table := range database.Tables {
			payload, err = encodeTable(table, uint64(tableIndex))
			if err != nil {
				return err
			}
			staged, err = repository.stageVersion(transaction, nativestore.ObjectKindTable, table.ID, table.SchemaVersion, payload, prior)
			if err != nil {
				return fmt.Errorf("write table %q: %w", table.ID, err)
			}
			changed = changed || staged
			for columnIndex, column := range table.Columns {
				payload, err = encodeColumn(column, table.ID, uint64(columnIndex))
				if err != nil {
					return err
				}
				staged, err = repository.stageVersion(transaction, nativestore.ObjectKindColumn, column.ID, column.SchemaVersion, payload, prior)
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

// catalogPayload is one object as it was last published: which revision, and the
// exact bytes. Both are needed — the revision decides whether this write is a
// new one, and the bytes decide whether a write at the same revision is a
// harmless repeat or a change that forgot to bump its schema version.
type catalogPayload struct {
	version uint64
	payload []byte
}

func catalogPayloads(databases []catalog.Database) (map[string]catalogPayload, error) {
	result := make(map[string]catalogPayload)
	for databaseIndex, database := range databases {
		payload, err := encodeDatabase(database, uint64(databaseIndex))
		if err != nil {
			return nil, err
		}
		result[payloadKey(nativestore.ObjectKindDatabase, database.ID)] =
			catalogPayload{version: database.SchemaVersion, payload: payload}
		for tableIndex, table := range database.Tables {
			payload, err := encodeTable(table, uint64(tableIndex))
			if err != nil {
				return nil, err
			}
			result[payloadKey(nativestore.ObjectKindTable, table.ID)] =
				catalogPayload{version: table.SchemaVersion, payload: payload}
			for columnIndex, column := range table.Columns {
				payload, err := encodeColumn(column, table.ID, uint64(columnIndex))
				if err != nil {
					return nil, err
				}
				result[payloadKey(nativestore.ObjectKindColumn, column.ID)] =
					catalogPayload{version: column.SchemaVersion, payload: payload}
			}
		}
	}
	return result, nil
}

func payloadKey(kind nativestore.ObjectKind, id string) string {
	return fmt.Sprintf("%d\x00%s", kind, id)
}

// stageVersion writes one object, if it moved.
//
// prior says where each object stood after the last publication. It used to be
// discovered by listing every record of that kind and decoding each one — a full
// sweep of the file per object written, so a Catalog of N objects cost N sweeps.
// The caller already knows what it published last, so it says so instead.
func (repository *Repository) stageVersion(
	transaction *nativestore.Transaction,
	kind nativestore.ObjectKind,
	id string,
	version uint64,
	payload []byte,
	prior map[string]catalogPayload,
) (bool, error) {
	existing := prior[payloadKey(kind, id)]
	if existing.version == version {
		// Same revision: identical bytes are the repeat that lets a retried
		// publication converge; different bytes are a change that forgot to say
		// it was one.
		if bytes.Equal(existing.payload, payload) {
			return false, nil
		}
		return false, fmt.Errorf("%w: object %q changes without schema version", ErrInvalid, id)
	}
	if existing.version >= version {
		return false, fmt.Errorf("%w: object %q schema version is stale", ErrInvalid, id)
	}
	recordID := id
	if existing.version > 0 {
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
	values := []fieldValue{
		{1, value.ID}, {2, order}, {3, value.Name}, {4, value.Aliases}, {5, value.Purpose},
		{6, value.Scope}, {7, value.AntiScope}, {8, value.SchemaVersion},
		{9, value.CreatedAt.UTC().UnixNano()}, {10, value.UpdatedAt.UTC().UnixNano()},
	}
	if value.ReadOnly {
		values = append(values, fieldValue{11, true})
	}
	if value.PackageSHA256 != "" {
		values = append(values, fieldValue{12, value.PackageSHA256})
	}
	if value.PackageSnapshotSHA256 != "" {
		values = append(values, fieldValue{13, value.PackageSnapshotSHA256})
	}
	if value.PackageSignerKeyID != "" {
		values = append(values, fieldValue{14, value.PackageSignerKeyID})
	}
	if value.ForkedFromDatabaseID != "" {
		values = append(values, fieldValue{15, value.ForkedFromDatabaseID})
	}
	if value.ForkedFromPackageSHA256 != "" {
		values = append(values, fieldValue{16, value.ForkedFromPackageSHA256})
	}
	if value.ForkedFromSnapshotSHA256 != "" {
		values = append(values, fieldValue{17, value.ForkedFromSnapshotSHA256})
	}
	if value.ArchivedAt != nil {
		values = append(values, fieldValue{18, value.ArchivedAt.UTC().UnixNano()})
	}
	if value.ArchivedReason != "" {
		values = append(values, fieldValue{19, value.ArchivedReason})
	}
	return encodeObject(values)
}

func encodeTable(value catalog.Table, order uint64) ([]byte, error) {
	values := []fieldValue{
		{1, value.ID}, {2, order}, {3, value.DatabaseID}, {4, value.Name}, {5, value.Aliases},
		{6, value.Purpose}, {7, value.Scope}, {8, value.AntiScope}, {9, value.RowSemantics},
		{10, value.SchemaVersion}, {11, value.CreatedAt.UTC().UnixNano()}, {12, value.UpdatedAt.UTC().UnixNano()},
	}
	if value.ArchivedAt != nil {
		values = append(values, fieldValue{13, value.ArchivedAt.UTC().UnixNano()})
	}
	if value.ArchivedReason != "" {
		values = append(values, fieldValue{14, value.ArchivedReason})
	}
	return encodeObject(values)
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
	if value.ArchivedAt != nil {
		values = append(values, fieldValue{14, value.ArchivedAt.UTC().UnixNano()})
	}
	if value.ArchivedReason != "" {
		values = append(values, fieldValue{15, value.ArchivedReason})
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
	readOnly, e11 := fields.optionalBool(11)
	packageHash, e12 := fields.optionalText(12)
	packageSnapshotHash, e13 := fields.optionalText(13)
	packageSigner, e14 := fields.optionalText(14)
	forkDatabase, e15 := fields.optionalText(15)
	forkPackage, e16 := fields.optionalText(16)
	forkSnapshot, e17 := fields.optionalText(17)
	archivedAt, e18 := fields.optionalTimestamp(18)
	archivedReason, e19 := fields.optionalText(19)
	if err := firstError(e1, e2, e3, e4, e5, e6, e7, e8, e9, e10, e11, e12, e13, e14, e15, e16, e17, e18, e19); err != nil {
		return databaseRecord{}, err
	}
	return databaseRecord{order: order, value: catalog.Database{ID: id, Name: name, Aliases: aliases, Purpose: purpose, Scope: scope, AntiScope: anti, SchemaVersion: schema, ReadOnly: readOnly, PackageSHA256: packageHash, PackageSnapshotSHA256: packageSnapshotHash, PackageSignerKeyID: packageSigner, ForkedFromDatabaseID: forkDatabase, ForkedFromPackageSHA256: forkPackage, ForkedFromSnapshotSHA256: forkSnapshot, ArchivedAt: archivedAt, ArchivedReason: archivedReason, CreatedAt: time.Unix(0, created).UTC(), UpdatedAt: time.Unix(0, updated).UTC()}}, nil
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
	archivedAt, e13 := fields.optionalTimestamp(13)
	archivedReason, e14 := fields.optionalText(14)
	if err := firstError(e1, e2, e3, e4, e5, e6, e7, e8, e9, e10, e11, e12, e13, e14); err != nil {
		return tableRecord{}, err
	}
	return tableRecord{order: order, value: catalog.Table{ID: id, DatabaseID: databaseID, Name: name, Aliases: aliases, Purpose: purpose, Scope: scope, AntiScope: anti, RowSemantics: semantics, SchemaVersion: schema, ArchivedAt: archivedAt, ArchivedReason: archivedReason, CreatedAt: time.Unix(0, created).UTC(), UpdatedAt: time.Unix(0, updated).UTC()}}, nil
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
	archivedAt, e14 := fields.optionalTimestamp(14)
	archivedReason, e15 := fields.optionalText(15)
	if err := firstError(e1, e2, e3, e4, e5, e6, e7, e8, e9, e10, e11, e12, e13, e14, e15); err != nil {
		return columnRecord{}, err
	}
	if maxChars < 0 || maxChars > math.MaxInt {
		return columnRecord{}, fmt.Errorf("%w: invalid max characters", ErrCorrupt)
	}
	if !validSemanticRole(semanticRole) || semanticRole != normalizeSemanticRole(semanticRole) {
		return columnRecord{}, fmt.Errorf("%w: invalid semantic role", ErrCorrupt)
	}
	return columnRecord{order: order, tableID: tableID, value: catalog.Column{ID: id, Name: name, Aliases: aliases, Type: kind, MaxCharacters: int(maxChars), Nullable: nullable, Purpose: purpose, SemanticRole: semanticRole, SchemaVersion: schema, ArchivedAt: archivedAt, ArchivedReason: archivedReason, CreatedAt: time.Unix(0, created).UTC(), UpdatedAt: time.Unix(0, updated).UTC()}}, nil
}

func firstError(errors ...error) error {
	for _, err := range errors {
		if err != nil {
			return err
		}
	}
	return nil
}
