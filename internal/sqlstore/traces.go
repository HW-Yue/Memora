package sqlstore

import (
	"context"
	"database/sql"
	"errors"

	"github.com/HW-Yue/Memora/internal/routetrace"
)

// RecordRouteTrace stores an Agent's navigation receipt. Recording the same
// trace twice with the same content returns the stored copy.
func (db *DB) RecordRouteTrace(ctx context.Context, draft routetrace.Draft) (routetrace.Trace, error) {
	var trace routetrace.Trace
	err := db.update(ctx, func(t *tx) error {
		var body string
		err := t.q().QueryRowContext(ctx, `SELECT body FROM mem_traces WHERE trace_id = ?`, draft.TraceID).Scan(&body)
		if err == nil {
			var existing routetrace.Trace
			if err := decodeJSON(body, &existing); err != nil {
				return err
			}
			want, sealErr := routetrace.Seal(draft, existing.Sequence)
			if sealErr != nil || want.Checksum != existing.Checksum {
				return routetrace.ErrConflict
			}
			trace = existing
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		sequence, err := t.nextCounter(ctx, "trace_sequence")
		if err != nil {
			return err
		}
		trace, err = routetrace.Seal(draft, sequence)
		if err != nil {
			return err
		}
		_, err = t.q().ExecContext(ctx, `INSERT INTO mem_traces(trace_id, database_id, sequence, body) VALUES (?, ?, ?, ?)`,
			trace.TraceID, trace.DatabaseID, trace.Sequence, encodeJSON(trace))
		return err
	})
	return trace, err
}

func (db *DB) ListRouteTraces(ctx context.Context, databaseID string, after uint64, snapshot routetrace.Snapshot, limit int) ([]routetrace.Trace, routetrace.Snapshot, bool, error) {
	if limit < 1 || limit > 256 {
		return nil, routetrace.Snapshot{}, false, routetrace.ErrInvalid
	}
	var traces []routetrace.Trace
	err := db.view(ctx, func(t *tx) error {
		highWater, err := t.peekCounter(ctx, "trace_sequence")
		if err != nil {
			return err
		}
		if snapshot.HighWater == 0 {
			snapshot.HighWater = highWater
		}
		rows, err := t.q().QueryContext(ctx, `SELECT body FROM mem_traces WHERE database_id = ? AND sequence > ? AND sequence <= ?
			ORDER BY sequence LIMIT ?`, databaseID, after, snapshot.HighWater, limit+1)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var body string
			if err := rows.Scan(&body); err != nil {
				return err
			}
			var trace routetrace.Trace
			if err := decodeJSON(body, &trace); err != nil {
				return err
			}
			traces = append(traces, trace)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, routetrace.Snapshot{}, false, err
	}
	more := len(traces) > limit
	if more {
		traces = traces[:limit]
	}
	return traces, snapshot, more, nil
}

func (db *DB) GetRouteTrace(ctx context.Context, traceID, databaseID string) (routetrace.Trace, error) {
	var trace routetrace.Trace
	err := db.view(ctx, func(t *tx) error {
		var body string
		err := t.q().QueryRowContext(ctx, `SELECT body FROM mem_traces WHERE trace_id = ? AND database_id = ?`, traceID, databaseID).Scan(&body)
		if errors.Is(err, sql.ErrNoRows) {
			return routetrace.ErrNotFound
		}
		if err != nil {
			return err
		}
		return decodeJSON(body, &trace)
	})
	return trace, err
}
