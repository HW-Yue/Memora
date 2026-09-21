package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/recall"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
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
	VectorIdentity     = recall.VectorIdentity
	VectorRecord       = recall.VectorRecord
	VectorRekeyTarget  = recall.VectorRekeyTarget
	VectorRekeyReceipt = recall.VectorRekeyReceipt
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

// rekeyState is the identity row's window columns. Active means this Database is
// between identities: its derived index is gone and its units are being
// released, so nothing may read or write the vector layer.
type rekeyState struct {
	Active    bool
	Target    recall.VectorRekeyTarget
	StartedAt string
	Identity  VectorIdentity
}

// vectorIdentityRow reads the identity and the window in one go, without
// refusing. Every guard in this file is a decision made on top of it, and the
// one caller that must keep answering during a window — the status a host reads
// to learn which identity to embed for — needs exactly this.
func (t *tx) vectorIdentityRow(ctx context.Context, databaseID string) (VectorIdentity, rekeyState, error) {
	identity := VectorIdentity{}
	state := rekeyState{}
	var lockedAt, rekeyAt sql.NullString
	err := t.q().QueryRowContext(ctx, `SELECT embedding_model, embedding_dimensions, embedding_locked_at,
			embedding_rekey_at, embedding_rekey_model, embedding_rekey_dimensions
		FROM mem_databases WHERE id = ?`, databaseID).
		Scan(&identity.Model, &identity.Dimensions, &lockedAt,
			&rekeyAt, &state.Target.Model, &state.Target.Dimensions)
	if errors.Is(err, sql.ErrNoRows) {
		return VectorIdentity{}, rekeyState{}, fail(result.CodeNotFound, "database was not found")
	}
	if err != nil {
		return VectorIdentity{}, rekeyState{}, err
	}
	identity.LockedAt = lockedAt.String
	state.StartedAt = rekeyAt.String
	state.Active = rekeyAt.String != ""
	state.Identity = identity
	return identity, state, nil
}

// vectorIdentity reads the pair a Database is locked to. An empty model means it
// has not accepted a vector yet.
//
// A rekey in progress is a refusal here rather than at each call site on
// purpose: accepting a vector, storing a derived index row, answering a vector
// query and reconciling the index all have to stop, and the one place they all
// pass through is this read. A guard per statement would be a guard someone
// forgets to add.
func (t *tx) vectorIdentity(ctx context.Context, databaseID string) (VectorIdentity, error) {
	identity, state, err := t.vectorIdentityRow(ctx, databaseID)
	if err != nil {
		return VectorIdentity{}, err
	}
	if state.Active {
		return VectorIdentity{}, fail(result.CodeRekeyInProgress,
			"database %q is between vector identities; no vector path can answer until the rekey finishes",
			databaseID)
	}
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
	// The status is the one vector read that keeps answering inside a rekey
	// window: it is what tells a host which identity to embed for once the
	// window closes, so refusing here would leave the work list unreachable.
	identity, state, err := t.vectorIdentityRow(ctx, database.ID)
	if err != nil {
		return recall.VectorStatus{}, err
	}
	status.IdentityLocked = identity.Model != ""
	status.Model, status.Dimensions = identity.Model, identity.Dimensions

	tableFilter := ""
	tableID := ""
	arguments := []any{database.ID}
	if tableName != "" {
		table, err := t.liveTable(ctx, databaseName, tableName)
		if err != nil {
			return recall.VectorStatus{}, err
		}
		tableID = table.ID
		tableFilter = " AND u.table_id = ?"
		arguments = append(arguments, table.ID)
	}
	if state.Active {
		// Between identities nothing is ready, and the identity still on the row
		// is the one being retired — the target is what the drain will need.
		status.Rekeying = true
		status.IdentityLocked = false
		status.Model, status.Dimensions = state.Target.Model, state.Target.Dimensions
		status.RekeyStartedAt = state.StartedAt
		remaining, err := t.unreleasedVectorUnits(ctx, database.ID, tableID)
		if err != nil {
			return recall.VectorStatus{}, err
		}
		status.RekeyRemaining = remaining
	}

	readiness := `AND (u.embedding IS NULL
		       OR u.embedded_content_hash <> u.content_hash
		       OR u.embedding_model <> d.embedding_model
		       OR u.embedding_dimensions <> d.embedding_dimensions)`
	if status.Rekeying {
		// Inside the window every unit is waiting, including the ones still
		// holding bytes: those bytes belong to the identity being retired.
		readiness = ""
	}
	err = t.q().QueryRowContext(ctx, `SELECT COUNT(*) FROM mem_recall_units u
		JOIN mem_databases d ON d.id = u.database_id
		WHERE u.database_id = ?`+tableFilter+`
		  `+readiness, arguments...).Scan(&status.NotReady)
	if err != nil {
		return recall.VectorStatus{}, err
	}
	return status, nil
}

