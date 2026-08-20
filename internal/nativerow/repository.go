package nativerow

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/logical"
	"github.com/HW-Yue/Memora/internal/nativecatalog"
	"github.com/HW-Yue/Memora/internal/row"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

const recordSchemaVersion = 1

var (
	ErrCorrupt          = errors.New("native row record is corrupt")
	ErrInvalid          = errors.New("native row value is invalid")
	ErrRevisionConflict = errors.New("native row revision conflicts with latest")
)

type Repository struct{ file *nativestore.File }

func New(file *nativestore.File) *Repository { return &Repository{file: file} }

func (repository *Repository) Write(value row.Row) error {
	if value.Revision != 1 || value.State != row.StateLive {
		return fmt.Errorf("%w: initial row must be live revision 1", ErrInvalid)
	}
	return repository.writeRecord(value, value.ID)
}

func (repository *Repository) StageInitial(transaction *nativestore.Transaction, value row.Row) error {
	if transaction == nil || value.Revision != 1 || value.State != row.StateLive {
		return fmt.Errorf("%w: transaction and live revision 1 are required", ErrInvalid)
	}
	if _, err := repository.ReadIncludingDeleted(value.ID); !errors.Is(err, nativestore.ErrNotFound) {
		if err == nil {
			return ErrRevisionConflict
		}
		return err
	}
	table, err := repository.table(value.DatabaseID, value.TableID)
	if err != nil {
		return err
	}
	normalized, err := normalize(value, table)
	if err != nil {
		return err
	}
	payload, err := encode(normalized, table)
	if err != nil {
		return err
	}
	return transaction.Put(nativestore.ObjectKindRow, recordSchemaVersion, value.ID, payload)
}

func (repository *Repository) WriteRevision(value row.Row) error {
	if err := repository.validateRevision(value); err != nil {
		return err
	}
	return repository.writeRecord(value, revisionRecordID(value.ID, value.Revision))
}

func (repository *Repository) StageRevision(transaction *nativestore.Transaction, value row.Row) error {
	if transaction == nil {
		return fmt.Errorf("%w: transaction is required", ErrInvalid)
	}
	if err := repository.validateRevision(value); err != nil {
		return err
	}
	table, err := repository.table(value.DatabaseID, value.TableID)
	if err != nil {
		return err
	}
	normalized, err := normalize(value, table)
	if err != nil {
		return err
	}
	payload, err := encode(normalized, table)
	if err != nil {
		return err
	}
	return transaction.Put(nativestore.ObjectKindRow, recordSchemaVersion, revisionRecordID(value.ID, value.Revision), payload)
}

func (repository *Repository) validateRevision(value row.Row) error {
	latest, err := repository.ReadIncludingDeleted(value.ID)
	if err != nil {
		return err
	}
	if value.Revision != latest.Revision+1 ||
		value.DatabaseID != latest.DatabaseID || value.TableID != latest.TableID ||
		!value.CreatedAt.Equal(latest.CreatedAt) {
		return ErrRevisionConflict
	}
	return nil
}

func (repository *Repository) writeRecord(value row.Row, recordID string) error {
	table, err := repository.table(value.DatabaseID, value.TableID)
	if err != nil {
		return err
	}
	normalized, err := normalize(value, table)
	if err != nil {
		return err
	}
	payload, err := encode(normalized, table)
	if err != nil {
		return err
	}
	if err := repository.file.Put(nativestore.ObjectKindRow, recordSchemaVersion, recordID, payload); err != nil {
		return fmt.Errorf("write row %q: %w", value.ID, err)
	}
	return nil
}

func (repository *Repository) Read(id string) (row.Row, error) {
	value, err := repository.ReadIncludingDeleted(id)
	if err != nil {
		return row.Row{}, err
	}
	if value.State == row.StateDeleted || value.State == row.StateSuperseded {
		return row.Row{}, nativestore.ErrNotFound
	}
	return value, nil
}

func (repository *Repository) ReadIncludingDeleted(id string) (row.Row, error) {
	if repository == nil || repository.file == nil || id == "" {
		return row.Row{}, fmt.Errorf("%w: native file and RowID are required", ErrInvalid)
	}
	ids, err := repository.file.IDs(nativestore.ObjectKindRow)
	if err != nil {
		return row.Row{}, err
	}
	var latest row.Row
	found := false
	for _, recordID := range ids {
		if recordID != id && !strings.HasPrefix(recordID, id+"@") {
			continue
		}
		value, err := repository.readRecord(recordID)
		if err != nil {
			return row.Row{}, err
		}
		if value.ID == id && (!found || value.Revision > latest.Revision) {
			latest, found = value, true
		}
	}
	if !found {
		return row.Row{}, nativestore.ErrNotFound
	}
	return latest, nil
}

