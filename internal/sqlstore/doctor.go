package sqlstore

import "context"

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
	// BrokenLinks counts entries that are dangling or one-sided: links are the
	// one place the same fact is stored twice, so they get a count too.
	BrokenLinks int `json:"broken_links"`
	// BrokenRecallUnits counts units that disagree with the live Rows: one for a
	// Row that is gone, or a live Row with no unit.
	BrokenRecallUnits int `json:"broken_recall_units"`
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
				violations, err := t.mountViolations(ctx, table)
				if err != nil {
					return err
				}
				report.OrphanRows += violations.OrphanRows
				report.MultiLeafRows += violations.MultiLeafRows
				report.MismatchedMounts += violations.MismatchedMounts
				broken, err := t.brokenLinks(ctx, table)
				if err != nil {
					return err
				}
				report.BrokenLinks += broken
				units, err := t.brokenRecallUnits(ctx, table)
				if err != nil {
					return err
				}
				report.BrokenRecallUnits += units
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
