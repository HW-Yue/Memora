package sqlstore

import (
	"context"
	"encoding/json"

	"github.com/HW-Yue/Memora/internal/archive"
	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/row"
)

// Archiving is the engine's whole job when a Row is deleted: the Row, the leaf
// it occupied, its history and every link pointing at it all go, so the archive
// is the only copy left. Rebuilding from it is the Agent's work, not the
// engine's. See docs/product/row-delete-archive.md.
type (
	ArchiveSegment = archive.Segment
	ArchivedRow    = archive.Row
)

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
	links := make([]archive.Link, 0, len(value.Links))
	for _, link := range value.Links {
		links = append(links, archive.Link{
			RelationID: link.RelationID, Direction: link.Direction, RowID: link.RowID,
			DatabaseID: link.DatabaseID, TableID: link.TableID, Summary: link.Summary,
			Revision: link.Revision, Type: link.Type, Description: link.Description,
		})
	}
	content, err := json.Marshal(ArchivedRow{
		RowID: value.ID, SchemaVersion: value.SchemaVersion, Revision: value.Revision,
		CommitSequence: value.CommitSequence, RowState: string(value.State), Values: value.Values,
		RouteLeafIDs: value.RouteLeafIDs, Links: links, SuccessorIDs: value.SuccessorIDs,
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
