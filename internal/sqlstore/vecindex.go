package sqlstore

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/HW-Yue/Memora/internal/repair"
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
	ready, indexedDimensions, err := t.vecIndexReady(ctx, tableID)
	if err != nil {
		return err
	}
	if ready && indexedDimensions != identity.Dimensions {
		// The width is welded into the virtual table (float[N] is fixed at
		// CREATE), so an index built for another identity is not one this write
		// can use: inserting would either fail or, worse, succeed at the wrong
		// width. Rebuild it instead of trusting the registry row.
		if err := t.dropVectorIndex(ctx, tableID); err != nil {
			return err
		}
		ready = false
	}
	if !ready {
		statement := fmt.Sprintf(`CREATE VIRTUAL TABLE IF NOT EXISTS %s USING vec0(embedding float[%d])`,
			vecIndexName(tableID), identity.Dimensions)
		if _, err := t.q().ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("create vector index for %s: %w", tableID, err)
		}
		if _, err := t.q().ExecContext(ctx, `INSERT OR REPLACE INTO mem_recall_vec_indexes
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

// dropVectorIndexes removes every derived index one Database owns, registry row
// first: a crash between the two must leave a table nobody trusts rather than a
// registry row pointing at a table that is gone.
//
// A rekey drops them rather than emptying them because their width is welded
// into the declaration, and the identity a rekey moves to may be a different
// width.
func (t *tx) dropVectorIndexes(ctx context.Context, databaseID string) error {
	rows, err := t.q().QueryContext(ctx,
		`SELECT table_id FROM mem_recall_vec_indexes WHERE database_id = ?`, databaseID)
	if err != nil {
		return err
	}
	tableIDs := []string{}
	for rows.Next() {
		var tableID string
		if err := rows.Scan(&tableID); err != nil {
			_ = rows.Close()
			return err
		}
		tableIDs = append(tableIDs, tableID)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	for _, tableID := range tableIDs {
		if err := t.dropVectorIndex(ctx, tableID); err != nil {
			return err
		}
	}
	return nil
}

func (t *tx) dropVectorIndex(ctx context.Context, tableID string) error {
	if _, err := t.q().ExecContext(ctx,
		`DELETE FROM mem_recall_vec_indexes WHERE table_id = ?`, tableID); err != nil {
		return err
	}
	_, err := t.q().ExecContext(ctx, `DROP TABLE IF EXISTS `+vecIndexName(tableID))
	return err
}

// removeVectors drops index rows for units that no longer exist. Scopes without
// an index are skipped rather than created: a table with no vectors has nothing
// to remove.
func (t *tx) removeVectors(ctx context.Context, units []unitRef) error {
	for _, unit := range units {
		ready, _, err := t.vecIndexReady(ctx, unit.tableID)
		if err != nil {
			return err
		}
		if !ready {
			continue
		}
		if _, err := t.q().ExecContext(ctx,
			`DELETE FROM `+vecIndexName(unit.tableID)+` WHERE rowid = ?`, unit.unitNo); err != nil {
			return err
		}
	}
	return nil
}

// vecIndexReady reports whether a Table's derived index is both registered and
// physically there. A registry row can outlive its table — a DROP, a restored
// file — and trusting the row would mean deleting from a table that is not there
// and skipping the rebuild that is the whole point of the pass.
func (t *tx) vecIndexReady(ctx context.Context, tableID string) (bool, int, error) {
	known, dimensions, err := t.vecIndexMetadata(ctx, tableID)
	if err != nil || !known {
		return false, 0, err
	}
	exists, err := t.vecIndexTableExists(ctx, tableID)
	if err != nil {
		return false, 0, err
	}
	return exists, dimensions, nil
}

// vecIndexTableExists asks about one exact name. It is deliberately not a prefix
// scan: a prefix would also match the virtual table's shadow tables.
func (t *tx) vecIndexTableExists(ctx context.Context, tableID string) (bool, error) {
	found := 0
	err := t.q().QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'table' AND name = ?`, vecIndexName(tableID)).Scan(&found)
	if err != nil {
		return false, err
	}
	return found > 0, nil
}

// vecIndexMetadata reports whether a Table's index is registered and how wide.
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

// vectorDrift is one disagreement between a derived index and the truth.
type vectorDrift struct {
	tableID string
	unitNo  int64
	vector  []byte
	// remove marks an index row whose unit no longer carries that vector.
	remove bool
}

// repairVectorIndex reconciles derived indexes with the truth for one Database.
//
// It repairs derived rows only. A unit whose text moved on without a new vector
// is stale, not broken: its bytes are still what the index should hold, and the
// readiness predicate keeps it out of answers. Re-embedding is the host's work,
// so a pass that waited for it would never converge — this one always makes
// progress towards "the index holds exactly the truth".
//
// The pass is bounded and repeatable: it applies at most limit repairs and
// reports how many are left, so the caller runs it until remaining is zero.
func (t *tx) repairVectorIndex(ctx context.Context, databaseName string, limit int) (repair.VectorReceipt, error) {
	receipt := repair.VectorReceipt{}
	database, err := t.resolveDatabase(ctx, databaseName)
	if err != nil {
		return repair.VectorReceipt{}, err
	}
	identity, err := t.vectorIdentity(ctx, database.ID)
	if err != nil {
		return repair.VectorReceipt{}, err
	}
	if identity.Model == "" {
		// No vector was ever accepted, so every unit is waiting for a host and
		// there is nothing derived to reconcile.
		return receipt, nil
	}

	found := []vectorDrift{}
	remaining := 0
	record := func(drift vectorDrift) {
		if len(found) < limit {
			found = append(found, drift)
			return
		}
		remaining++
	}
	for _, table := range database.Tables {
		drifts, err := t.tableVectorDrift(ctx, database.ID, table.ID, identity.Dimensions, record)
		if err != nil {
			return repair.VectorReceipt{}, err
		}
		_ = drifts
	}
	for _, drift := range found {
		if err := t.applyVectorDrift(ctx, database.ID, drift); err != nil {
			return repair.VectorReceipt{}, err
		}
	}
	receipt.Repaired = len(found)
	receipt.Remaining = remaining
	return receipt, nil
}

