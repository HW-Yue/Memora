package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/recall"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
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
		return engine.recallUnion(ctx, statement, bound, databaseName, tableName, limit)
	case statement.Vector != nil:
		return engine.recallNearest(ctx, statement, bound, databaseName, tableName, limit)
	}
	return engine.recallKeywords(ctx, statement, bound, databaseName, tableName, limit)
}

// recallUnion answers with both arms and merges what they found.
//
// There is nothing to weigh the two against each other: recall returns no
// scores, so a merge has no common scale to rank on, and inventing one would
// make relevance a number the contract deliberately withholds. So each arm
// brings back its own candidates (how many is its business), the union is
// de-duplicated by position, and LIMIT truncates the listing — exactly what
// LIMIT means for a single arm, which is what keeps one statement's LIMIT from
// meaning two different things.
func (engine *Engine) recallUnion(ctx context.Context, statement *ast.RecallStatement, bound bindings, databaseName, tableName string, limit uint64) (Output, error) {
	keywordHits, err := engine.recallKeywords(ctx, statement, bound, databaseName, tableName, limit)
	if err != nil {
		return Output{}, err
	}
	// A vector arm that cannot answer at all (no identity, or a query that does
	// not decode) must not silently degrade the answer to keywords only: the
	// caller asked for both, and half an answer that looks whole is the one
	// outcome recall must never produce.
	vectorHits, err := engine.recallNearest(ctx, statement, bound, databaseName, tableName, limit)
	if err != nil {
		return Output{}, err
	}
	merged := mergeRecallRows(keywordHits.Rows, vectorHits.Rows)
	merged = merged[:min(len(merged), int(limit))]
	output := keywordHits
	output.Rows = merged
	output.Truncated = len(merged) == int(limit)
	return output, nil
}

func rowText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.RawMessage:
		return string(typed)
	}
	return ""
}

// mergeRecallRows unions two listings by position and returns them in the stable
// order both arms already promise: Table, then path. Two arms finding the same
// position is one position.
func mergeRecallRows(left, right []result.Row) []result.Row {
	seen := map[string]bool{}
	merged := make([]result.Row, 0, len(left)+len(right))
	for _, rows := range [][]result.Row{left, right} {
		for _, row := range rows {
			key := rowText(row["table"]) + "|" + rowText(row["path"])
			if seen[key] {
				continue
			}
			seen[key] = true
			merged = append(merged, row)
		}
	}
	sort.Slice(merged, func(first, second int) bool {
		leftTable, rightTable := rowText(merged[first]["table"]), rowText(merged[second]["table"])
		if leftTable != rightTable {
			return leftTable < rightTable
		}
		return rowText(merged[first]["path"]) < rowText(merged[second]["path"])
	})
	return merged
}

// showPendingVectors hands a host the work list: which units still need a
// vector, and the text each one must be embedded from. It is a read — it reports
// work, it does not do any, and it never calls a provider itself.
func (engine *Engine) showPendingVectors(ctx context.Context, statement ast.Statement, bound bindings) (Output, error) {
	if statement.Show == nil || statement.Show.Database == nil || statement.Show.Limit == nil {
		return Output{}, executeError(result.CodeValidation, "SHOW PENDING VECTORS needs IN DATABASE and LIMIT")
	}
	if len(statement.Show.Database.Parts) != 1 {
		return Output{}, executeError(result.CodeValidation,
			"SHOW PENDING VECTORS takes a Database name, not a dotted name")
	}
	databaseName := statement.Show.Database.Parts[0].Value
	if err := engine.authorizeDatabaseReference(ctx, databaseName); err != nil {
		return Output{}, err
	}
	limit, err := historyPositiveInteger(statement.Show.Limit, catalog.Table{}, bound, "SHOW PENDING VECTORS LIMIT")
	if err != nil {
		return Output{}, err
	}
	if limit > maxQueryScan {
		return Output{}, executeError(result.CodeValidation,
			"SHOW PENDING VECTORS LIMIT must be between 1 and 1000")
	}
	units, err := engine.rows.PendingVectors(ctx, databaseName, int(limit))
	if err != nil {
		return Output{}, normalizeError(err)
	}
	output := Output{
		Columns: []result.Column{
			{Name: "unit_no", Type: "INTEGER"},
			{Name: "table", Type: "TEXT"},
			{Name: "content_hash", Type: "TEXT"},
			{Name: "payload", Type: "TEXT"},
		},
		Rows:      make([]result.Row, 0, len(units)),
		Truncated: len(units) == int(limit),
	}
	for _, unit := range units {
		output.Rows = append(output.Rows, result.Row{
			"unit_no": unit.UnitNo, "table": unit.Table,
			"content_hash": unit.ContentHash, "payload": unit.Payload,
		})
	}
	return output, nil
}

