package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/HW-Yue/Memora/internal/recall"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/sqlstore/vecext"
)

// The vector truth lives on the unit row itself, and vec0 is an index derived
// from it. Two rules follow from that and are enforced here:
//
//   - A vector is accepted only for the text it was computed from. The record
//     carries the hash of that text, and it has to equal the unit's current
//     content hash. Without this check a host could attach an embedding of the
//     previous revision, and recall returns no scores, so nothing downstream
//     could tell that the neighbours were computed for a different sentence.
//   - A Database commits to one (model, dimensions) pair the first time it
//     accepts a vector ("first use locks it"). Vectors from another model are
//     not lower quality, they are incomparable; mixing them in one index is
//     silent pollution rather than visible degradation.
//
// The vocabulary is shared with the executor, which speaks it without the
// storage layer; these aliases are what the storage layer calls it.
type (
	VectorIdentity = recall.VectorIdentity
	VectorRecord   = recall.VectorRecord
)

type recallUnit struct {
	unitNo      int64
	tableID     string
	contentHash string
}

// acceptVector stores an embedding only when it describes the unit's current
// text and matches the Database's identity. A rejection leaves the unit exactly
// as it was: a stale vector is a known state the readiness count reports, while
// a wrong one is invisible.
func (t *tx) acceptVector(ctx context.Context, databaseName string, record VectorRecord) (VectorIdentity, error) {
	database, err := t.resolveDatabase(ctx, databaseName)
	if err != nil {
		return VectorIdentity{}, err
	}
	unit, err := t.recallUnitByNumber(ctx, database.ID, record.UnitNo)
	if err != nil {
		return VectorIdentity{}, err
	}
	if strings.TrimSpace(record.Model) == "" {
		return VectorIdentity{}, fail(result.CodeValidation, "the vector names no model")
	}
	if record.Dimensions != len(record.Vector) || record.Dimensions == 0 {
		return VectorIdentity{}, fail(result.CodeValidation,
			"the vector says %d dimensions and carries %d", record.Dimensions, len(record.Vector))
	}
	if record.ContentHash == "" || record.ContentHash != unit.contentHash {
		return VectorIdentity{}, fail(result.CodeValidation,
			"the vector was computed for different text than unit %d holds; re-embed its current payload", record.UnitNo)
	}

	identity, err := t.vectorIdentity(ctx, database.ID)
	if err != nil {
		return VectorIdentity{}, err
	}
	switch {
	case identity.Model == "":
		identity.LockedAt = formatTime(t.now)
		if _, err := t.q().ExecContext(ctx, `UPDATE mem_databases
			SET embedding_model = ?, embedding_dimensions = ?, embedding_locked_at = ? WHERE id = ?`,
			record.Model, record.Dimensions, identity.LockedAt, database.ID); err != nil {
			return VectorIdentity{}, err
		}
		identity.Model, identity.Dimensions = record.Model, record.Dimensions
	case identity.Model != record.Model || identity.Dimensions != record.Dimensions:
		return VectorIdentity{}, fail(result.CodeValidation,
			"database %q is locked to %s/%d vectors; %s/%d cannot share its index",
			database.Name, identity.Model, identity.Dimensions, record.Model, record.Dimensions)
	}

	encoded, err := vecext.SerializeFloat32(record.Vector)
	if err != nil {
		return VectorIdentity{}, err
	}
	if _, err := t.q().ExecContext(ctx, `UPDATE mem_recall_units
		SET embedding = ?, embedded_content_hash = ?, embedding_model = ?, embedding_dimensions = ?,
			embedded_at = ?, updated_at = ?
		WHERE unit_no = ?`,
		encoded, record.ContentHash, record.Model, record.Dimensions, formatTime(t.now), formatTime(t.now),
		record.UnitNo); err != nil {
		return VectorIdentity{}, err
	}
	// The index is derived from the row just written, in the same transaction:
	// a crash cannot leave a vector without its index row or the reverse.
	if err := t.storeVector(ctx, database.ID, unit.tableID, record.UnitNo, encoded); err != nil {
		return VectorIdentity{}, err
	}
	return identity, nil
}

// vectorIdentity reads the pair a Database is locked to. An empty model means it
// has not accepted a vector yet.
func (t *tx) vectorIdentity(ctx context.Context, databaseID string) (VectorIdentity, error) {
	identity := VectorIdentity{}
	var lockedAt sql.NullString
	err := t.q().QueryRowContext(ctx, `SELECT embedding_model, embedding_dimensions, embedding_locked_at
		FROM mem_databases WHERE id = ?`, databaseID).
		Scan(&identity.Model, &identity.Dimensions, &lockedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return VectorIdentity{}, fail(result.CodeNotFound, "database was not found")
	}
	if err != nil {
		return VectorIdentity{}, err
	}
	identity.LockedAt = lockedAt.String
	return identity, nil
}

func (t *tx) recallUnitByNumber(ctx context.Context, databaseID string, unitNo int64) (recallUnit, error) {
	unit := recallUnit{unitNo: unitNo}
	err := t.q().QueryRowContext(ctx, `SELECT table_id, content_hash FROM mem_recall_units
		WHERE unit_no = ? AND database_id = ?`, unitNo, databaseID).Scan(&unit.tableID, &unit.contentHash)
	if errors.Is(err, sql.ErrNoRows) {
		// The identity is part of the lookup on purpose: units from every
		// Database share one table, and a vector must never land in another
		// Database's row just because the number fits.
		return recallUnit{}, fail(result.CodeNotFound, "recall unit %d is not in database %q", unitNo, databaseID)
	}
	if err != nil {
		return recallUnit{}, err
	}
	return unit, nil
}

// vectorStatus counts the units a vector path cannot answer for: no vector yet,
// a vector for text the unit no longer holds, or a vector from a different
// identity. Readiness is derived from the truth columns rather than stored, so
// no code path can forget to update it.
//
// The identity join is not optional. Units from every Database share one table,
// so the Database's identity is part of every readiness question — a unit is
// only ready against the identity of the Database it belongs to.
func (t *tx) vectorStatus(ctx context.Context, databaseName, tableName string) (recall.VectorStatus, error) {
	status := recall.VectorStatus{}
	database, err := t.resolveDatabase(ctx, databaseName)
	if err != nil {
		return recall.VectorStatus{}, err
	}
	identity, err := t.vectorIdentity(ctx, database.ID)
	if err != nil {
		return recall.VectorStatus{}, err
	}
	status.IdentityLocked = identity.Model != ""
	status.Model, status.Dimensions = identity.Model, identity.Dimensions

	tableFilter := ""
	arguments := []any{database.ID}
	if tableName != "" {
		table, err := t.liveTable(ctx, databaseName, tableName)
		if err != nil {
			return recall.VectorStatus{}, err
		}
		tableFilter = " AND u.table_id = ?"
		arguments = append(arguments, table.ID)
	}
	err = t.q().QueryRowContext(ctx, `SELECT COUNT(*) FROM mem_recall_units u
		JOIN mem_databases d ON d.id = u.database_id
		WHERE u.database_id = ?`+tableFilter+`
		  AND (u.embedding IS NULL
		       OR u.embedded_content_hash <> u.content_hash
		       OR u.embedding_model <> d.embedding_model
		       OR u.embedding_dimensions <> d.embedding_dimensions)`, arguments...).Scan(&status.NotReady)
	if err != nil {
		return recall.VectorStatus{}, err
	}
	return status, nil
}
