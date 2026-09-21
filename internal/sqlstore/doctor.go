package sqlstore

import (
	"context"

	"github.com/HW-Yue/Memora/internal/catalog"
)

// Report is the health summary `memora doctor` prints.
type Report struct {
	Status        string `json:"status"`
	Engine        string `json:"engine"`
	SQLiteVersion string `json:"sqlite_version"`
	Integrity     string `json:"integrity"`
	Databases     int    `json:"databases"`
	Tables        int    `json:"tables"`
	Rows          int    `json:"rows"`
	RouteNodes    int    `json:"route_nodes"`
	Changes       int    `json:"changes"`
	// The mount invariant — a live Row hangs under exactly one leaf whose
	// row_id points back at it — is enforced on the write path, so these count
	// data that predates the rule or a bug that got past it. A healthy Instance
	// reports zero for all three; see docs/planning/row-navigable.md.
	OrphanRows       int `json:"orphan_rows"`
	MultiLeafRows    int `json:"multi_leaf_rows"`
	MismatchedMounts int `json:"mismatched_mounts"`
}

func (db *DB) Doctor(ctx context.Context) (Report, error) {
	report := Report{Engine: "sqlite"}
	err := db.view(ctx, func(t *tx) error {
		if err := t.q().QueryRowContext(ctx, `SELECT sqlite_version()`).Scan(&report.SQLiteVersion); err != nil {
			return err
		}
		if err := t.q().QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&report.Integrity); err != nil {
			return err
		}
		databases, err := t.loadDatabases(ctx)
		if err != nil {
			return err
		}
		report.Databases = len(databases)
		for _, database := range databases {
			for _, table := range database.Tables {
				report.Tables++
				var rows, nodes int
				if err := t.q().QueryRowContext(ctx, `SELECT COUNT(*) FROM `+dataTable(table.ID)+` WHERE row_state = 'live'`).Scan(&rows); err != nil {
					return err
				}
				if err := t.q().QueryRowContext(ctx, `SELECT COUNT(*) FROM `+routeTable(table.ID)+` WHERE deprecated = 0`).Scan(&nodes); err != nil {
					return err
				}
				report.Rows += rows
				report.RouteNodes += nodes
				if err := t.countMountViolations(ctx, table, &report); err != nil {
					return err
				}
			}
		}
		return t.q().QueryRowContext(ctx, `SELECT COUNT(*) FROM mem_changes`).Scan(&report.Changes)
	})
	report.Status = "healthy"
	if err != nil || report.Integrity != "ok" {
		report.Status = "unhealthy"
	}
	return report, err
}

// countMountViolations adds the mount invariant's three failure shapes to the
// report: a live Row with no leaf, a live Row with several, and a live Row
// whose leaf does not point back at it. The last one is also where an older
// shape would surface — a leaf claimed by one Row while pointing at another.
//
// They are counted in SQL rather than by walking the Rows: a health check must
// not pull a table into memory, and json_array_length plus one join say exactly
// what the write path refuses.
func (t *tx) countMountViolations(ctx context.Context, table catalog.Table, report *Report) error {
	counts := []struct {
		target *int
		query  string
	}{
		{
			&report.OrphanRows,
			`SELECT COUNT(*) FROM ` + dataTable(table.ID) +
				` WHERE row_state = 'live' AND json_array_length(route_leaf_ids) = 0`,
		},
		{
			&report.MultiLeafRows,
			`SELECT COUNT(*) FROM ` + dataTable(table.ID) +
				` WHERE row_state = 'live' AND json_array_length(route_leaf_ids) > 1`,
		},
		{
			&report.MismatchedMounts,
			`SELECT COUNT(*) FROM ` + dataTable(table.ID) + ` AS d
				LEFT JOIN ` + routeTable(table.ID) + ` AS r ON r.route_id = json_extract(d.route_leaf_ids, '$[0]')
				WHERE d.row_state = 'live' AND json_array_length(d.route_leaf_ids) = 1
				AND (r.route_id IS NULL OR r.deprecated = 1 OR r.kind <> 'leaf'
					OR COALESCE(json_extract(r.body, '$.row_id'), '') <> d.row_id)`,
		},
	}
	for _, count := range counts {
		var value int
		if err := t.q().QueryRowContext(ctx, count.query).Scan(&value); err != nil {
			return err
		}
		*count.target += value
	}
	return nil
}