// acceptVector records one host-computed embedding for one unit. It is a write,
// and it is the entry a client drains its backlog through: the language has no
// array type, so one statement carries one embedding and a client that has many
// sends a batch of statements.
//
// The engine does not compute vectors and does not judge them; it only refuses
// to attach an embedding that does not describe what the unit currently holds.
func (engine *Engine) acceptVector(ctx context.Context, statement *ast.AcceptVectorStatement, bound bindings) (Output, error) {
	if statement == nil || statement.Values == nil || statement.Unit == nil || statement.Database == nil ||
		statement.Model == nil || statement.ContentHash == nil {
		return Output{}, executeError(result.CodeValidation,
			"ACCEPT VECTOR needs values, FOR UNIT, IN DATABASE, MODEL and HASH")
	}
	if len(statement.Database.Parts) != 1 {
		return Output{}, executeError(result.CodeValidation, "ACCEPT VECTOR takes a Database name, not a dotted name")
	}
	databaseName := statement.Database.Parts[0].Value
	if err := engine.authorizeDatabaseReference(ctx, databaseName); err != nil {
		return Output{}, err
	}
	unitNo, err := historyPositiveInteger(statement.Unit, catalog.Table{}, bound, "ACCEPT VECTOR unit number")
	if err != nil {
		return Output{}, err
	}
	model, err := relationshipString(statement.Model, catalog.Table{}, bound, "ACCEPT VECTOR model")
	if err != nil {
		return Output{}, err
	}
	contentHash, err := relationshipString(statement.ContentHash, catalog.Table{}, bound, "ACCEPT VECTOR content hash")
	if err != nil {
		return Output{}, err
	}
	if statement.Values == nil || containsIdentifier(statement.Values) {
		return Output{}, executeError(result.CodeValidation, "ACCEPT VECTOR values must be a literal or parameter")
	}
	value, err := evaluate(statement.Values, catalog.Table{}, nil, bound)
	if err != nil {
		return Output{}, err
	}
	text, ok := value.(string)
	if !ok {
		return Output{}, executeError(result.CodeValidation,
			"ACCEPT VECTOR values must be "+recall.VectorEncoding+" as TEXT")
	}
	vector, err := recall.DecodeVector(text)
	if err != nil {
		return Output{}, executeError(result.CodeValidation, err.Error())
	}
	identity, err := engine.rows.AcceptVector(ctx, databaseName, recall.VectorRecord{
		UnitNo: int64(unitNo), ContentHash: contentHash, Model: model,
		Dimensions: len(vector), Vector: vector,
	})
	if err != nil {
		return Output{}, normalizeError(err)
	}
	return Output{
		Columns: []result.Column{
			{Name: "unit_no", Type: "INTEGER"},
			{Name: "model", Type: "TEXT"},
			{Name: "dimensions", Type: "INTEGER"},
		},
		Rows: []result.Row{{
			"unit_no": unitNo, "model": identity.Model, "dimensions": identity.Dimensions,
		}},
		AffectedRows: 1,
	}, nil
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

// writeVector turns an offered embedding into the write option the store takes.
// The wire form is decoded here rather than in the store: it is a language
// concern, and a malformed one is the caller's mistake to hear about before
// anything is written.
func writeVector(input *VectorInput) (*row.Vector, error) {
	if input == nil {
		return nil, nil
	}
	if input.Model == "" || input.ContentHash == "" {
		return nil, executeError(result.CodeValidation,
			"an attached vector needs MODEL and content_hash, so the engine can check what it describes")
	}
	values, err := recall.DecodeVector(input.Values)
	if err != nil {
		return nil, executeError(result.CodeValidation, err.Error())
	}
	return &row.Vector{Model: input.Model, ContentHash: input.ContentHash, Values: values}, nil
}

// warnIfVectorDidNotLand says so when a write offered an embedding and that
// Row's unit still has no usable vector afterwards.
//
// It answers for the Row the write just touched, not for the scope: an unrelated
// unit elsewhere in the same Table being not-ready is not news about this write,
// and a notice that fired for it would be noise a caller learns to ignore. The
// write is never failed for this — the Row is the fact and the vector is an
// index over it — but a caller that thinks its vector landed must be able to
// find out that it did not, because recall returns no scores to reveal it.
func (engine *Engine) warnIfVectorDidNotLand(ctx context.Context, output Output, attached *row.Vector, databaseName, tableName, rowID string) (Output, error) {
	if attached == nil {
		return output, nil
	}
	state, err := engine.rows.UnitVectorState(ctx, databaseName, tableName, rowID)
	if err != nil {
		return output, normalizeError(err)
	}
	if state.Ready {
		return output, nil
	}
	details := map[string]any{
		"database": databaseName, "table": tableName, "row_id": rowID,
	}
	reason, message := "not_accepted", "the attached vector was not accepted"
	switch {
	case !state.HasUnit:
		reason = "no_unit"
		message = "the Row has no recallable unit, so the attached vector had nothing to describe"
	case state.ContentHash != attached.ContentHash:
		details["unit_no"] = state.UnitNo
		reason = "text_changed"
		message = "the attached vector was computed for different text than the Row holds"
	case state.Model != "" && (state.Model != attached.Model || state.Dimensions != len(attached.Values)):
		details["unit_no"] = state.UnitNo
		details["locked_model"] = state.Model
		details["locked_dimensions"] = state.Dimensions
		reason = "identity_mismatch"
		message = "the attached vector does not match the model and width this Database is locked to"
	default:
		details["unit_no"] = state.UnitNo
	}
	details["reason"] = reason
	output.Warnings = append(output.Warnings, result.Notice{
		Code: result.CodeVectorsNotReady, Message: message, Details: details,
	})
	return output, nil
}
