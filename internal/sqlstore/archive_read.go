package sqlstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/HW-Yue/Memora/internal/archive"
	"github.com/HW-Yue/Memora/internal/result"
)

// The archive read model lives in package archive so the executor can speak it
// without the storage layer; these aliases are what the storage layer calls it.
type (
	ArchiveSummary = archive.Summary
	ArchivePage    = archive.Page
	ArchiveRecord  = archive.Record
)

const (
	archiveCursorVersion   = "memora.archive-cursor/v1"
	archiveSnapshotVersion = "memora.archive-snapshot/v1"
	maxArchivePageLimit    = 1000
)

// archiveCursor is a keyset cursor over the append-only sequence. The archive is
// never updated in place, so a sequence value keeps its meaning and no snapshot
// digest is needed; the checksum binds the cursor to the scope it was issued for.
type archiveCursor struct {
	Version  string `json:"version"`
	Scope    string `json:"scope"`
	After    int64  `json:"after"`
	Checksum string `json:"checksum"`
}

func archiveScope(tableID, rowID string) string { return tableID + "|" + rowID }

// archiveSnapshot names the view a listing walks. The envelope requires every
// page to carry one, and for an append-only scope it must not move: a keyset
// cursor only ever walks towards older sequence numbers, so a later deletion
// cannot enter a walk already in progress. Reporting a high-water mark instead
// would make every concurrent write look like a changed view while the two
// pages still cover exactly the same records.
func archiveSnapshot(scope string) string {
	digest := sha256.Sum256([]byte(archiveSnapshotVersion + "|" + scope))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func archiveCursorChecksum(core archiveCursor) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d", core.Version, core.Scope, core.After)))
	return hex.EncodeToString(digest[:])
}

func encodeArchiveCursor(scope string, after int64) (string, error) {
	core := archiveCursor{Version: archiveCursorVersion, Scope: scope, After: after}
	core.Checksum = archiveCursorChecksum(core)
	encoded, err := json.Marshal(core)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func decodeArchiveCursor(cursor, scope string) (int64, error) {
	invalid := func() error {
		return fail(result.CodeValidation, "archive cursor is invalid for this scope")
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, invalid()
	}
	core := archiveCursor{}
	if err := json.Unmarshal(raw, &core); err != nil {
		return 0, invalid()
	}
	if core.Version != archiveCursorVersion || core.Scope != scope || core.After < 0 {
		return 0, invalid()
	}
	if core.Checksum != archiveCursorChecksum(core) {
		return 0, invalid()
	}
	return core.After, nil
}

// archivePage lists one Table's deletions, newest first. A Table scope is
// required and the limit is bounded: an unconditional listing would turn the
// archive into a soft-delete browser, which is what query-model §7 says it must
// never become.
func (t *tx) archivePage(ctx context.Context, databaseName, tableName, rowID, cursor string, limit int) ([]ArchiveSummary, ArchivePage, error) {
	table, err := t.liveTable(ctx, databaseName, tableName)
	if err != nil {
		return nil, ArchivePage{}, err
	}
	if limit < 1 || limit > maxArchivePageLimit {
		return nil, ArchivePage{}, fail(result.CodeValidation,
			"archive limit must be between 1 and %d", maxArchivePageLimit)
	}
	scope := archiveScope(table.ID, rowID)
	after := int64(0)
	if cursor != "" {
		if after, err = decodeArchiveCursor(cursor, scope); err != nil {
			return nil, ArchivePage{}, err
		}
	}
	query := `SELECT sequence, archive_id, row_id, database_id, table_id, revision, deleted_at,
		actor, source, reason, path_json FROM mem_archive WHERE table_id = ?`
	arguments := []any{table.ID}
	if rowID != "" {
		query += ` AND row_id = ?`
		arguments = append(arguments, rowID)
	}
	if after > 0 {
		query += ` AND sequence < ?`
		arguments = append(arguments, after)
	}
	query += ` ORDER BY sequence DESC LIMIT ?`
	arguments = append(arguments, limit+1)

	rows, err := t.q().QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, ArchivePage{}, err
	}
	defer func() { _ = rows.Close() }()

	values := []ArchiveSummary{}
	page := ArchivePage{Snapshot: archiveSnapshot(scope)}
	last := int64(0)
	for rows.Next() {
		var sequence int64
		var summary ArchiveSummary
		var pathJSON string
		if err := rows.Scan(&sequence, &summary.ArchiveID, &summary.RowID, &summary.DatabaseID,
			&summary.TableID, &summary.Revision, &summary.DeletedAt, &summary.Actor, &summary.Source,
			&summary.Reason, &pathJSON); err != nil {
			return nil, ArchivePage{}, err
		}
		if len(values) == limit {
			page.Truncated = true
			break
		}
		segments := []ArchiveSegment{}
		if err := json.Unmarshal([]byte(pathJSON), &segments); err != nil {
			return nil, ArchivePage{}, err
		}
		summary.Path = archivePathLabel(segments)
		values = append(values, summary)
		last = sequence
	}
	if err := rows.Err(); err != nil {
		return nil, ArchivePage{}, err
	}
	if page.Truncated {
		if page.NextCursor, err = encodeArchiveCursor(scope, last); err != nil {
			return nil, ArchivePage{}, err
		}
	}
	return values, page, nil
}

// archiveRecord returns one archived deletion in full. The archive is the only
// place a deleted Row still exists, so this is where a rebuild starts.
func (t *tx) archiveRecord(ctx context.Context, archiveID string) (ArchiveRecord, error) {
	record := ArchiveRecord{}
	var pathJSON, rowJSON string
	err := t.q().QueryRowContext(ctx, `SELECT archive_id, row_id, database_id, table_id, revision,
		deleted_at, actor, source, reason, path_json, row_json FROM mem_archive WHERE archive_id = ?`,
		archiveID).Scan(&record.Summary.ArchiveID, &record.Summary.RowID, &record.Summary.DatabaseID,
		&record.Summary.TableID, &record.Summary.Revision, &record.Summary.DeletedAt,
		&record.Summary.Actor, &record.Summary.Source, &record.Summary.Reason, &pathJSON, &rowJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return ArchiveRecord{}, fail(result.CodeNotFound, "archived Row %q was not found", archiveID)
	}
	if err != nil {
		return ArchiveRecord{}, err
	}
	if err := json.Unmarshal([]byte(pathJSON), &record.Path); err != nil {
		return ArchiveRecord{}, err
	}
	record.Summary.Path = archivePathLabel(record.Path)
	if err := json.Unmarshal([]byte(rowJSON), &record.Row); err != nil {
		return ArchiveRecord{}, err
	}
	return record, nil
}

func archivePathLabel(segments []ArchiveSegment) string {
	names := make([]string, 0, len(segments))
	for _, segment := range segments {
		names = append(names, segment.Name)
	}
	return "/" + strings.Join(names, "/")
}
