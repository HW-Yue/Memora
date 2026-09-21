package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/recall"
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
			Snapshot:  page.Snapshot,
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

// repairVectorIndex reconciles a Database's derived vector indexes with the
// truth on its unit rows. It is a write with the same bound as REPAIR LINKS:
// the statement's LIMIT is how much one pass may repair, and the caller's
// declared ceiling has to cover it. Running it until `remaining` is zero is
// safe — each pass re-reads the truth, so nothing is applied twice.
func (engine *Engine) repairVectorIndex(ctx context.Context, statement *ast.RepairVectorStatement, bound bindings, options MutationOptions) (Output, error) {
	if statement == nil || statement.Database == nil || statement.Limit == nil {
		return Output{}, executeError(result.CodeValidation, "REPAIR VECTOR needs INDEX IN DATABASE and LIMIT")
	}
	if options.MaxAffectedRows == 0 {
		return Output{}, executeError(result.CodeValidation, "REPAIR VECTOR requires max_affected_rows")
	}
	limit, err := historyPositiveInteger(statement.Limit, catalog.Table{}, bound, "REPAIR VECTOR LIMIT")
	if err != nil {
		return Output{}, err
	}
	if limit > maxQueryScan {
		return Output{}, executeError(result.CodeValidation, "REPAIR VECTOR LIMIT must be between 1 and 1000")
	}
	if options.MaxAffectedRows < uint64(limit) {
		return Output{}, executeError(result.CodeValidation,
			fmt.Sprintf("REPAIR VECTOR LIMIT %d exceeds max_affected_rows %d", limit, options.MaxAffectedRows))
	}
	if len(statement.Database.Parts) != 1 {
		return Output{}, executeError(result.CodeValidation, "REPAIR VECTOR takes a Database name, not a dotted name")
	}
	databaseName := statement.Database.Parts[0].Value
	if err := engine.authorizeDatabaseReference(ctx, databaseName); err != nil {
		return Output{}, err
	}
	receipt, err := engine.rows.RepairVectorIndex(ctx, databaseName, int(limit))
	if err != nil {
		return Output{}, normalizeError(err)
	}
	// Two aggregate numbers, no per-Table breakdown: recall answers with paths
	// and no scores, so publishing how many units each Table is missing would
	// hand out exactly the kind of signal the recall contract withholds.
	return Output{
		Columns: []result.Column{
			{Name: "repaired", Type: "INTEGER"},
			{Name: "remaining", Type: "INTEGER"},
		},
		Rows: []result.Row{{
			"repaired": receipt.Repaired, "remaining": receipt.Remaining,
		}},
		AffectedRows: uint64(receipt.Repaired),
	}, nil
}

// recall answers RECALL in either of its arms. Both name one intent — where does
// this sit in the tree — and answer with the same shape, so they share one
// statement, one permission check, one bound, one stable ordering. What the
// answer never carries is just as much of the contract: no score, no distance,
// no reason, no matched field and no content, so a caller can only navigate.
func (engine *Engine) recall(ctx context.Context, statement *ast.RecallStatement, bound bindings) (Output, error) {
	if statement == nil || statement.Database == nil || statement.Limit == nil {
		return Output{}, executeError(result.CodeValidation, "RECALL needs FROM, an arm and LIMIT")
	}
	if len(statement.Database.Parts) != 1 || statement.Table != nil && len(statement.Table.Parts) != 1 {
		return Output{}, executeError(result.CodeValidation, "RECALL takes a Database name and an optional Table name")
	}
	databaseName := statement.Database.Parts[0].Value
	tableName := ""
	if statement.Table != nil {
		tableName = statement.Table.Parts[0].Value
	}
	if err := engine.authorizeDatabaseReference(ctx, databaseName); err != nil {
		return Output{}, err
	}
	limit, err := historyPositiveInteger(statement.Limit, catalog.Table{}, bound, "RECALL LIMIT")
	if err != nil {
		return Output{}, err
	}
	if limit > maxQueryScan {
		return Output{}, executeError(result.CodeValidation, "RECALL LIMIT must be between 1 and 1000")
	}
	switch {
	case statement.Query != nil && statement.Vector != nil:
		// The union is specified (LIMIT truncates the merged, de-duplicated
		// output; the two arms' internal candidate counts are implementation
		// detail) but not implemented until the fusion step lands. Refusing is
		// honest; answering from one arm while ignoring the other is not.
		return Output{}, executeError(result.CodeUnsupported,
			"RECALL with both MATCH and NEAREST is not implemented yet")
	case statement.Vector != nil:
		return engine.recallNearest(ctx, statement, bound, databaseName, tableName, limit)
	}
	return engine.recallKeywords(ctx, statement, bound, databaseName, tableName, limit)
}

