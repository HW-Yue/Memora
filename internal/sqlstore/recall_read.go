package sqlstore

import (
	"context"
	"sort"
	"strings"

	"github.com/HW-Yue/Memora/internal/recall"
	"github.com/HW-Yue/Memora/internal/router"
)

// recallKeywords answers a keyword query with semantic paths. Both the ordering
// the limit cuts on and the de-duplication happen here, in the storage layer:
// leaving either to the caller would change what the limit means.
func (t *tx) recallKeywords(ctx context.Context, databaseName, tableName, text string, limit int) ([]recall.Hit, error) {
	database, err := t.resolveDatabase(ctx, databaseName)
	if err != nil {
		return nil, err
	}
	tableID := ""
	if tableName != "" {
		table, err := t.liveTable(ctx, databaseName, tableName)
		if err != nil {
			return nil, err
		}
		tableID = table.ID
	}
	query := `SELECT u.table_id, u.row_id, u.route_id FROM mem_recall_fts f
		JOIN mem_recall_units u ON u.unit_no = f.rowid
		WHERE mem_recall_fts MATCH ? AND u.database_id = ?`
	arguments := []any{text, database.ID}
	if tableID != "" {
		query += ` AND u.table_id = ?`
		arguments = append(arguments, tableID)
	}
	// The internal order decides which hits survive the limit. Insertion order is
	// deterministic and, unlike FTS rank, is nothing a caller could mistake for a
	// judgement about relevance.
	query += ` ORDER BY u.unit_no LIMIT ?`
	arguments = append(arguments, limit)

	rows, err := t.q().QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, err
	}
	type storedHit struct{ tableID, rowID, leafID string }
	stored := []storedHit{}
	for rows.Next() {
		hit := storedHit{}
		if err := rows.Scan(&hit.tableID, &hit.rowID, &hit.leafID); err != nil {
			_ = rows.Close()
			return nil, err
		}
		stored = append(stored, hit)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()

	hits := []recall.Hit{}
	seen := map[string]bool{}
	for _, hit := range stored {
		table, err := t.tableByID(ctx, hit.tableID)
		if err != nil {
			continue
		}
		path, err := t.pathSegments(ctx, table.ID, hit.leafID)
		if err != nil {
			continue
		}
		key := table.ID + "|" + pathLabel(path)
		if seen[key] {
			continue
		}
		seen[key] = true
		hits = append(hits, recall.Hit{
			Database: database.Name, Table: table.Name, Path: path,
			Kind: string(router.KindLeaf), ObjectID: hit.rowID,
		})
	}
	// Stable, de-duplicated output: the internal order above only chose which
	// hits survived the limit.
	sort.Slice(hits, func(left, right int) bool {
		if hits[left].Table != hits[right].Table {
			return hits[left].Table < hits[right].Table
		}
		return pathLabel(hits[left].Path) < pathLabel(hits[right].Path)
	})
	return hits, nil
}

// pathSegments walks from a leaf up to the root and returns the path root-first,
// each segment with the id navigation needs.
func (t *tx) pathSegments(ctx context.Context, tableID, leafID string) ([]recall.Segment, error) {
	segments := []recall.Segment{}
	for id := leafID; id != ""; {
		node, err := t.readRoute(ctx, tableID, id)
		if err != nil {
			return nil, err
		}
		segments = append(segments, recall.Segment{Name: node.Name, RouteID: node.ID})
		id = node.ParentID
	}
	for left, right := 0, len(segments)-1; left < right; left, right = left+1, right-1 {
		segments[left], segments[right] = segments[right], segments[left]
	}
	return segments, nil
}

func pathLabel(segments []recall.Segment) string {
	names := make([]string, 0, len(segments))
	for _, segment := range segments {
		names = append(names, segment.Name)
	}
	return "/" + strings.Join(names, "/")
}
