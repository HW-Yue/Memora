package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/HW-Yue/Memora/internal/change"
)

// changeDraft collects what one write transaction did. It becomes a single
// committed-change envelope at commit, so a statement touching fifty rows has
// one attribution record rather than fifty.
type changeDraft struct {
	metadata change.Metadata
	entries  map[string]change.Entry
	order    []string
	sequence uint64
}

func (t *tx) recordEntry(entry change.Entry, metadata change.Metadata) {
	if t.change == nil {
		t.change = &changeDraft{entries: map[string]change.Entry{}}
	}
	draft := t.change
	if strings.TrimSpace(draft.metadata.Actor) == "" {
		draft.metadata = metadata
	}
	key := string(entry.ObjectKind) + "\x00" + entry.ObjectID
	if existing, ok := draft.entries[key]; ok {
		entry.BeforeRevision = existing.BeforeRevision
		if entry.Operation != existing.Operation && existing.Operation == change.OperationInsert {
			entry.Operation = change.OperationInsert
		}
	} else {
		draft.order = append(draft.order, key)
	}
	draft.entries[key] = entry
}

// commitSequence is the one sequence number this transaction's writes carry.
func (t *tx) commitSequence(ctx context.Context) (uint64, error) {
	if t.change == nil {
		t.change = &changeDraft{entries: map[string]change.Entry{}}
	}
	if t.change.sequence == 0 {
		value, err := t.nextCounter(ctx, "commit_sequence")
		if err != nil {
			return 0, err
		}
		t.change.sequence = value
	}
	return t.change.sequence, nil
}

func (t *tx) flushChange(ctx context.Context) error {
	draft := t.change
	if draft == nil || len(draft.entries) == 0 {
		return nil
	}
	sequence, err := t.commitSequence(ctx)
	if err != nil {
		return err
	}
	metadata := draft.metadata
	if strings.TrimSpace(metadata.Actor) == "" {
		metadata.Actor = "system"
	}
	if strings.TrimSpace(metadata.Source) == "" {
		metadata.Source = "msql"
	}
	if strings.TrimSpace(metadata.Reason) == "" {
		metadata.Reason = "write"
	}
	entries := make([]change.Entry, 0, len(draft.order))
	for _, key := range draft.order {
		entries = append(entries, draft.entries[key])
	}
	envelope, err := change.NewEnvelope(sequence, t.now, metadata, entries)
	if err != nil {
		return fmt.Errorf("seal committed change: %w", err)
	}
	if _, err := t.q().ExecContext(ctx,
		`INSERT INTO mem_changes(sequence, transaction_id, database_id, committed_at, body) VALUES (?, ?, ?, ?, ?)`,
		sequence, envelope.TransactionID, strings.Join(envelope.DatabaseIDs, ","), formatTime(t.now), encodeJSON(envelope)); err != nil {
		return err
	}
	for _, databaseID := range envelope.DatabaseIDs {
		if _, err := t.q().ExecContext(ctx,
			`INSERT INTO mem_change_scopes(database_id, sequence) VALUES (?, ?)`, databaseID, sequence); err != nil {
			return err
		}
	}
	return nil
}

func (db *DB) ListCommittedChanges(ctx context.Context, databaseID string, after, snapshot uint64, limit int) ([]change.Envelope, uint64, bool, error) {
	if limit < 1 || limit > 256 {
		return nil, 0, false, change.ErrInvalid
	}
	var values []change.Envelope
	more := false
	err := db.view(ctx, func(t *tx) error {
		highWater, err := t.peekCounter(ctx, "commit_sequence")
		if err != nil {
			return err
		}
		if snapshot == 0 {
			snapshot = highWater
		}
		if snapshot > highWater || after > snapshot {
			return change.ErrInvalid
		}
		query := `SELECT c.body FROM mem_changes c WHERE c.sequence > ? AND c.sequence <= ?`
		arguments := []any{after, snapshot}
		if databaseID != "" {
			query = `SELECT c.body FROM mem_changes c JOIN mem_change_scopes s ON s.sequence = c.sequence
				WHERE s.database_id = ? AND c.sequence > ? AND c.sequence <= ?`
			arguments = []any{databaseID, after, snapshot}
		}
		rows, err := t.q().QueryContext(ctx, query+` ORDER BY c.sequence LIMIT ?`, append(arguments, limit+1)...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var body string
			if err := rows.Scan(&body); err != nil {
				return err
			}
			var envelope change.Envelope
			if err := decodeJSON(body, &envelope); err != nil {
				return err
			}
			values = append(values, envelope)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, 0, false, err
	}
	if len(values) > limit {
		values, more = values[:limit], true
	}
	return values, snapshot, more, nil
}

func (db *DB) GetCommittedChange(ctx context.Context, transactionID, databaseID string) (change.Envelope, error) {
	var envelope change.Envelope
	err := db.view(ctx, func(t *tx) error {
		var body string
		err := t.q().QueryRowContext(ctx, `SELECT body FROM mem_changes WHERE transaction_id = ?`, transactionID).Scan(&body)
		if errors.Is(err, sql.ErrNoRows) {
			return change.ErrNotFound
		}
		if err != nil {
			return err
		}
		if err := decodeJSON(body, &envelope); err != nil {
			return err
		}
		if databaseID != "" {
			for _, scope := range envelope.DatabaseIDs {
				if scope == databaseID {
					return nil
				}
			}
			return change.ErrNotFound
		}
		return nil
	})
	return envelope, err
}

// claimAttribution sets who this transaction's change envelope is attributed
// to, before any entry records a system default.
func (t *tx) claimAttribution(metadata change.Metadata) {
	if t.change == nil {
		t.change = &changeDraft{entries: map[string]change.Entry{}}
	}
	if strings.TrimSpace(metadata.Actor) != "" {
		t.change.metadata = metadata
	}
}
