package executor

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/result"
)

// The archive is where a deleted Row still exists, and it is the one read face
// query-model §7 exempts from "a deleted Row is unreachable". Two statements
// reach it and nothing else does: SHOW ARCHIVE lists metadata under a required
// Table scope and a bounded page — an unconditional listing would turn it into
// a soft-delete browser — and OPEN ARCHIVE hands over one record in full, which
// is where an Agent rebuilds from.

func (engine *Engine) showArchive(ctx context.Context, statement ast.Statement, bound bindings) (Output, error) {
	show := statement.Show
	if show == nil || show.Table == nil || show.Limit == nil {
		return Output{}, executeError(result.CodeValidation, "SHOW ARCHIVE needs FROM TABLE and LIMIT")
	}
	databaseName, tableName, table, err := engine.bindTable(ctx, *show.Table)
	if err != nil {
		return Output{}, err
	}
	limit, err := historyPositiveInteger(show.Limit, table, bound, "SHOW ARCHIVE LIMIT")
	if err != nil {
		return Output{}, err
	}
	if limit > maxQueryScan {
		return Output{}, executeError(result.CodeValidation, "SHOW ARCHIVE LIMIT must be between 1 and 1000")
	}
	rowID := ""
	if show.Row != nil {
		if rowID, err = historyRowID(show.Row, table, bound); err != nil {
			return Output{}, err
		}
	}
	cursor := ""
	if show.Cursor != nil {
		if cursor, err = relationshipString(show.Cursor, table, bound, "Archive cursor"); err != nil {
			return Output{}, err
		}
		if cursor == "" {
			return Output{}, executeError(result.CodeValidation, "Archive cursor must be non-empty TEXT")
		}
	}
	summaries, page, err := engine.rows.ArchivePage(ctx, databaseName, tableName, rowID, cursor, int(limit))
	if err != nil {
		return Output{}, normalizeError(err)
	}
	output := Output{
		Columns: []result.Column{
			{Name: "archive_id", Type: "ID"},
			{Name: "row_id", Type: "ID"},
			{Name: "revision", Type: "INTEGER"},
			{Name: "deleted_at", Type: "TIMESTAMP"},
			{Name: "actor", Type: "TEXT"},
			{Name: "source", Type: "TEXT"},
			{Name: "reason", Type: "TEXT"},
			{Name: "path", Type: "TEXT"},
		},
		Rows:      make([]result.Row, 0, len(summaries)),
		Truncated: page.Truncated, NextCursor: page.NextCursor,
		Page: &result.ListPage{
			Version: result.ListPageVersion, Limit: limit, Cursor: cursor,
			Truncated: page.Truncated, NextCursor: page.NextCursor,
		},
	}
	for _, summary := range summaries {
		output.Rows = append(output.Rows, result.Row{
			"archive_id": summary.ArchiveID, "row_id": summary.RowID, "revision": summary.Revision,
			"deleted_at": summary.DeletedAt, "actor": summary.Actor, "source": summary.Source,
			"reason": summary.Reason, "path": summary.Path,
		})
	}
	return output, nil
}

func (engine *Engine) openArchive(ctx context.Context, open *ast.OpenArchiveStatement, bound bindings) (Output, error) {
	if open == nil || open.Archive == nil {
		return Output{}, executeError(result.CodeValidation, "OPEN ARCHIVE needs an archive ID")
	}
	archiveID, err := relationshipString(open.Archive, catalog.Table{}, bound, "Archive ID")
	if err != nil {
		return Output{}, err
	}
	record, err := engine.rows.ArchiveRecord(ctx, archiveID)
	if err != nil {
		return Output{}, normalizeError(err)
	}
	// The record names its own Database, so authorization follows the record
	// rather than whatever the caller happened to have in scope.
	if err := engine.authorizeDatabaseReference(ctx, record.Summary.DatabaseID); err != nil {
		return Output{}, err
	}
	path, err := json.Marshal(record.Path)
	if err != nil {
		return Output{}, executeError(result.CodeInternal, "archived path could not be encoded")
	}
	content, err := json.Marshal(record.Row)
	if err != nil {
		return Output{}, executeError(result.CodeInternal, "archived Row could not be encoded")
	}
	return Output{
		Columns: []result.Column{
			{Name: "archive_id", Type: "ID"},
			{Name: "row_id", Type: "ID"},
			{Name: "revision", Type: "INTEGER"},
			{Name: "deleted_at", Type: "TIMESTAMP"},
			{Name: "actor", Type: "TEXT"},
			{Name: "source", Type: "TEXT"},
			{Name: "reason", Type: "TEXT"},
			{Name: "path", Type: "JSON"},
			{Name: "row", Type: "JSON"},
		},
		Rows: []result.Row{{
			"archive_id": record.Summary.ArchiveID, "row_id": record.Summary.RowID,
			"revision": record.Summary.Revision, "deleted_at": record.Summary.DeletedAt,
			"actor": record.Summary.Actor, "source": record.Summary.Source,
			"reason": record.Summary.Reason,
			"path":   json.RawMessage(path), "row": json.RawMessage(content),
		}},
	}, nil
}

// repairLinks drains a bounded batch of queued link repairs. It is a write: the
// batch bound is the statement's own LIMIT, and the caller's declared ceiling
// has to cover it, because one endpoint can touch several Rows.
func (engine *Engine) repairLinks(ctx context.Context, statement *ast.RepairLinksStatement, bound bindings, options MutationOptions) (Output, error) {
	if statement == nil || statement.Database == nil || statement.Limit == nil {
		return Output{}, executeError(result.CodeValidation, "REPAIR LINKS needs IN DATABASE and LIMIT")
	}
	if options.MaxAffectedRows == 0 {
		return Output{}, executeError(result.CodeValidation, "REPAIR LINKS requires max_affected_rows")
	}
	limit, err := historyPositiveInteger(statement.Limit, catalog.Table{}, bound, "REPAIR LINKS LIMIT")
	if err != nil {
		return Output{}, err
	}
	if limit > maxQueryScan {
		return Output{}, executeError(result.CodeValidation, "REPAIR LINKS LIMIT must be between 1 and 1000")
	}
	if options.MaxAffectedRows < uint64(limit) {
		return Output{}, executeError(result.CodeValidation,
			fmt.Sprintf("REPAIR LINKS LIMIT %d exceeds max_affected_rows %d", limit, options.MaxAffectedRows))
	}
	// The statement names one Database; a dotted name is not accepted, because a
	// repair pass is scoped to the Database whose queue it drains.
	if len(statement.Database.Parts) != 1 {
		return Output{}, executeError(result.CodeValidation, "REPAIR LINKS takes a Database name, not a dotted name")
	}
	databaseName := statement.Database.Parts[0].Value
	if err := engine.authorizeDatabaseReference(ctx, databaseName); err != nil {
		return Output{}, err
	}
	receipt, err := engine.rows.RepairLinks(ctx, databaseName, int(limit))
	if err != nil {
		return Output{}, normalizeError(err)
	}
	return Output{
		Columns: []result.Column{
			{Name: "repaired", Type: "INTEGER"},
			{Name: "discarded", Type: "INTEGER"},
			{Name: "remaining", Type: "INTEGER"},
		},
		Rows: []result.Row{{
			"repaired": receipt.Repaired, "discarded": receipt.Discarded, "remaining": receipt.Remaining,
		}},
		AffectedRows: uint64(receipt.Repaired),
	}, nil
}
