package executor

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/change"
	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/security"
	"github.com/HW-Yue/Memora/internal/store/changeindex"
)

type committedChangeReads interface {
	ListCommittedChanges(context.Context, string, uint64, uint64, int) ([]change.Envelope, uint64, bool, error)
	GetCommittedChange(context.Context, string, string) (change.Envelope, error)
}

func (engine *Engine) showCommittedChanges(
	ctx context.Context,
	show *ast.ShowStatement,
	bound bindings,
) (Output, error) {
	reads, ok := engine.rows.(committedChangeReads)
	if !ok {
		return Output{}, executeError(result.CodeUnsupported, "committed change reads require Page Store authority")
	}
	databaseID, scope, err := engine.changeReadScope(ctx, show, changeTimelineKind)
	if err != nil {
		return Output{}, err
	}
	limit, err := changeReadLimit(show.Limit, bound)
	if err != nil {
		return Output{}, err
	}
	if show.After != nil && show.Cursor != nil {
		return Output{}, executeError(result.CodeValidation, "SHOW CHANGES AFTER and CURSOR are mutually exclusive")
	}
	after, snapshot, cursor := uint64(0), uint64(0), ""
	if show.After != nil {
		after, err = changeNonnegativeInteger(show.After, bound, "SHOW CHANGES AFTER COMMIT_SEQUENCE")
		if err != nil {
			return Output{}, err
		}
	}
	if show.Cursor != nil {
		cursor, err = relationshipString(show.Cursor, catalog.Table{}, bound, "Change cursor")
		if err != nil || cursor == "" || len(cursor) > changeCursorMaximum {
			return Output{}, executeError(result.CodeValidation, "Change cursor is invalid")
		}
		decoded, decodeErr := decodeChangeCursor(cursor)
		if decodeErr != nil || decoded.Kind != changeTimelineKind || decoded.Scope != scope {
			return Output{}, executeError(result.CodeValidation, "Change cursor is invalid for this timeline")
		}
		after, snapshot = decoded.Position, decoded.Snapshot
	}
	values, snapshot, more, err := reads.ListCommittedChanges(ctx, databaseID, after, snapshot, int(limit))
	if err != nil {
		return Output{}, normalizeChangeReadError(err)
	}
	next := ""
	if more {
		if len(values) == 0 {
			return Output{}, executeError(result.CodeInternal, "change timeline returned an empty continuation Page")
		}
		next, err = encodeChangeCursor(changeCursorCore{
			Version: changeCursorVersion, Kind: changeTimelineKind, Scope: scope,
			Snapshot: snapshot, Position: values[len(values)-1].CommitSequence,
		})
		if err != nil {
			return Output{}, executeError(result.CodeInternal, "Change cursor could not be encoded")
		}
	}
	output := Output{
		Columns: changeTimelineColumns(), Rows: make([]result.Row, 0, len(values)),
		Truncated: more, NextCursor: next,
		Page: &result.ListPage{
			Version: result.ListPageVersion, Limit: limit, Cursor: cursor,
			Snapshot: changeSnapshot(scope, snapshot, ""), Truncated: more, NextCursor: next,
		},
	}
	for _, value := range values {
		output.Rows = append(output.Rows, result.Row{
			"version": value.Version, "transaction_id": value.TransactionID,
			"commit_sequence": value.CommitSequence, "committed_at": value.CommittedAt,
			"actor": value.Actor, "source": value.Source, "reason": value.Reason,
			"source_receipt_id": value.SourceReceiptID,
			"database_ids":      visibleChangeDatabaseIDs(value.DatabaseIDs, databaseID),
			"entry_count":       uint64(len(scopedChangeEntries(value.Entries, databaseID))), "checksum": value.Checksum,
		})
	}
	return output, nil
}

func visibleChangeDatabaseIDs(values []string, databaseID string) []string {
	if databaseID != "" {
		return []string{databaseID}
	}
	return append([]string(nil), values...)
}