// pendingVectors lists the units a host still has to embed, lowest number first.
//
// It is the read side of the same predicate the readiness count uses, which is
// why a client can drain a backlog it did not create: rows written by another
// client, or before a provider was configured, show up here exactly like rows
// written just now. A stale vector is listed too — it needs re-embedding, and
// the payload handed over is the text the unit holds now, not the one the old
// vector was computed from.
func (t *tx) pendingVectors(ctx context.Context, databaseName string, limit int) ([]recall.PendingUnit, error) {
	database, err := t.resolveDatabase(ctx, databaseName)
	if err != nil {
		return nil, err
	}
	// A work list inside a rekey window would be a lie twice over: the units it
	// named are the ones being released, and the host that embedded them would
	// be embedding for an identity that is no longer the target.
	if _, err := t.vectorIdentity(ctx, database.ID); err != nil {
		return nil, err
	}
	rows, err := t.q().QueryContext(ctx, `SELECT u.unit_no, tb.name, u.content_hash, u.payload
		FROM mem_recall_units u
		JOIN mem_tables tb ON tb.id = u.table_id
		JOIN mem_databases d ON d.id = u.database_id
		WHERE u.database_id = ?
		  AND (u.embedding IS NULL
		       OR u.embedded_content_hash <> u.content_hash
		       OR u.embedding_model <> d.embedding_model
		       OR u.embedding_dimensions <> d.embedding_dimensions)
		ORDER BY u.unit_no LIMIT ?`, database.ID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	units := []recall.PendingUnit{}
	for rows.Next() {
		unit := recall.PendingUnit{}
		if err := rows.Scan(&unit.UnitNo, &unit.Table, &unit.ContentHash, &unit.Payload); err != nil {
			return nil, err
		}
		units = append(units, unit)
	}
	return units, rows.Err()
}

// rekeyVectorIdentity moves one Database out of the identity it locked itself
// into. It is bounded and repeatable like the repair passes in this family: the
// first call opens the window and drops what the old identity derived, each call
// releases at most limit units, and the call that releases the last one writes
// the new identity and closes the window.
//
// The window is stored rather than inferred because the two halves have to agree
// across a crash: while it is open the derived index is gone and the units are
// half-released, so any reader would be answering from an index that no longer
// describes the bytes, and any writer would be pinning an identity the rekey is
// retiring.
//
// A nil target leaves the Database unlocked, which is the shape a user needs
// when nothing was configured yet or the first provider they tried was a trial:
// whatever they configure next is then free to lock it.
func (t *tx) rekeyVectorIdentity(
	ctx context.Context,
	databaseName string,
	limit int,
	target *recall.VectorRekeyTarget,
) (recall.VectorRekeyReceipt, error) {
	receipt := recall.VectorRekeyReceipt{}
	database, err := t.resolveDatabase(ctx, databaseName)
	if err != nil {
		return recall.VectorRekeyReceipt{}, err
	}
	if limit < 1 {
		return recall.VectorRekeyReceipt{}, fail(result.CodeValidation, "a rekey needs a positive LIMIT")
	}
	if target != nil && (strings.TrimSpace(target.Model) == "" || target.Dimensions <= 0) {
		return recall.VectorRekeyReceipt{}, fail(result.CodeValidation,
			"a rekey target needs a model and positive dimensions")
	}
	identity, state, err := t.vectorIdentityRow(ctx, database.ID)
	if err != nil {
		return recall.VectorRekeyReceipt{}, err
	}
	switch {
	case !state.Active:
		if identity.Model == "" && target == nil {
			// No identity to release and nothing derived from one.
			return receipt, nil
		}
		state = rekeyState{Active: true, StartedAt: formatTime(t.now)}
		if target != nil {
			state.Target = *target
		}
		if _, err := t.q().ExecContext(ctx, `UPDATE mem_databases
			SET embedding_rekey_at = ?, embedding_rekey_model = ?, embedding_rekey_dimensions = ?
			WHERE id = ?`,
			state.StartedAt, state.Target.Model, state.Target.Dimensions, database.ID); err != nil {
			return recall.VectorRekeyReceipt{}, err
		}
		if err := t.dropVectorIndexes(ctx, database.ID); err != nil {
			return recall.VectorRekeyReceipt{}, err
		}
	case target != nil && (target.Model != state.Target.Model || target.Dimensions != state.Target.Dimensions):
		// The window's target is fixed by the call that opened it. Aiming an
		// open window somewhere else would release units for one identity and
		// lock the Database to another.
		return recall.VectorRekeyReceipt{}, fail(result.CodeValidation,
			"database %q is already rekeying to %s/%d, so it cannot also be rekeyed to %s/%d",
			database.Name, state.Target.Model, state.Target.Dimensions, target.Model, target.Dimensions)
	case target == nil && state.Target.Model != "":
		// A window nobody finishes would leave the Database refusing every
		// vector answer with no way out, so "no target" is also the escape
		// hatch: it re-aims the open window at unlocked. Units already released
		// stay released — the work is the same either way, only the ending
		// changes — and the next accepted vector locks whatever is configured.
		state.Target = recall.VectorRekeyTarget{}
		if _, err := t.q().ExecContext(ctx, `UPDATE mem_databases
			SET embedding_rekey_model = '', embedding_rekey_dimensions = 0 WHERE id = ?`,
			database.ID); err != nil {
			return recall.VectorRekeyReceipt{}, err
		}
	}

	released, err := t.releaseVectorBytes(ctx, database.ID, limit)
	if err != nil {
		return recall.VectorRekeyReceipt{}, err
	}
	remaining, err := t.unreleasedVectorUnits(ctx, database.ID, "")
	if err != nil {
		return recall.VectorRekeyReceipt{}, err
	}
	receipt.Released, receipt.Remaining = released, remaining
	receipt.Model, receipt.Dimensions = state.Target.Model, state.Target.Dimensions
	if remaining > 0 {
		receipt.Rekeying = true
		return receipt, nil
	}
	lockedAt := ""
	if state.Target.Model != "" {
		lockedAt = formatTime(t.now)
	}
	if _, err := t.q().ExecContext(ctx, `UPDATE mem_databases
		SET embedding_model = ?, embedding_dimensions = ?, embedding_locked_at = ?,
		    embedding_rekey_at = '', embedding_rekey_model = '', embedding_rekey_dimensions = 0
		WHERE id = ?`,
		state.Target.Model, state.Target.Dimensions, lockedAt, database.ID); err != nil {
		return recall.VectorRekeyReceipt{}, err
	}
	return receipt, nil
}

// releaseVectorBytes lets go of at most limit units' vectors. The provenance
// columns go with the bytes: a unit that keeps a model name while holding no
// bytes would still be counted as ready by anything that asked the columns
// instead of the bytes.
func (t *tx) releaseVectorBytes(ctx context.Context, databaseID string, limit int) (int, error) {
	outcome, err := t.q().ExecContext(ctx, `UPDATE mem_recall_units
		SET embedding = NULL, embedded_content_hash = '', embedding_model = '',
		    embedding_dimensions = 0, embedded_at = '', updated_at = ?
		WHERE unit_no IN (SELECT unit_no FROM mem_recall_units
			WHERE database_id = ? AND embedding IS NOT NULL ORDER BY unit_no LIMIT ?)`,
		formatTime(t.now), databaseID, limit)
	if err != nil {
		return 0, err
	}
	released, err := outcome.RowsAffected()
	return int(released), err
}

// unreleasedVectorUnits counts the bytes a rekey still has to let go of, for a
// whole Database or for one of its Tables. It is the same number the pass
// reports as remaining, so a caller watching the window and a caller running it
// never disagree about how much is left.
func (t *tx) unreleasedVectorUnits(ctx context.Context, databaseID, tableID string) (int, error) {
	filter := ""
	arguments := []any{databaseID}
	if tableID != "" {
		filter = " AND table_id = ?"
		arguments = append(arguments, tableID)
	}
	count := 0
	err := t.q().QueryRowContext(ctx, `SELECT COUNT(*) FROM mem_recall_units
		WHERE database_id = ? AND embedding IS NOT NULL`+filter, arguments...).Scan(&count)
	return count, err
}

// attachVectorForRow offers a write's attached embedding to the unit the Row
// owns. It is best effort on purpose: the caller's Row is already written, and a
// vector that does not describe the text it was computed from must not turn a
// recorded fact into a failed write. The unit simply stays not-ready.
func (t *tx) attachVectorForRow(ctx context.Context, table catalog.Table, rowID string, vector *row.Vector) error {
	if vector == nil {
		return nil
	}
	var unitNo int64
	err := t.q().QueryRowContext(ctx, `SELECT unit_no FROM mem_recall_units WHERE table_id = ? AND row_id = ?`,
		table.ID, rowID).Scan(&unitNo)
	if errors.Is(err, sql.ErrNoRows) {
		// The Row is not recallable (it is not live, or it holds no leaf), so
		// there is nothing for a vector to describe.
		return nil
	}
	if err != nil {
		return err
	}
	_, err = t.acceptVector(ctx, table.DatabaseID, recall.VectorRecord{
		UnitNo: unitNo, ContentHash: vector.ContentHash, Model: vector.Model,
		Dimensions: len(vector.Values), Vector: vector.Values,
	})
	return err
}

// unitVectorState answers for one Row: does it have a recallable unit, and is
// that unit's vector one the vector path can use. The identity join is part of
// it for the same reason it is everywhere else — readiness is a property of a
// unit *against its Database's identity*.
func (t *tx) unitVectorState(ctx context.Context, databaseName, tableName, rowID string) (recall.UnitVectorState, error) {
	table, err := t.liveTable(ctx, databaseName, tableName)
	if err != nil {
		return recall.UnitVectorState{}, err
	}
	state := recall.UnitVectorState{}
	var embeddedHash, unitModel string
	var unitDimensions int
	err = t.q().QueryRowContext(ctx, `SELECT u.unit_no, u.content_hash, u.embedded_content_hash,
			u.embedding IS NOT NULL AND LENGTH(u.embedding) > 0, d.embedding_model, d.embedding_dimensions
		FROM mem_recall_units u JOIN mem_databases d ON d.id = u.database_id
		WHERE u.database_id = ? AND u.table_id = ? AND u.row_id = ?`,
		table.DatabaseID, table.ID, rowID).
		Scan(&state.UnitNo, &state.ContentHash, &embeddedHash, &state.Ready, &state.Model, &state.Dimensions)
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	// Inside a rekey window nothing is ready, and the identity named on the row
	// is the one being retired — the honest answer is the target, with Ready
	// false, so a write that offered a vector reports it did not attach rather
	// than claiming a vector the vector path will not answer with.
	if _, rekey, err := t.vectorIdentityRow(ctx, table.DatabaseID); err != nil {
		return state, err
	} else if rekey.Active {
		state.HasUnit = true
		state.Ready = false
		state.Model, state.Dimensions = rekey.Target.Model, rekey.Target.Dimensions
		return state, nil
	}
	state.HasUnit = true
	state.Ready = state.Ready && embeddedHash == state.ContentHash &&
		unitModel == "" && unitDimensions == 0
	// The unit's own provenance must name the Database's identity; a unit whose
	// bytes came from another model is not usable even though it has bytes.
	if state.Ready {
		if err := t.q().QueryRowContext(ctx, `SELECT embedding_model, embedding_dimensions FROM mem_recall_units
			WHERE unit_no = ?`, state.UnitNo).Scan(&unitModel, &unitDimensions); err != nil {
			return state, err
		}
		state.Ready = unitModel == state.Model && unitDimensions == state.Dimensions
	}
	return state, nil
}
