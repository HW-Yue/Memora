package sqlstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/HW-Yue/Memora/internal/catalog"
	rowmodel "github.com/HW-Yue/Memora/internal/row"
)

// A recall unit is one live Row as the recall paths see it: the leaf it hangs
// under, the text both predictors read, and a hash of that text. Keyword and
// vector recall share this one source of truth, so "what can be recalled" is
// decided once, not twice — see docs/planning/m7-recall-plan.md.
//
// The unit is keyed by the leaf because the leaf is what recall returns: the
// semantic path ends there, and one Row occupies exactly one leaf, so a unit is
// a position and not merely a Row.

// recallPayload is the text a unit recalls by: the Row's title and summary when
// it has them, otherwise every text value it holds. Both predictors read this,
// so what a phrase matches is decided here and nowhere else.
func recallPayload(table catalog.Table, value storedRow) string {
	parts := []string{}
	byRole := func(role string) {
		for _, column := range table.Columns {
			if column.Archived() || column.SemanticRole != role {
				continue
			}
			if text, ok := value.Values[column.ID].(string); ok && strings.TrimSpace(text) != "" {
				parts = append(parts, text)
			}
		}
	}
	byRole("title")
	byRole("summary")
	if len(parts) == 0 {
		for _, column := range table.Columns {
			if column.Archived() {
				continue
			}
			if text, ok := value.Values[column.ID].(string); ok && strings.TrimSpace(text) != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

// foldRecallText is what the keyword index stores: full-width ASCII folded to
// half-width, so "ＡＢＣ" and "ABC" meet. Case is left to the tokenizer, which
// already folds it. The stored payload keeps the text as written — the vector
// path embeds that, not this.
func foldRecallText(text string) string {
	return strings.Map(func(character rune) rune {
		if character >= '\uFF01' && character <= '\uFF5E' {
			return character - 0xFEE0
		}
		if character == '\u3000' {
			return ' '
		}
		return character
	}, text)
}

func recallContentHash(payload string) string {
	digest := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(digest[:])
}

// syncRecallUnit makes the unit for a Row's leaf match the Row. When the text is
// unchanged the embedding provenance is kept: a vector describes text, not a
// revision, so an edit to a column the payload does not cover must not throw the
// vector away.
func (t *tx) syncRecallUnit(ctx context.Context, table catalog.Table, value storedRow) error {
	if value.State != rowmodel.StateLive || len(value.RouteLeafIDs) != 1 {
		return t.removeRecallUnitsForRow(ctx, table, value.ID)
	}
	leafID := value.RouteLeafIDs[0]
	payload := recallPayload(table, value)
	hash := recallContentHash(payload)

	// A Row occupies exactly one leaf, so a unit for this Row on any other leaf
	// describes a position it has left. Removing it here makes the index agree
	// with the Rows however the move happened.
	if err := t.withdrawRecallUnits(ctx, `table_id = ? AND row_id = ? AND route_id <> ?`,
		table.ID, value.ID, leafID); err != nil {
		return err
	}
	if _, err := t.q().ExecContext(ctx, `DELETE FROM mem_recall_units
		WHERE table_id = ? AND row_id = ? AND route_id <> ?`, table.ID, value.ID, leafID); err != nil {
		return err
	}

	folded := foldRecallText(payload)
	var unitNo int64
	var existingRow, existingHash, existingIndex string
	err := t.q().QueryRowContext(ctx, `SELECT unit_no, row_id, content_hash, payload_index
		FROM mem_recall_units WHERE route_id = ?`, leafID).Scan(&unitNo, &existingRow, &existingHash, &existingIndex)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		inserted, err := t.q().ExecContext(ctx, `INSERT INTO mem_recall_units
			(route_id, database_id, table_id, row_id, revision, content_hash, payload, payload_index,
			 embedding_model, embedding_dimensions, embedded_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, '', 0, '', ?)`,
			leafID, table.DatabaseID, table.ID, value.ID, value.Revision, hash, payload, folded, formatTime(t.now))
		if err != nil {
			return err
		}
		if unitNo, err = inserted.LastInsertId(); err != nil {
			return err
		}
		_, err = t.q().ExecContext(ctx, `INSERT INTO mem_recall_fts(rowid, payload_index) VALUES (?, ?)`,
			unitNo, folded)
		return err
	case err != nil:
		return err
	}
	if existingRow == value.ID && existingHash == hash {
		_, err = t.q().ExecContext(ctx, `UPDATE mem_recall_units SET revision = ?, updated_at = ?
			WHERE route_id = ?`, value.Revision, formatTime(t.now), leafID)
		return err
	}
	// The text changed (or another Row took the leaf), so whatever vector was here
	// described something else and its provenance goes with it. The index is told
	// about the old text before the content row changes: an external-content index
	// deletes by value, not by rowid.
	if _, err := t.q().ExecContext(ctx,
		`INSERT INTO mem_recall_fts(mem_recall_fts, rowid, payload_index) VALUES('delete', ?, ?)`,
		unitNo, existingIndex); err != nil {
		return err
	}
	if _, err := t.q().ExecContext(ctx, `UPDATE mem_recall_units
		SET row_id = ?, revision = ?, content_hash = ?, payload = ?, payload_index = ?,
		    embedding_model = '', embedding_dimensions = 0, embedded_at = '', updated_at = ?
		WHERE route_id = ?`,
		value.ID, value.Revision, hash, payload, folded, formatTime(t.now), leafID); err != nil {
		return err
	}
	_, err = t.q().ExecContext(ctx, `INSERT INTO mem_recall_fts(rowid, payload_index) VALUES (?, ?)`,
		unitNo, folded)
	return err
}

// removeRecallUnitsForRow drops every unit a Row owns. A deleted or superseded
// Row is not recallable: recall answers where something is, and it is nowhere.
func (t *tx) removeRecallUnitsForRow(ctx context.Context, table catalog.Table, rowID string) error {
	if err := t.withdrawRecallUnits(ctx, `table_id = ? AND row_id = ?`, table.ID, rowID); err != nil {
		return err
	}
	_, err := t.q().ExecContext(ctx, `DELETE FROM mem_recall_units WHERE table_id = ? AND row_id = ?`,
		table.ID, rowID)
	return err
}

// withdrawRecallUnits removes the units matching a predicate from the keyword
// index. An external-content FTS5 index deletes by value, so the old text has to
// be read before the content row disappears.
func (t *tx) withdrawRecallUnits(ctx context.Context, where string, arguments ...any) error {
	rows, err := t.q().QueryContext(ctx, `SELECT unit_no, payload_index FROM mem_recall_units WHERE `+where, arguments...)
	if err != nil {
		return err
	}
	units := []struct {
		unitNo int64
		index  string
	}{}
	for rows.Next() {
		unit := struct {
			unitNo int64
			index  string
		}{}
		if err := rows.Scan(&unit.unitNo, &unit.index); err != nil {
			_ = rows.Close()
			return err
		}
		units = append(units, unit)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	for _, unit := range units {
		if _, err := t.q().ExecContext(ctx,
			`INSERT INTO mem_recall_fts(mem_recall_fts, rowid, payload_index) VALUES('delete', ?, ?)`,
			unit.unitNo, unit.index); err != nil {
			return err
		}
	}
	return nil
}

// brokenRecallUnits counts the two ways the materialised index can disagree with
// the Rows it indexes: a unit for a Row that is gone or no longer live, and a
// live Row with no unit. A stale payload is not counted — that is text waiting to
// be re-read, not a broken index.
func (t *tx) brokenRecallUnits(ctx context.Context, table catalog.Table) (int, error) {
	broken := 0
	err := t.q().QueryRowContext(ctx, `SELECT COUNT(*) FROM mem_recall_units u
		WHERE u.table_id = ?
		  AND NOT EXISTS (SELECT 1 FROM `+dataTable(table.ID)+` d
			WHERE d.row_id = u.row_id AND d.row_state = 'live')`, table.ID).Scan(&broken)
	if err != nil {
		return 0, err
	}
	var missing int
	err = t.q().QueryRowContext(ctx, `SELECT COUNT(*) FROM `+dataTable(table.ID)+` d
		WHERE d.row_state = 'live'
		  AND NOT EXISTS (SELECT 1 FROM mem_recall_units u
			WHERE u.table_id = ? AND u.row_id = d.row_id)`, table.ID).Scan(&missing)
	return broken + missing, err
}