func (engine *Engine) showCommittedChange(
	ctx context.Context,
	show *ast.ShowStatement,
	bound bindings,
) (Output, error) {
	reads, ok := engine.rows.(committedChangeReads)
	if !ok {
		return Output{}, executeError(result.CodeUnsupported, "committed change reads require Page Store authority")
	}
	databaseID, scope, err := engine.changeReadScope(ctx, show, changeEntriesKind)
	if err != nil {
		return Output{}, err
	}
	limit, err := changeReadLimit(show.Limit, bound)
	if err != nil {
		return Output{}, err
	}
	transactionID, err := relationshipString(show.Change, catalog.Table{}, bound, "Transaction ID")
	if err != nil || !validCommittedTransactionID(transactionID) {
		return Output{}, executeError(result.CodeValidation, "Transaction ID is invalid")
	}
	value, err := reads.GetCommittedChange(ctx, transactionID, databaseID)
	if err != nil {
		return Output{}, normalizeChangeReadError(err)
	}
	entries := scopedChangeEntries(value.Entries, databaseID)
	offset, cursor := uint64(0), ""
	if show.Cursor != nil {
		cursor, err = relationshipString(show.Cursor, catalog.Table{}, bound, "Change cursor")
		if err != nil || cursor == "" || len(cursor) > changeCursorMaximum {
			return Output{}, executeError(result.CodeValidation, "Change cursor is invalid")
		}
		decoded, decodeErr := decodeChangeCursor(cursor)
		if decodeErr != nil || decoded.Kind != changeEntriesKind || decoded.Scope != scope ||
			decoded.TransactionID != value.TransactionID || decoded.EnvelopeChecksum != value.Checksum ||
			decoded.Snapshot != value.CommitSequence {
			return Output{}, executeError(result.CodeValidation, "Change cursor is invalid for this transaction")
		}
		offset = decoded.Offset
		if offset >= uint64(len(entries)) {
			return Output{}, executeError(result.CodeValidation, "Change cursor offset is invalid")
		}
	}
	end := min(uint64(len(entries)), offset+limit)
	more := end < uint64(len(entries))
	next := ""
	if more {
		next, err = encodeChangeCursor(changeCursorCore{
			Version: changeCursorVersion, Kind: changeEntriesKind, Scope: scope,
			Snapshot: value.CommitSequence, TransactionID: value.TransactionID,
			EnvelopeChecksum: value.Checksum, Offset: end,
		})
		if err != nil {
			return Output{}, executeError(result.CodeInternal, "Change cursor could not be encoded")
		}
	}
	output := Output{
		Columns: changeEntryColumns(), Rows: make([]result.Row, 0, end-offset),
		Truncated: more, NextCursor: next,
		Page: &result.ListPage{
			Version: result.ListPageVersion, Limit: limit, Cursor: cursor,
			Snapshot:  changeSnapshot(scope, value.CommitSequence, value.Checksum),
			Truncated: more, NextCursor: next,
		},
	}
	for _, entry := range entries[offset:end] {
		output.Rows = append(output.Rows, result.Row{
			"transaction_id": value.TransactionID, "commit_sequence": value.CommitSequence,
			"object_kind": string(entry.ObjectKind), "database_id": entry.DatabaseID,
			"table_id": entry.TableID, "object_id": entry.ObjectID,
			"operation": string(entry.Operation), "before_revision": entry.BeforeRevision,
			"after_revision": entry.AfterRevision, "schema_version": entry.SchemaVersion,
			"history_locator":    entry.HistoryLocator,
			"related_object_ids": append([]string{}, entry.RelatedObjectIDs...),
		})
	}
	return output, nil
}

