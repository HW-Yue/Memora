package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"sort"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/recall"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/sqlstore/vecext"
)

// recallNearest answers a vector query with semantic paths, the same shape the
// keyword arm answers with: a position, and nothing about how close it was.
//
// Two things make the answer trustworthy rather than merely plausible:
//
//   - The index is read through the readiness join. An index row can be stale —
//     the text moved on and no host has re-embedded it — and a stale vector
//     describes a sentence the unit no longer holds. Recall returns no scores,
//     so returning it would point at the wrong place with nothing to show for it.
//   - The order is decided here, not upstream. sqlite-vec's ordering among equal
//     distances is not a promise, and with no scores in the answer a caller
//     could never notice it moving. We ask for extra candidates, sort by
//     (distance, unit_no) ourselves, and grow the request until the boundary is
//     unambiguous.
func (t *tx) recallNearest(ctx context.Context, databaseName, tableName string, query []float32, limit int) ([]recall.Hit, error) {
	database, err := t.resolveDatabase(ctx, databaseName)
	if err != nil {
		return nil, err
	}
	identity, err := t.vectorIdentity(ctx, database.ID)
	if err != nil {
		return nil, err
	}
	if identity.Model == "" {
		// Not "nothing matched": this Database has never accepted a vector, so a
		// vector query cannot be answered at all. Saying so is the honest answer.
		return nil, fail(result.CodeValidation,
			"database %q has no vector identity yet, so a vector query has nothing to search", database.Name)
	}
	if len(query) != identity.Dimensions {
		return nil, fail(result.CodeValidation,
			"the query vector has %d dimensions; database %q is locked to %d",
			len(query), database.Name, identity.Dimensions)
	}
	encoded, err := vecext.SerializeFloat32(query)
	if err != nil {
		return nil, err
	}

	tables := database.Tables
	if tableName != "" {
		table, err := t.liveTable(ctx, databaseName, tableName)
		if err != nil {
			return nil, err
		}
		tables = []catalog.Table{table}
	}
	candidates := []vectorCandidate{}
	for _, table := range tables {
		found, err := t.tableNearest(ctx, database.ID, table.ID, encoded, limit)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, found...)
	}
	sort.Slice(candidates, func(left, right int) bool {
		if candidates[left].distance != candidates[right].distance {
			return candidates[left].distance < candidates[right].distance
		}
		// A deterministic tie-break is part of the contract: the same query twice
		// must return the same set, and nothing in the answer would reveal that
		// it did not.
		return candidates[left].unitNo < candidates[right].unitNo
	})

	hits := []recall.Hit{}
	for _, candidate := range candidates {
		if len(hits) == limit {
			break
		}
		unit, err := t.readyUnit(ctx, database.ID, candidate.tableID, candidate.unitNo)
		if err != nil {
			return nil, err
		}
		if unit == nil {
			continue
		}
		table, err := t.tableByID(ctx, candidate.tableID)
		if err != nil {
			continue
		}
		path, err := t.pathSegments(ctx, table.ID, unit.routeID)
		if err != nil {
			continue
		}
		hits = append(hits, recall.Hit{
			Database: database.Name, Table: table.Name, Path: path,
			Kind: "leaf", ObjectID: unit.rowID,
		})
	}
	// Same output contract as the keyword arm: the choice is by distance, the
	// listing is stable and lexicographic.
	sort.Slice(hits, func(left, right int) bool {
		if hits[left].Table != hits[right].Table {
			return hits[left].Table < hits[right].Table
		}
		return pathLabel(hits[left].Path) < pathLabel(hits[right].Path)
	})
	return hits, nil
}

// maxVectorCandidates bounds one Table's search: past this, the extra rows cost
// more than the tie they resolve.
const maxVectorCandidates = 4096

type vectorCandidate struct {
	tableID  string
	unitNo   int64
	distance float64
}

type readyUnitRow struct {
	routeID string
	rowID   string
}

// readyUnit returns a unit's locator only when the vector stored for it still
// describes the text it holds. A nil result is not an error: the row is simply
// not answerable, and skipping it is the point — recall has no scores to show
// that a hit was computed for a sentence the unit has moved past.
func (t *tx) readyUnit(ctx context.Context, databaseID, tableID string, unitNo int64) (*readyUnitRow, error) {
	unit := readyUnitRow{}
	var embeddedHash, contentHash string
	var embedding []byte
	err := t.q().QueryRowContext(ctx, `SELECT route_id, row_id, embedded_content_hash, content_hash, embedding
		FROM mem_recall_units WHERE unit_no = ? AND database_id = ? AND table_id = ?`,
		unitNo, databaseID, tableID).Scan(&unit.routeID, &unit.rowID, &embeddedHash, &contentHash, &embedding)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(embedding) == 0 || embeddedHash == "" || embeddedHash != contentHash {
		return nil, nil
	}
	return &unit, nil
}

// tableNearest reads one Table's index, asking for more candidates than needed
// whenever the boundary is ambiguous or the first ones turn out to be stale.
func (t *tx) tableNearest(ctx context.Context, databaseID, tableID string, encoded []byte, limit int) ([]vectorCandidate, error) {
	ready, _, err := t.vecIndexReady(ctx, tableID)
	if err != nil {
		return nil, err
	}
	if !ready {
		return nil, nil
	}
	ask := limit * 2
	if ask < limit+8 {
		ask = limit + 8
	}
	for {
		rows, err := t.nearestRows(ctx, tableID, encoded, ask)
		if err != nil {
			return nil, err
		}
		exhausted := len(rows) < ask
		sort.Slice(rows, func(left, right int) bool {
			if rows[left].distance != rows[right].distance {
				return rows[left].distance < rows[right].distance
			}
			return rows[left].unitNo < rows[right].unitNo
		})
		live := 0
		for _, row := range rows {
			unit, err := t.readyUnit(ctx, databaseID, tableID, row.unitNo)
			if err != nil {
				return nil, err
			}
			if unit != nil {
				live++
			}
		}
		boundaryClear := len(rows) <= limit || rows[limit-1].distance < rows[limit].distance
		if exhausted || (live >= limit && boundaryClear) || ask >= maxVectorCandidates {
			return rows, nil
		}
		ask *= 2
	}
}

func (t *tx) nearestRows(ctx context.Context, tableID string, encoded []byte, k int) ([]vectorCandidate, error) {
	rows, err := t.q().QueryContext(ctx,
		`SELECT rowid, distance FROM `+vecIndexName(tableID)+` WHERE embedding MATCH ? AND k = ?`, encoded, k)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	candidates := []vectorCandidate{}
	for rows.Next() {
		candidate := vectorCandidate{tableID: tableID}
		if err := rows.Scan(&candidate.unitNo, &candidate.distance); err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}