// tableVectorDrift reports every disagreement in one Table, newest number last:
// a truth vector with no index row or a different one, and an index row whose
// unit is gone or holds nothing.
func (t *tx) tableVectorDrift(ctx context.Context, databaseID, tableID string, dimensions int, record func(vectorDrift)) (int, error) {
	known, indexedDimensions, err := t.vecIndexReady(ctx, tableID)
	if err != nil {
		return 0, err
	}
	if known && indexedDimensions != dimensions {
		return 0, fail(result.CodeInternal,
			"the vector index for %s was built with %d dimensions, the database now uses %d",
			tableID, indexedDimensions, dimensions)
	}
	count := 0
	units, err := t.q().QueryContext(ctx, `SELECT unit_no, embedding FROM mem_recall_units
		WHERE table_id = ? AND embedding IS NOT NULL ORDER BY unit_no`, tableID)
	if err != nil {
		return 0, err
	}
	for units.Next() {
		var unitNo int64
		var vector []byte
		if err := units.Scan(&unitNo, &vector); err != nil {
			_ = units.Close()
			return 0, err
		}
		count++
		if !known {
			record(vectorDrift{tableID: tableID, unitNo: unitNo, vector: vector})
			continue
		}
		indexed, found, err := t.indexedVector(ctx, tableID, unitNo)
		if err != nil {
			_ = units.Close()
			return 0, err
		}
		if !found || !bytes.Equal(indexed, vector) {
			record(vectorDrift{tableID: tableID, unitNo: unitNo, vector: vector})
		}
	}
	if err := units.Err(); err != nil {
		_ = units.Close()
		return 0, err
	}
	_ = units.Close()

	if !known {
		return count, nil
	}
	// The other direction: index rows whose unit is gone, or whose unit no
	// longer holds bytes at all.
	indexed, err := t.q().QueryContext(ctx, `SELECT rowid FROM `+vecIndexName(tableID)+` ORDER BY rowid`)
	if err != nil {
		return 0, err
	}
	orphans := []int64{}
	for indexed.Next() {
		var unitNo int64
		if err := indexed.Scan(&unitNo); err != nil {
			_ = indexed.Close()
			return 0, err
		}
		orphans = append(orphans, unitNo)
	}
	if err := indexed.Err(); err != nil {
		_ = indexed.Close()
		return 0, err
	}
	_ = indexed.Close()
	for _, unitNo := range orphans {
		holds, err := t.unitHoldsVector(ctx, databaseID, unitNo)
		if err != nil {
			return 0, err
		}
		if !holds {
			record(vectorDrift{tableID: tableID, unitNo: unitNo, remove: true})
		}
	}
	return count, nil
}

func (t *tx) indexedVector(ctx context.Context, tableID string, unitNo int64) ([]byte, bool, error) {
	vector := []byte{}
	err := t.q().QueryRowContext(ctx,
		`SELECT embedding FROM `+vecIndexName(tableID)+` WHERE rowid = ?`, unitNo).Scan(&vector)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return vector, true, nil
}

func (t *tx) unitHoldsVector(ctx context.Context, databaseID string, unitNo int64) (bool, error) {
	var held int
	err := t.q().QueryRowContext(ctx, `SELECT COUNT(*) FROM mem_recall_units
		WHERE unit_no = ? AND database_id = ? AND embedding IS NOT NULL`, unitNo, databaseID).Scan(&held)
	if err != nil {
		return false, err
	}
	return held > 0, nil
}

func (t *tx) applyVectorDrift(ctx context.Context, databaseID string, drift vectorDrift) error {
	if drift.remove {
		ready, _, err := t.vecIndexReady(ctx, drift.tableID)
		if err != nil {
			return err
		}
		if !ready {
			return nil
		}
		_, err = t.q().ExecContext(ctx,
			`DELETE FROM `+vecIndexName(drift.tableID)+` WHERE rowid = ?`, drift.unitNo)
		return err
	}
	return t.storeVector(ctx, databaseID, drift.tableID, drift.unitNo, drift.vector)
}

// vectorIndexDrift counts the disagreements a reconcile pass would repair. It is
// the same walk REPAIR runs, with a counter instead of a writer, so the health
// report and the repair cannot disagree about what "drift" means.
func (t *tx) vectorIndexDrift(ctx context.Context, databaseID, tableID string, dimensions int) (int, error) {
	drift := 0
	if _, err := t.tableVectorDrift(ctx, databaseID, tableID, dimensions, func(vectorDrift) { drift++ }); err != nil {
		return 0, err
	}
	return drift, nil
}
