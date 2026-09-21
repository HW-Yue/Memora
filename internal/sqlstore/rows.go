package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/change"
	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/row"
)

// storedRow is one data-table row as SQLite holds it. Values are keyed by
// column ID so renaming a column never rewrites data.
type storedRow struct {
	ID             string
	Revision       uint64
	SchemaVersion  uint64
	CommitSequence uint64
	State          row.State
	Values         map[string]any
	RouteLeafIDs   []string
	Links          []Link
	SuccessorIDs   []string
	CreatedAt      string
	UpdatedAt      string
}

const rowColumns = `row_id, revision, schema_version, commit_sequence, row_state, values_json, route_leaf_ids, links, successor_ids, created_at, updated_at`

func scanRow(scanner interface{ Scan(...any) error }) (storedRow, error) {
	var value storedRow
	var values, leaves, links, successors, state string
	if err := scanner.Scan(&value.ID, &value.Revision, &value.SchemaVersion, &value.CommitSequence, &state,
		&values, &leaves, &links, &successors, &value.CreatedAt, &value.UpdatedAt); err != nil {
		return storedRow{}, err
	}
	value.State = row.State(state)
	value.Values = map[string]any{}
	if err := decodeJSON(values, &value.Values); err != nil {
		return storedRow{}, err
	}
	if err := decodeJSON(leaves, &value.RouteLeafIDs); err != nil {
		return storedRow{}, err
	}
	if err := decodeJSON(links, &value.Links); err != nil {
		return storedRow{}, err
	}
	if err := decodeJSON(successors, &value.SuccessorIDs); err != nil {
		return storedRow{}, err
	}
	return value, nil
}

func (t *tx) readRow(ctx context.Context, table catalog.Table, rowID string) (storedRow, error) {
	value, err := scanRow(t.q().QueryRowContext(ctx,
		`SELECT `+rowColumns+` FROM `+dataTable(table.ID)+` WHERE row_id = ?`, rowID))
	if errors.Is(err, sql.ErrNoRows) {
		return storedRow{}, fail(result.CodeNotFound, "row %q was not found", rowID)
	}
	return value, err
}

