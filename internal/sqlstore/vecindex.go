package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/HW-Yue/Memora/internal/result"
)

// The vector index is derived: the bytes live on the unit row, and a vec0 table
// per (Database, Table) is a nearest-neighbour index over them, keyed by
// unit_no. Two consequences shape this file:
//
//   - An index is created the first time the scope accepts a vector, because
//     until then there is no committed dimension to declare (float[N] is fixed
//     when the table is created).
//   - Every write that removes a unit removes its index row in the same
//     transaction. Recall returns no scores, so a stale index row would point
//     at a position that no longer exists and nothing in the answer would show
//     it.
func vecIndexName(tableID string) string { return "mem_recall_vec_" + tableID }

// storeVector writes one unit's bytes into its Table's index, creating the index
// if this is the scope's first vector.
func (t *tx) storeVector(ctx context.Context, databaseID, tableID string, unitNo int64, vector []byte) error {
	identity, err := t.vectorIdentity(ctx, databaseID)
	if err != nil {
		return err
	}
	if identity.Model == "" {
		return fail(result.CodeInternal, "a vector was accepted before the database identity was locked")
	}
	known, _, err := t.vecIndexMetadata(ctx, tableID)
	if err != nil {
		return err
	}
	if !known {
		statement := fmt.Sprintf(`CREATE VIRTUAL TABLE %s USING vec0(embedding float[%d])`,
			vecIndexName(tableID), identity.Dimensions)
		if _, err := t.q().ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("create vector index for %s: %w", tableID, err)
		}
		if _, err := t.q().ExecContext(ctx, `INSERT INTO mem_recall_vec_indexes
			(table_id, database_id, model, dimensions, created_at) VALUES (?, ?, ?, ?, ?)`,
			tableID, databaseID, identity.Model, identity.Dimensions, formatTime(t.now)); err != nil {
			return err
		}
	}
	// vec0 has no upsert, so the old row goes first. Both statements are in the
	// caller's transaction, so no reader sees the gap.
	name := vecIndexName(tableID)
	if _, err := t.q().ExecContext(ctx, `DELETE FROM `+name+` WHERE rowid = ?`, unitNo); err != nil {
		return err
	}
	_, err = t.q().ExecContext(ctx, `INSERT INTO `+name+`(rowid, embedding) VALUES (?, ?)`, unitNo, vector)
	return err
}

// removeVectors drops index rows for units that no longer exist. Scopes without
// an index are skipped rather than created: a table with no vectors has nothing
// to remove.
func (t *tx) removeVectors(ctx context.Context, units []unitRef) error {
	for _, unit := range units {
		known, _, err := t.vecIndexMetadata(ctx, unit.tableID)
		if err != nil {
			return err
		}
		if !known {
			continue
		}
		if _, err := t.q().ExecContext(ctx,
			`DELETE FROM `+vecIndexName(unit.tableID)+` WHERE rowid = ?`, unit.unitNo); err != nil {
			return err
		}
	}
	return nil
}

// vecIndexMetadata reports whether a Table has an index and how wide it is.
func (t *tx) vecIndexMetadata(ctx context.Context, tableID string) (bool, int, error) {
	var dimensions int
	err := t.q().QueryRowContext(ctx, `SELECT dimensions FROM mem_recall_vec_indexes WHERE table_id = ?`,
		tableID).Scan(&dimensions)
	if errors.Is(err, sql.ErrNoRows) {
		return false, 0, nil
	}
	if err != nil {
		return false, 0, err
	}
	return true, dimensions, nil
}

// unitRef names one unit's place: every Database's units share one table, so a
// unit is only identified by its Table together with its number.
type unitRef struct {
	tableID string
	unitNo  int64
}