// recallVectorQuery decodes the NEAREST arm: base64 of little-endian float32,
// the one wire form a vector has. The contract is fixed rather than left to the
// implementation because a wrong decode yields a *valid* vector — it would come
// back with valid paths for the wrong places and nothing in the answer to show
// it. Dimensions are checked against the Database's identity in the store.
func recallVectorQuery(value any) ([]float32, error) {
	text, ok := value.(string)
	if !ok || text == "" {
		return nil, executeError(result.CodeValidation, "RECALL NEAREST needs the query vector as base64 TEXT")
	}
	query, err := recall.DecodeVector(text)
	if err != nil {
		return nil, executeError(result.CodeValidation, err.Error())
	}
	return query, nil
}

func (engine *Engine) recallNearest(ctx context.Context, statement *ast.RecallStatement, bound bindings, databaseName, tableName string, limit uint64) (Output, error) {
	if statement.Vector == nil || containsIdentifier(statement.Vector) {
		return Output{}, executeError(result.CodeValidation, "RECALL vector must be a literal or parameter")
	}
	value, err := evaluate(statement.Vector, catalog.Table{}, nil, bound)
	if err != nil {
		return Output{}, err
	}
	query, err := recallVectorQuery(value)
	if err != nil {
		return Output{}, err
	}
	hits, err := engine.rows.RecallNearest(ctx, databaseName, tableName, query, int(limit))
	if err != nil {
		return Output{}, normalizeError(err)
	}
	return engine.recallOutput(ctx, hits, databaseName, tableName, limit)
}

func (engine *Engine) recallKeywords(ctx context.Context, statement *ast.RecallStatement, bound bindings, databaseName, tableName string, limit uint64) (Output, error) {
	text, err := relationshipString(statement.Query, catalog.Table{}, bound, "RECALL query")
	if err != nil {
		return Output{}, err
	}
	text = strings.TrimSpace(text)
	// The trigram tokenizer cannot answer anything shorter than three
	// characters: it would return an empty list that reads exactly like "not
	// found". Recall does not explain itself, so the statement refuses instead.
	if utf8.RuneCountInString(text) < recallMinimumQueryRunes {
		return Output{}, executeError(result.CodeValidation,
			fmt.Sprintf("RECALL needs at least %d characters: shorter queries cannot be indexed",
				recallMinimumQueryRunes))
	}
	hits, err := engine.rows.RecallKeywords(ctx, databaseName, tableName, text, int(limit))
	if err != nil {
		return Output{}, normalizeError(err)
	}
	return engine.recallOutput(ctx, hits, databaseName, tableName, limit)
}

// recallOutput is the one place a recall answer is shaped, so both arms keep the
// same columns, the same stable order and the same notice.
func (engine *Engine) recallOutput(ctx context.Context, hits []recall.Hit, databaseName, tableName string, limit uint64) (Output, error) {
	output := Output{
		Columns: []result.Column{
			{Name: "database", Type: "TEXT"},
			{Name: "table", Type: "TEXT"},
			{Name: "path", Type: "JSON"},
			{Name: "kind", Type: "TEXT"},
			{Name: "object_id", Type: "ID", Nullable: true},
		},
		Rows: make([]result.Row, 0, len(hits)),
	}
	for _, hit := range hits {
		path, err := json.Marshal(hit.Path)
		if err != nil {
			return Output{}, executeError(result.CodeInternal, "recalled path could not be encoded")
		}
		object := any(nil)
		if hit.ObjectID != "" {
			object = hit.ObjectID
		}
		output.Rows = append(output.Rows, result.Row{
			"database": hit.Database, "table": hit.Table,
			"path": json.RawMessage(path), "kind": hit.Kind, "object_id": object,
		})
	}
	output.Truncated = len(hits) == int(limit)

	// Recall answers with the paths it found, and a result that silently covered
	// only part of the scope would read exactly like a complete one — there are
	// no scores to hint that something is missing. So the count of units this
	// answer could not draw on travels with the answer itself, aggregated by
	// scope rather than listed per unit.
	status, err := engine.rows.VectorStatus(ctx, databaseName, tableName)
	if err != nil {
		return Output{}, normalizeError(err)
	}
	if status.NotReady > 0 {
		details := map[string]any{
			"database": databaseName, "not_ready_units": status.NotReady,
			"identity_locked": status.IdentityLocked,
		}
		if tableName != "" {
			details["table"] = tableName
		}
		if status.IdentityLocked {
			details["embedding_model"] = status.Model
			details["embedding_dimensions"] = status.Dimensions
		}
		output.Warnings = append(output.Warnings, result.Notice{
			Code: result.CodeVectorsNotReady,
			Message: "the vector path could not answer for " +
				strconv.Itoa(status.NotReady) + " unit(s) in this scope, so this result may be missing matches",
			Details: details,
		})
	}
	return output, nil
}

// recallMinimumQueryRunes is the shortest query the trigram tokenizer can serve.
const recallMinimumQueryRunes = 3