func (repository *Repository) ReadRevision(id string, revision uint64) (row.Row, error) {
	if id == "" || revision == 0 {
		return row.Row{}, fmt.Errorf("%w: RowID and revision are required", ErrInvalid)
	}
	return repository.readRecord(revisionRecordID(id, revision))
}

func (repository *Repository) ReadRevisionWithTable(
	id string,
	revision uint64,
	table catalog.Table,
) (row.Row, error) {
	if id == "" || revision == 0 || table.ID == "" || table.DatabaseID == "" {
		return row.Row{}, fmt.Errorf("%w: RowID, revision, and Table are required", ErrInvalid)
	}
	return repository.readRecordWithTable(revisionRecordID(id, revision), table)
}

func (repository *Repository) ReadAsOfCommit(id string, commitSequence uint64) (row.Row, error) {
	if id == "" || commitSequence == 0 {
		return row.Row{}, fmt.Errorf("%w: RowID and commit sequence are required", ErrInvalid)
	}
	ids, err := repository.file.IDs(nativestore.ObjectKindRow)
	if err != nil {
		return row.Row{}, err
	}
	var selected row.Row
	found := false
	for _, recordID := range ids {
		value, err := repository.readRecord(recordID)
		if err != nil {
			return row.Row{}, err
		}
		if value.ID != id || value.CommitSequence > commitSequence {
			continue
		}
		if !found || value.CommitSequence > selected.CommitSequence {
			selected, found = value, true
		}
	}
	if !found {
		return row.Row{}, nativestore.ErrNotFound
	}
	return selected, nil
}

func (repository *Repository) List(databaseID, tableID string, limit int) ([]row.Row, bool, error) {
	if limit < 1 || limit > 1000 {
		return nil, false, fmt.Errorf("%w: limit must be between 1 and 1000", ErrInvalid)
	}
	ids, err := repository.file.IDs(nativestore.ObjectKindRow)
	if err != nil {
		return nil, false, err
	}
	logicalIDs := make(map[string]struct{})
	for _, recordID := range ids {
		payload, err := repository.file.Get(nativestore.ObjectKindRow, recordID)
		if err != nil {
			return nil, false, err
		}
		value, err := decode(payload)
		if err != nil {
			return nil, false, err
		}
		logicalIDs[value.ID] = struct{}{}
	}
	sorted := make([]string, 0, len(logicalIDs))
	for id := range logicalIDs {
		sorted = append(sorted, id)
	}
	sort.Strings(sorted)
	result := make([]row.Row, 0, limit)
	for _, id := range sorted {
		value, err := repository.Read(id)
		if errors.Is(err, nativestore.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, false, err
		}
		if value.DatabaseID != databaseID || value.TableID != tableID {
			continue
		}
		if len(result) == limit {
			return result, true, nil
		}
		result = append(result, value)
	}
	return result, false, nil
}

func (repository *Repository) AllRows() ([]row.Row, error) {
	ids, err := repository.file.IDs(nativestore.ObjectKindRow)
	if err != nil {
		return nil, err
	}
	logical := map[string]struct{}{}
	for _, recordID := range ids {
		value, err := repository.readRecord(recordID)
		if err != nil {
			return nil, err
		}
		logical[value.ID] = struct{}{}
	}
	result := make([]row.Row, 0, len(logical))
	for id := range logical {
		value, err := repository.ReadIncludingDeleted(id)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result, nil
}

// AllRowVersions returns every immutable Row revision in stable RowID/revision order.
func (repository *Repository) AllRowVersions() ([]row.Row, error) {
	if repository == nil || repository.file == nil {
		return nil, fmt.Errorf("%w: native file is required", ErrInvalid)
	}
	databases, err := nativecatalog.New(repository.file).Read()
	if err != nil {
		return nil, err
	}
	tables := make(map[string]catalog.Table)
	for _, database := range databases {
		for _, table := range database.Tables {
			tables[table.ID] = table
		}
	}
	ids, err := repository.file.IDs(nativestore.ObjectKindRow)
	if err != nil {
		return nil, err
	}
	result := make([]row.Row, 0, len(ids))
	for _, recordID := range ids {
		value, err := repository.readDecodedRecord(recordID)
		if err != nil {
			return nil, err
		}
		table, exists := tables[value.TableID]
		if !exists || table.DatabaseID != value.DatabaseID {
			return nil, fmt.Errorf("%w: row %q has invalid Catalog reference", ErrCorrupt, value.ID)
		}
		value, err = normalizeDecodedRecord(value, table)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].ID != result[right].ID {
			return result[left].ID < result[right].ID
		}
		return result[left].Revision < result[right].Revision
	})
	return result, nil
}