func (engine *Engine) changeReadScope(
	ctx context.Context,
	show *ast.ShowStatement,
	kind string,
) (string, string, error) {
	if show.Database == nil {
		if _, present := security.AuthorizationFrom(ctx); present {
			return "", "", executeError(result.CodePermissionDenied, "scoped Authorization requires IN DATABASE for committed changes")
		}
		return "", changeScope(kind, ""), nil
	}
	if len(show.Database.Parts) != 1 || engine.catalog == nil {
		return "", "", executeError(result.CodeValidation, "committed change Database must be one Database name")
	}
	database, err := engine.catalog.DescribeDatabase(ctx, show.Database.Parts[0].Value)
	if err != nil {
		return "", "", normalizeError(err)
	}
	return database.ID, changeScope(kind, database.ID), nil
}

func changeReadLimit(expression *ast.Expression, bound bindings) (uint64, error) {
	limit, err := historyPositiveInteger(expression, catalog.Table{}, bound, "Committed change LIMIT")
	if err != nil {
		return 0, err
	}
	if limit > 256 {
		return 0, executeError(result.CodeValidation, "Committed change LIMIT must be between 1 and 256")
	}
	return limit, nil
}

func changeNonnegativeInteger(expression *ast.Expression, bound bindings, label string) (uint64, error) {
	if expression == nil || containsIdentifier(expression) {
		return 0, executeError(result.CodeValidation, label+" must be a literal or parameter")
	}
	value, err := evaluate(expression, catalog.Table{}, nil, bound)
	if err != nil {
		return 0, err
	}
	integer, ok := integerValue(value)
	if !ok || integer < 0 {
		return 0, executeError(result.CodeValidation, label+" must be a nonnegative INTEGER")
	}
	return uint64(integer), nil
}

func validCommittedTransactionID(value string) bool {
	if len(value) != 36 || !strings.HasPrefix(value, "txn_") || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value[4:])
	return err == nil && len(decoded) == 16
}

func scopedChangeEntries(entries []change.Entry, databaseID string) []change.Entry {
	if databaseID == "" {
		return entries
	}
	result := make([]change.Entry, 0, len(entries))
	for _, entry := range entries {
		if entry.DatabaseID == databaseID {
			result = append(result, entry)
		}
	}
	return result
}

func normalizeChangeReadError(err error) error {
	switch {
	case errors.Is(err, changeindex.ErrNotFound):
		return executeError(result.CodeNotFound, "committed change was not found")
	case errors.Is(err, changeindex.ErrInvalid):
		return executeError(result.CodeValidation, "committed change request is invalid")
	default:
		return normalizeError(err)
	}
}

func changeTimelineColumns() []result.Column {
	return []result.Column{
		{Name: "version", Type: "TEXT"}, {Name: "transaction_id", Type: "ID"},
		{Name: "commit_sequence", Type: "INTEGER"}, {Name: "committed_at", Type: "TIMESTAMP"},
		{Name: "actor", Type: "TEXT"}, {Name: "source", Type: "TEXT"},
		{Name: "reason", Type: "TEXT"}, {Name: "source_receipt_id", Type: "TEXT", Nullable: true},
		{Name: "database_ids", Type: "ID_LIST"}, {Name: "entry_count", Type: "INTEGER"},
		{Name: "checksum", Type: "TEXT"},
	}
}

func changeEntryColumns() []result.Column {
	return []result.Column{
		{Name: "transaction_id", Type: "ID"}, {Name: "commit_sequence", Type: "INTEGER"},
		{Name: "object_kind", Type: "TEXT"}, {Name: "database_id", Type: "ID", Nullable: true},
		{Name: "table_id", Type: "ID", Nullable: true}, {Name: "object_id", Type: "ID"},
		{Name: "operation", Type: "TEXT"}, {Name: "before_revision", Type: "INTEGER", Nullable: true},
		{Name: "after_revision", Type: "INTEGER"}, {Name: "schema_version", Type: "INTEGER", Nullable: true},
		{Name: "history_locator", Type: "ID", Nullable: true},
		{Name: "related_object_ids", Type: "ID_LIST"},
	}
}