func (t *tx) writeRow(ctx context.Context, table catalog.Table, value storedRow, insert bool) error {
	if value.RouteLeafIDs == nil {
		value.RouteLeafIDs = []string{}
	}
	if value.Links == nil {
		value.Links = []Link{}
	}
	if value.SuccessorIDs == nil {
		value.SuccessorIDs = []string{}
	}
	arguments := []any{value.Revision, value.SchemaVersion, value.CommitSequence, string(value.State),
		encodeJSON(value.Values), encodeJSON(value.RouteLeafIDs), encodeJSON(value.Links), encodeJSON(value.SuccessorIDs),
		value.UpdatedAt}
	if insert {
		ordinal, err := t.nextCounter(ctx, "ordinal:"+table.ID)
		if err != nil {
			return err
		}
		_, err = t.q().ExecContext(ctx, `INSERT INTO `+dataTable(table.ID)+`(row_id, revision, schema_version, commit_sequence, row_state,
			values_json, route_leaf_ids, links, successor_ids, updated_at, created_at, ordinal) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			append([]any{value.ID}, append(arguments, value.CreatedAt, ordinal)...)...)
		return err
	}
	_, err := t.q().ExecContext(ctx, `UPDATE `+dataTable(table.ID)+` SET revision = ?, schema_version = ?, commit_sequence = ?,
		row_state = ?, values_json = ?, route_leaf_ids = ?, links = ?, successor_ids = ?, updated_at = ? WHERE row_id = ?`,
		append(arguments, value.ID)...)
	return err
}

// project turns a stored row into the Row the executor sees: values keyed by
// column name, archived columns dropped.
func project(table catalog.Table, value storedRow) row.Row {
	projected := row.Row{
		ID: value.ID, DatabaseID: table.DatabaseID, TableID: table.ID, SchemaVersion: value.SchemaVersion,
		Revision: value.Revision, CommitSequence: value.CommitSequence, State: value.State,
		Values: make(map[string]any, len(table.Columns)), RouteLeafIDs: value.RouteLeafIDs,
	}
	projected.CreatedAt = parseTime(value.CreatedAt)
	projected.UpdatedAt = parseTime(value.UpdatedAt)
	for _, column := range table.Columns {
		if column.Archived() {
			continue
		}
		projected.Values[column.Name] = value.Values[column.ID]
	}
	if len(projected.RouteLeafIDs) == 0 {
		projected.RouteLeafIDs = nil
	}
	return projected
}

func resolveColumnForWrite(table catalog.Table, name string) (catalog.Column, bool) {
	for _, column := range table.Columns {
		if column.Archived() {
			continue
		}
		if matchesName(column.ID, column.Name, column.Aliases, name) {
			return column, true
		}
	}
	return catalog.Column{}, false
}

// bindValues validates named values against the table and keys them by column ID.
func bindValues(table catalog.Table, values map[string]any) (map[string]any, error) {
	bound := make(map[string]any, len(values))
	for name, value := range values {
		column, ok := resolveColumnForWrite(table, name)
		if !ok {
			return nil, fail(result.CodeValidation, "unknown column %q", name)
		}
		if _, duplicate := bound[column.ID]; duplicate {
			return nil, fail(result.CodeValidation, "column %q is specified more than once", name)
		}
		normalized, err := column.Validate(value)
		if err != nil {
			return nil, err
		}
		bound[column.ID] = normalized
	}
	return bound, nil
}

func requireComplete(table catalog.Table, values map[string]any) error {
	for _, column := range table.Columns {
		if column.Archived() || column.Nullable {
			continue
		}
		if value, ok := values[column.ID]; !ok || value == nil {
			return fail(result.CodeConstraint, "column %q requires a value", column.Name)
		}
	}
	return nil
}

func metadataFrom(options row.WriteMetadata) change.Metadata {
	return change.Metadata{
		Actor: options.Actor, Source: options.Source, Reason: options.Reason,
		SourceReceiptID: options.SourceReceiptID, SourceKind: string(options.SourceKind),
		SourceLocator: options.SourceLocator, SourceContentHash: options.SourceContentHash,
	}
}

func (t *tx) appendHistory(ctx context.Context, table catalog.Table, value storedRow, operation history.Operation, metadata row.WriteMetadata) error {
	projected := project(table, value)
	record := history.Record{
		Version: history.Version, DatabaseID: table.DatabaseID, TableID: table.ID, RowID: value.ID,
		SchemaVersion: value.SchemaVersion, Revision: value.Revision, CommitSequence: value.CommitSequence,
		Operation: operation, State: string(value.State), Values: value.Values,
		Actor: fallback(metadata.Actor, "system"), Source: fallback(metadata.Source, "msql"),
		SourceKind: metadata.SourceKind, SourceReceiptID: metadata.SourceReceiptID,
		SourceLocator: metadata.SourceLocator, SourceContentHash: metadata.SourceContentHash,
		Reason: fallback(metadata.Reason, "write"), CreatedAt: projected.CreatedAt, UpdatedAt: projected.UpdatedAt,
		RecordedAt: t.now,
	}
	_, err := t.q().ExecContext(ctx, `INSERT INTO `+historyTable(table.ID)+`(row_id, revision, commit_sequence, body) VALUES (?, ?, ?, ?)`,
		value.ID, value.Revision, value.CommitSequence, encodeJSON(record))
	return err
}

func fallback(value, otherwise string) string {
	if strings.TrimSpace(value) == "" {
		return otherwise
	}
	return value
}

func (t *tx) rowChange(table catalog.Table, value storedRow, operation change.Operation, metadata row.WriteMetadata, related []string) {
	sort.Strings(related)
	t.recordEntry(change.Entry{
		ObjectKind: change.ObjectRow, DatabaseID: table.DatabaseID, TableID: table.ID, ObjectID: value.ID,
		Operation: operation, BeforeRevision: value.Revision - 1, AfterRevision: value.Revision,
		SchemaVersion: value.SchemaVersion, HistoryLocator: fmt.Sprintf("%s@%d", value.ID, value.Revision),
		RelatedObjectIDs: dedupe(related),
	}, metadataFrom(metadata))
}

func dedupe(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	sort.Strings(values)
	out := values[:0]
	for index, value := range values {
		if index == 0 || value != values[index-1] {
			out = append(out, value)
		}
	}
	return out
}

// ---- the operations, on a transaction ----

func (t *tx) insert(ctx context.Context, databaseName, tableName string, values map[string]any, options row.WriteOptions) (row.Row, error) {
	t.claimAttribution(metadataFrom(options.Metadata))
	table, err := t.liveTable(ctx, databaseName, tableName)
	if err != nil {
		return row.Row{}, err
	}
	if options.ExpectedSchemaVersion != 0 && options.ExpectedSchemaVersion != table.SchemaVersion {
		return row.Row{}, fail(result.CodeRevisionConflict, "schema version %d does not match table %q at %d",
			options.ExpectedSchemaVersion, table.Name, table.SchemaVersion)
	}
	bound, err := bindValues(table, values)
	if err != nil {
		return row.Row{}, err
	}
	if err := requireComplete(table, bound); err != nil {
		return row.Row{}, err
	}
	sequence, err := t.commitSequence(ctx)
	if err != nil {
		return row.Row{}, err
	}
	now := formatTime(t.now)
	value := storedRow{
		ID: newID("row_"), Revision: 1, SchemaVersion: table.SchemaVersion, CommitSequence: sequence,
		State: row.StateLive, Values: bound, CreatedAt: now, UpdatedAt: now,
	}
	// A write either names the leaf it mounts on or names a path for the engine
	// to complete; it never does both, and the path is completed here so the
	// nodes it creates commit in the same transaction as the Row.
	var leaves []string
	switch {
	case options.RoutePath != nil && options.RouteLeafIDs != nil:
		return row.Row{}, fail(result.CodeValidation,
			"a write takes either an explicit leaf or an implicit path, not both")
	case options.RoutePath != nil:
		leafID, err := t.ensureRoutePath(ctx, table, options.RoutePath)
		if err != nil {
			return row.Row{}, err
		}
		if leaves, err = t.mountLeaves(ctx, table, value.ID, []string{leafID}); err != nil {
			return row.Row{}, err
		}
	default:
		if leaves, err = t.mountLeaves(ctx, table, value.ID, options.RouteLeafIDs); err != nil {
			return row.Row{}, err
		}
	}
	value.RouteLeafIDs = leaves
	if err := requireSingleLeaf(value); err != nil {
		return row.Row{}, err
	}
	if err := t.writeRow(ctx, table, value, true); err != nil {
		return row.Row{}, err
	}
	if err := t.appendHistory(ctx, table, value, history.OperationInsert, options.Metadata); err != nil {
		return row.Row{}, err
	}
	t.rowChange(table, value, change.OperationInsert, options.Metadata, leaves)
	return project(table, value), nil
}

func (t *tx) get(ctx context.Context, databaseName, tableName, rowID string) (row.Row, error) {
	table, err := t.liveTable(ctx, databaseName, tableName)
	if err != nil {
		return row.Row{}, err
	}
	value, err := t.readRow(ctx, table, rowID)
	if err != nil {
		return row.Row{}, err
	}
	if value.State != row.StateLive {
		return row.Row{}, fail(result.CodeNotFound, "row %q was not found", rowID)
	}
	return project(table, value), nil
}

func (t *tx) listPage(ctx context.Context, databaseName, tableName string, limit int) ([]row.Row, bool, error) {
	table, err := t.liveTable(ctx, databaseName, tableName)
	if err != nil {
		return nil, false, err
	}
	if limit < 1 {
		return nil, false, fail(result.CodeValidation, "list limit must be positive")
	}
	rows, err := t.q().QueryContext(ctx, `SELECT `+rowColumns+` FROM `+dataTable(table.ID)+
		` WHERE row_state = 'live' ORDER BY ordinal LIMIT ?`, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = rows.Close() }()
	values := []row.Row{}
	for rows.Next() {
		value, err := scanRow(rows)
		if err != nil {
			return nil, false, err
		}
		values = append(values, project(table, value))
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if len(values) > limit {
		return values[:limit], true, nil
	}
	return values, false, nil
}

func (t *tx) liveRowForWrite(ctx context.Context, databaseName, tableName, rowID string, expected uint64) (catalog.Table, storedRow, error) {
	table, err := t.liveTable(ctx, databaseName, tableName)
	if err != nil {
		return catalog.Table{}, storedRow{}, err
	}
	value, err := t.readRow(ctx, table, rowID)
	if err != nil {
		return catalog.Table{}, storedRow{}, err
	}
	if value.State != row.StateLive {
		return catalog.Table{}, storedRow{}, fail(result.CodeNotFound, "row %q was not found", rowID)
	}
	if expected != 0 && expected != value.Revision {
		return catalog.Table{}, storedRow{}, fail(result.CodeRevisionConflict,
			"row %q is at revision %d, not %d", rowID, value.Revision, expected)
	}
	return table, value, nil
}

func (t *tx) advance(ctx context.Context, table catalog.Table, value *storedRow) error {
	sequence, err := t.commitSequence(ctx)
	if err != nil {
		return err
	}
	value.Revision++
	value.CommitSequence = sequence
	value.SchemaVersion = table.SchemaVersion
	value.UpdatedAt = formatTime(t.now)
	return nil
}

func (t *tx) updateRow(ctx context.Context, databaseName, tableName, rowID string, changes map[string]any, options row.WriteOptions) (row.Row, error) {
	t.claimAttribution(metadataFrom(options.Metadata))
	table, value, err := t.liveRowForWrite(ctx, databaseName, tableName, rowID, options.ExpectedRevision)
	if err != nil {
		return row.Row{}, err
	}
	bound, err := bindValues(table, changes)
	if err != nil {
		return row.Row{}, err
	}
	for id, changed := range bound {
		value.Values[id] = changed
	}
	if err := requireComplete(table, value.Values); err != nil {
		return row.Row{}, err
	}
	related := []string{}
	if options.RouteLeafIDs != nil {
		// The snapshot is the Row's complete membership, so this replaces rather
		// than adds. A Row that names another leaf moves, and the leaf it left
		// must stop pointing at it — otherwise the two directions disagree and
		// that leaf could never take another Row.
		leaves, err := t.mountLeaves(ctx, table, value.ID, options.RouteLeafIDs)
		if err != nil {
			return row.Row{}, err
		}
		for _, previous := range value.RouteLeafIDs {
			if contains(leaves, previous) {
				continue
			}
			if err := t.unmountLeaf(ctx, table, value.ID, previous); err != nil {
				return row.Row{}, err
			}
		}
		related = append(related, leaves...)
		value.RouteLeafIDs = leaves
	}
	if err := requireSingleLeaf(value); err != nil {
		return row.Row{}, err
	}
	if err := t.advance(ctx, table, &value); err != nil {
		return row.Row{}, err
	}
	if err := t.writeRow(ctx, table, value, false); err != nil {
		return row.Row{}, err
	}
	if err := t.appendHistory(ctx, table, value, history.OperationUpdate, options.Metadata); err != nil {
		return row.Row{}, err
	}
	t.rowChange(table, value, change.OperationUpdate, options.Metadata, related)
	return project(table, value), nil
}

func mergeLeaves(existing, added []string) []string {
	return dedupe(append(append([]string{}, existing...), added...))
}

// deleteRow deprecates the row. Nothing is physically removed: references to
// it resolve lazily (docs/product/row-lifecycle-successor.md).
func (t *tx) deleteRow(ctx context.Context, databaseName, tableName, rowID string, options row.WriteOptions) (row.Row, error) {
	table, value, err := t.liveRowForWrite(ctx, databaseName, tableName, rowID, options.ExpectedRevision)
	if err != nil {
		return row.Row{}, err
	}
	if err := t.advance(ctx, table, &value); err != nil {
		return row.Row{}, err
	}
	value.State = row.StateDeleted
	if err := t.writeRow(ctx, table, value, false); err != nil {
		return row.Row{}, err
	}
	if err := t.appendHistory(ctx, table, value, history.OperationDelete, options.Metadata); err != nil {
		return row.Row{}, err
	}
	t.rowChange(table, value, change.OperationDelete, options.Metadata, nil)
	return project(table, value), nil
}

func (t *tx) historyRecords(ctx context.Context, table catalog.Table, rowID string) ([]history.Record, error) {
	rows, err := t.q().QueryContext(ctx, `SELECT body FROM `+historyTable(table.ID)+` WHERE row_id = ? ORDER BY revision`, rowID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	records := []history.Record{}
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		var record history.Record
		if err := decodeJSON(body, &record); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func recordRow(table catalog.Table, record history.Record) row.Row {
	return project(table, storedRow{
		ID: record.RowID, Revision: record.Revision, SchemaVersion: record.SchemaVersion,
		CommitSequence: record.CommitSequence, State: row.State(record.State), Values: record.Values,
		CreatedAt: formatTime(record.CreatedAt), UpdatedAt: formatTime(record.UpdatedAt),
	})
}

func (t *tx) asOf(ctx context.Context, databaseName, tableName, rowID string, match func(history.Record) bool) (row.Row, error) {
	table, err := t.liveTable(ctx, databaseName, tableName)
	if err != nil {
		return row.Row{}, err
	}
	records, err := t.historyRecords(ctx, table, rowID)
	if err != nil {
		return row.Row{}, err
	}
	if len(records) == 0 {
		return row.Row{}, fail(result.CodeNotFound, "row %q was not found", rowID)
	}
	var chosen *history.Record
	for index := range records {
		if match(records[index]) {
			chosen = &records[index]
		}
	}
	if chosen == nil {
		return row.Row{}, fail(result.CodeNotFound, "row %q has no such revision", rowID)
	}
	return recordRow(table, *chosen), nil
}

func (t *tx) historyPage(ctx context.Context, databaseName, tableName, rowID, cursor string, limit int) ([]history.Record, history.ReadPage, error) {
	table, err := t.liveTable(ctx, databaseName, tableName)
	if err != nil {
		return nil, history.ReadPage{}, err
	}
	records, err := t.historyRecords(ctx, table, rowID)
	if err != nil {
		return nil, history.ReadPage{}, err
	}
	if len(records) == 0 {
		return nil, history.ReadPage{}, fail(result.CodeNotFound, "row %q was not found", rowID)
	}
	for index := range records {
		records[index].Values = recordRow(table, records[index]).Values
	}
	sort.Slice(records, func(left, right int) bool { return records[left].Revision > records[right].Revision })
	return history.Paginate(table.ID+"/"+rowID, cursor, limit, records)
}

func (t *tx) restore(ctx context.Context, databaseName, tableName, rowID string, revision uint64, options row.WriteOptions) (row.Row, error) {
	table, err := t.liveTable(ctx, databaseName, tableName)
	if err != nil {
		return row.Row{}, err
	}
	value, err := t.readRow(ctx, table, rowID)
	if err != nil {
		return row.Row{}, err
	}
	if options.ExpectedRevision != 0 && options.ExpectedRevision != value.Revision {
		return row.Row{}, fail(result.CodeRevisionConflict, "row %q is at revision %d, not %d", rowID, value.Revision, options.ExpectedRevision)
	}
	records, err := t.historyRecords(ctx, table, rowID)
	if err != nil {
		return row.Row{}, err
	}
	var target *history.Record
	for index := range records {
		if records[index].Revision == revision {
			target = &records[index]
		}
	}
	if target == nil || revision >= value.Revision {
		return row.Row{}, fail(result.CodeValidation, "row %q has no earlier revision %d", rowID, revision)
	}
	value.Values = map[string]any{}
	for id, restored := range target.Values {
		value.Values[id] = restored
	}
	value.State = row.State(target.State)
	if err := t.advance(ctx, table, &value); err != nil {
		return row.Row{}, err
	}
	if err := t.writeRow(ctx, table, value, false); err != nil {
		return row.Row{}, err
	}
	if err := t.appendHistory(ctx, table, value, history.OperationCompensate, options.Metadata); err != nil {
		return row.Row{}, err
	}
	t.rowChange(table, value, change.OperationRestore, options.Metadata, nil)
	return project(table, value), nil
}

// ---- leaf mounting ----

// requireSingleLeaf keeps the mount one-to-one. A live Row with no leaf can
// never be navigated to, and one with several breaks the equality between the
// leaf's row_id and the Row's own mount (docs/product/write-model.md §1.3).
// It runs before anything is written, so a refusal leaves nothing half-applied.
func requireSingleLeaf(value storedRow) error {
	if value.State != row.StateLive || len(value.RouteLeafIDs) == 1 {
		return nil
	}
	return fail(result.CodeConstraint,
		"a live Row needs exactly one leaf, got %d: attach an empty leaf, or create one first",
		len(value.RouteLeafIDs))
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// mountLeaves points each named leaf at rowID. A leaf holds at most one live
// row and a live Row hangs under exactly one leaf; requireSingleLeaf enforces
// the second half at the call sites that write a Row.
func (t *tx) mountLeaves(ctx context.Context, table catalog.Table, rowID string, leafIDs []string) ([]string, error) {
	leaves := dedupe(append([]string{}, leafIDs...))
	for _, leafID := range leaves {
		node, err := t.readRoute(ctx, table.ID, leafID)
		if err != nil {
			return nil, err
		}
		if node.Kind != router.KindLeaf || node.Deprecated {
			return nil, fail(result.CodeConstraint, "route %q is not a live leaf of table %q", leafID, table.Name)
		}
		if node.RowID != "" && node.RowID != rowID {
			holder, err := t.readRow(ctx, table, node.RowID)
			if err == nil && holder.State == row.StateLive {
				return nil, fail(result.CodeConstraint, "leaf %q already holds row %q", leafID, node.RowID)
			}
		}
		if node.RowID == rowID {
			continue
		}
		node.RowID = rowID
		if err := t.saveRoute(ctx, table, node, change.OperationUpdate); err != nil {
			return nil, err
		}
	}
	return leaves, nil
}