func (repository *Repository) NextCommitSequence() (uint64, error) {
	rows, err := repository.AllRows()
	if err != nil {
		return 0, err
	}
	var latest uint64
	for _, value := range rows {
		latest = max(latest, value.CommitSequence)
	}
	relations, err := repository.AllRelationVersions()
	if err != nil {
		return 0, err
	}
	for _, value := range relations {
		latest = max(latest, value.CommitSequence)
	}
	return latest + 1, nil
}

func (repository *Repository) StageSnapshotRow(transaction *nativestore.Transaction, value row.Row, table catalog.Table) error {
	if transaction == nil {
		return fmt.Errorf("%w: transaction is required", ErrInvalid)
	}
	normalized, err := normalize(value, table)
	if err != nil {
		return err
	}
	payload, err := encode(normalized, table)
	if err != nil {
		return err
	}
	return transaction.Put(nativestore.ObjectKindRow, recordSchemaVersion, revisionRecordID(value.ID, value.Revision), payload)
}

func (repository *Repository) readRecord(recordID string) (row.Row, error) {
	value, err := repository.readDecodedRecord(recordID)
	if err != nil {
		return row.Row{}, err
	}
	table, err := repository.table(value.DatabaseID, value.TableID)
	if err != nil {
		return row.Row{}, fmt.Errorf("%w: row %q has invalid catalog reference", ErrCorrupt, value.ID)
	}
	return normalizeDecodedRecord(value, table)
}

func (repository *Repository) readRecordWithTable(recordID string, table catalog.Table) (row.Row, error) {
	value, err := repository.readDecodedRecord(recordID)
	if err != nil {
		return row.Row{}, err
	}
	if value.DatabaseID != table.DatabaseID || value.TableID != table.ID {
		return row.Row{}, fmt.Errorf("%w: Row body does not belong to indexed Table", ErrCorrupt)
	}
	return normalizeDecodedRecord(value, table)
}

func (repository *Repository) readDecodedRecord(recordID string) (row.Row, error) {
	payload, err := repository.file.Get(nativestore.ObjectKindRow, recordID)
	if err != nil {
		return row.Row{}, err
	}
	value, err := decode(payload)
	if err != nil {
		return row.Row{}, fmt.Errorf("%w: decode row record %q", ErrCorrupt, recordID)
	}
	if revisionRecordID(value.ID, value.Revision) != recordID {
		return row.Row{}, fmt.Errorf("%w: row record identity mismatch", ErrCorrupt)
	}
	return value, nil
}

func normalizeDecodedRecord(value row.Row, table catalog.Table) (row.Row, error) {
	normalized, err := normalize(value, table)
	if err != nil {
		return row.Row{}, fmt.Errorf("%w: row %q fails catalog validation", ErrCorrupt, value.ID)
	}
	return normalized, nil
}

func revisionRecordID(id string, revision uint64) string {
	if revision == 1 {
		return id
	}
	return id + "@" + fmt.Sprintf("%020d", revision)
}

func (repository *Repository) table(databaseID, tableID string) (catalog.Table, error) {
	if repository == nil || repository.file == nil {
		return catalog.Table{}, fmt.Errorf("%w: native file is required", ErrInvalid)
	}
	databases, err := nativecatalog.New(repository.file).Read()
	if err != nil {
		return catalog.Table{}, err
	}
	for _, database := range databases {
		if database.ID != databaseID {
			continue
		}
		for _, table := range database.Tables {
			if table.ID == tableID {
				return table, nil
			}
		}
	}
	return catalog.Table{}, fmt.Errorf("%w: table %q does not belong to database %q", ErrInvalid, tableID, databaseID)
}

