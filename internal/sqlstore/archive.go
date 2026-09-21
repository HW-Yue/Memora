package sqlstore

import (
	"context"
	"encoding/json"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/row"
)

// Archiving is the engine's whole job when a Row is deleted: the Row, the leaf
// it occupied, its history and every link pointing at it all go, so the archive
// is the only copy left. Rebuilding from it is the Agent's work, not the
// engine's. See docs/product/row-delete-archive.md.
type ArchiveSegment struct {
	RouteID string      `json:"route_id"`
	Name    string      `json:"name"`
	Kind    router.Kind `json:"kind"`
	Purpose string      `json:"purpose"`
}

// ArchivedRow is the Row as it was stored, keyed exactly as the data table
// keys it, so a rebuild does not depend on this file guessing at columns.
type ArchivedRow struct {
	RowID          string         `json:"row_id"`
	SchemaVersion  uint64         `json:"schema_version"`
	Revision       uint64         `json:"revision"`
	CommitSequence uint64         `json:"commit_sequence"`
	RowState       string         `json:"row_state"`
	Values         map[string]any `json:"values"`
	RouteLeafIDs   []string       `json:"route_leaf_ids"`
	Links          []Link         `json:"links"`
	SuccessorIDs   []string       `json:"successor_ids"`
	CreatedAt      string         `json:"created_at"`
	UpdatedAt      string         `json:"updated_at"`
}

// archivedPath walks from the Row's leaf up to the root and returns the path
// root-first, so a reader can rebuild it in the order it was navigated.
func (t *tx) archivedPath(ctx context.Context, table catalog.Table, value storedRow) ([]ArchiveSegment, error) {
	segments := []ArchiveSegment{}
	for leafID := range value.RouteLeafIDs {
		node, err := t.readRoute(ctx, table.ID, value.RouteLeafIDs[leafID])
		if err != nil {
			return nil, err
		}
		for {
			segments = append(segments, ArchiveSegment{
				RouteID: node.ID, Name: node.Name, Kind: node.Kind, Purpose: node.Purpose,
			})
			if node.ParentID == "" {
				break
			}
			if node, err = t.readRoute(ctx, table.ID, node.ParentID); err != nil {
				return nil, err
			}
		}
	}
	for left, right := 0, len(segments)-1; left < right; left, right = left+1, right-1 {
		segments[left], segments[right] = segments[right], segments[left]
	}
	return segments, nil
}

// archiveRow writes the record a rebuild starts from. It runs before anything
// is removed, and inside the same transaction, so a refused delete leaves no
// archive row and a committed delete always has one.
func (t *tx) archiveRow(
	ctx context.Context, table catalog.Table, value storedRow,
	path []ArchiveSegment, metadata row.WriteMetadata,
) error {
	segments, err := json.Marshal(path)
	if err != nil {
		return err
	}
	content, err := json.Marshal(ArchivedRow{
		RowID: value.ID, SchemaVersion: value.SchemaVersion, Revision: value.Revision,
		CommitSequence: value.CommitSequence, RowState: string(value.State), Values: value.Values,
		RouteLeafIDs: value.RouteLeafIDs, Links: value.Links, SuccessorIDs: value.SuccessorIDs,
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	})
	if err != nil {
		return err
	}
	_, err = t.q().ExecContext(ctx, `INSERT INTO mem_archive
		(archive_id, database_id, table_id, row_id, revision, deleted_at, actor, source, reason, path_json, row_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		newID("archive_"), table.DatabaseID, table.ID, value.ID, value.Revision, formatTime(t.now),
		metadata.Actor, metadata.Source, metadata.Reason, string(segments), string(content))
	return err
}
