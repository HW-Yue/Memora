package sqlstore

import "context"

// Report is the health summary `memora doctor` prints.
type Report struct {
	Status        string `json:"status"`
	Engine        string `json:"engine"`
	SQLiteVersion string `json:"sqlite_version"`
	VecVersion    string `json:"vec_version"`
	Integrity     string `json:"integrity"`
	Databases     int    `json:"databases"`
	Tables        int    `json:"tables"`
	Rows          int    `json:"rows"`
	RouteNodes    int    `json:"route_nodes"`
	Changes       int    `json:"changes"`
	Vectors       int    `json:"vectors"`
	VectorSearch  bool   `json:"vector_search"`
}

func (db *DB) Doctor(ctx context.Context) (Report, error) {
	report := Report{Engine: "sqlite", VectorSearch: db.embedder != nil}
	err := db.view(ctx, func(t *tx) error {
		if err := t.q().QueryRowContext(ctx, `SELECT sqlite_version(), vec_version()`).Scan(&report.SQLiteVersion, &report.VecVersion); err != nil {
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
			}
		}
		if err := t.q().QueryRowContext(ctx, `SELECT COUNT(*) FROM mem_changes`).Scan(&report.Changes); err != nil {
			return err
		}
		return t.q().QueryRowContext(ctx, `SELECT COUNT(*) FROM mem_vector_items`).Scan(&report.Vectors)
	})
	report.Status = "healthy"
	if err != nil || report.Integrity != "ok" {
		report.Status = "unhealthy"
	}
	return report, err
}