func normalize(value row.Row, table catalog.Table) (row.Row, error) {
	// A Row keeps the SchemaVersion it was written at. Catalog changes bump the
	// Table without rewriting Rows — a schema-change plan rewrites them only
	// when it reports RequiresRowRewrite — so demanding equality here made every
	// Row unreadable after any Catalog bump, a rename included. A version ahead
	// of the Table is still invalid, and the column checks below remain the
	// substantive guarantee that the Row fits the current schema.
	if value.ID == "" || value.DatabaseID == "" || value.TableID == "" ||
		value.Revision == 0 || (value.State != row.StateLive && value.State != row.StateDeleted && value.State != row.StateSuperseded) ||
		value.CreatedAt.IsZero() || value.UpdatedAt.IsZero() ||
		value.SchemaVersion == 0 || value.SchemaVersion > table.SchemaVersion {
		return row.Row{}, fmt.Errorf("%w: invalid identity, state, revision, timestamps, or schema", ErrInvalid)
	}
	columns := make(map[string]catalog.Column, len(table.Columns))
	for _, column := range table.Columns {
		columns[column.ID] = column
	}
	for columnID := range value.Values {
		if _, ok := columns[columnID]; !ok {
			return row.Row{}, fmt.Errorf("%w: unknown column %q", ErrInvalid, columnID)
		}
	}
	result := value
	result.CreatedAt = value.CreatedAt.UTC()
	result.UpdatedAt = value.UpdatedAt.UTC()
	result.Values = make(map[string]any, len(columns))
	for _, column := range table.Columns {
		input, ok := value.Values[column.ID]
		if !ok {
			if !column.Nullable {
				return row.Row{}, fmt.Errorf("%w: missing column %q", ErrInvalid, column.ID)
			}
			input = nil
		}
		normalized, err := column.Validate(input)
		if err != nil {
			return row.Row{}, fmt.Errorf("%w: column %q: %v", ErrInvalid, column.ID, err)
		}
		result.Values[column.ID] = normalized
	}
	return result, nil
}

type encoder struct{ bytes []byte }

func encode(value row.Row, table catalog.Table) ([]byte, error) {
	output := encoder{bytes: make([]byte, 0, 256)}
	output.u16(recordSchemaVersion)
	output.u16(0)
	output.u64(value.SchemaVersion)
	output.u64(value.Revision)
	output.u64(value.CommitSequence)
	output.i64(value.CreatedAt.UnixNano())
	output.i64(value.UpdatedAt.UnixNano())
	for _, text := range []string{value.ID, value.DatabaseID, value.TableID, string(value.State)} {
		if err := output.text(text); err != nil {
			return nil, err
		}
	}
	columns := append([]catalog.Column(nil), table.Columns...)
	sort.Slice(columns, func(left, right int) bool { return columns[left].ID < columns[right].ID })
	output.u32(uint32(len(columns)))
	for _, column := range columns {
		if err := output.text(column.ID); err != nil {
			return nil, err
		}
		if err := output.value(logical.Kind(column.Type), value.Values[column.ID]); err != nil {
			return nil, fmt.Errorf("%w: column %q", err, column.ID)
		}
	}
	return output.bytes, nil
}

func (output *encoder) value(kind logical.Kind, value any) error {
	if value == nil {
		output.bytes = append(output.bytes, 0)
		return nil
	}
	switch kind {
	case logical.KindInteger:
		output.bytes = append(output.bytes, 1)
		output.i64(value.(int64))
	case logical.KindBoolean:
		output.bytes = append(output.bytes, 2)
		if value.(bool) {
			output.bytes = append(output.bytes, 1)
		} else {
			output.bytes = append(output.bytes, 0)
		}
	case logical.KindTimestamp:
		output.bytes = append(output.bytes, 3)
		output.i64(value.(time.Time).UnixNano())
	case logical.KindText:
		output.bytes = append(output.bytes, 4)
		return output.text(value.(string))
	case logical.KindRelationID:
		output.bytes = append(output.bytes, 5)
		return output.text(value.(string))
	default:
		return fmt.Errorf("%w: unsupported logical type", ErrInvalid)
	}
	return nil
}

func (output *encoder) text(value string) error {
	if !utf8.ValidString(value) || len(value) > math.MaxUint32 {
		return fmt.Errorf("%w: invalid text", ErrInvalid)
	}
	output.u32(uint32(len(value)))
	output.bytes = append(output.bytes, value...)
	return nil
}

func (output *encoder) u16(value uint16) {
	output.bytes = binary.LittleEndian.AppendUint16(output.bytes, value)
}
func (output *encoder) u32(value uint32) {
	output.bytes = binary.LittleEndian.AppendUint32(output.bytes, value)
}
func (output *encoder) u64(value uint64) {
	output.bytes = binary.LittleEndian.AppendUint64(output.bytes, value)
}
func (output *encoder) i64(value int64) { output.u64(uint64(value)) }

type decoder struct {
	bytes  []byte
	offset int
}

func decode(payload []byte) (row.Row, error) {
	input := decoder{bytes: payload}
	version, err := input.u16()
	if err != nil || version != recordSchemaVersion {
		return row.Row{}, fmt.Errorf("%w: invalid version", ErrCorrupt)
	}
	flags, err := input.u16()
	if err != nil || flags != 0 {
		return row.Row{}, fmt.Errorf("%w: invalid flags", ErrCorrupt)
	}
	value := row.Row{}
	if value.SchemaVersion, err = input.u64(); err != nil {
		return row.Row{}, err
	}
	if value.Revision, err = input.u64(); err != nil {
		return row.Row{}, err
	}
	if value.CommitSequence, err = input.u64(); err != nil {
		return row.Row{}, err
	}
	created, err := input.i64()
	if err != nil {
		return row.Row{}, err
	}
	updated, err := input.i64()
	if err != nil {
		return row.Row{}, err
	}
	value.CreatedAt, value.UpdatedAt = time.Unix(0, created).UTC(), time.Unix(0, updated).UTC()
	if value.ID, err = input.text(); err != nil {
		return row.Row{}, err
	}
	if value.DatabaseID, err = input.text(); err != nil {
		return row.Row{}, err
	}
	if value.TableID, err = input.text(); err != nil {
		return row.Row{}, err
	}
	state, err := input.text()
	if err != nil {
		return row.Row{}, err
	}
	value.State = row.State(state)
	count, err := input.u32()
	if err != nil || count > 10000 {
		return row.Row{}, fmt.Errorf("%w: invalid value count", ErrCorrupt)
	}
	value.Values = make(map[string]any, count)
	for index := uint32(0); index < count; index++ {
		columnID, err := input.text()
		if err != nil {
			return row.Row{}, err
		}
		if _, duplicate := value.Values[columnID]; duplicate {
			return row.Row{}, fmt.Errorf("%w: duplicate column", ErrCorrupt)
		}
		value.Values[columnID], err = input.value()
		if err != nil {
			return row.Row{}, err
		}
	}
	if input.offset != len(input.bytes) {
		return row.Row{}, fmt.Errorf("%w: trailing bytes", ErrCorrupt)
	}
	return value, nil
}

func (input *decoder) value() (any, error) {
	tag, err := input.take(1)
	if err != nil {
		return nil, err
	}
	switch tag[0] {
	case 0:
		return nil, nil
	case 1:
		return input.i64()
	case 2:
		value, err := input.take(1)
		if err != nil || value[0] > 1 {
			return nil, fmt.Errorf("%w: invalid boolean", ErrCorrupt)
		}
		return value[0] == 1, nil
	case 3:
		nanos, err := input.i64()
		return time.Unix(0, nanos).UTC(), err
	case 4, 5:
		return input.text()
	default:
		return nil, fmt.Errorf("%w: invalid value tag", ErrCorrupt)
	}
}

func (input *decoder) text() (string, error) {
	length, err := input.u32()
	if err != nil || uint64(length) > uint64(len(input.bytes)-input.offset) {
		return "", fmt.Errorf("%w: truncated text", ErrCorrupt)
	}
	value, err := input.take(int(length))
	if err != nil || !utf8.Valid(value) {
		return "", fmt.Errorf("%w: invalid text", ErrCorrupt)
	}
	return string(value), nil
}

func (input *decoder) take(length int) ([]byte, error) {
	if length < 0 || length > len(input.bytes)-input.offset {
		return nil, fmt.Errorf("%w: truncated value", ErrCorrupt)
	}
	value := input.bytes[input.offset : input.offset+length]
	input.offset += length
	return value, nil
}

func (input *decoder) u16() (uint16, error) {
	value, err := input.take(2)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint16(value), nil
}
func (input *decoder) u32() (uint32, error) {
	value, err := input.take(4)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(value), nil
}
func (input *decoder) u64() (uint64, error) {
	value, err := input.take(8)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(value), nil
}
func (input *decoder) i64() (int64, error) {
	value, err := input.u64()
	return int64(value), err
}
