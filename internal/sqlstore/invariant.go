package sqlstore

import (
	"context"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/result"
	rowmodel "github.com/HW-Yue/Memora/internal/row"
)

// MountViolations counts the three ways the mount invariant can be broken: a
// live Row with no leaf, a live Row with several, and a live Row whose leaf does
// not point back at it. The doctor report and the commit guard read the same
// queries, so what an operator sees and what a write is held to cannot drift.
type MountViolations struct {
	OrphanRows       int
	MultiLeafRows    int
	MismatchedMounts int
}

func (violations MountViolations) Total() int {
	return violations.OrphanRows + violations.MultiLeafRows + violations.MismatchedMounts
}

// mountViolations counts them for one Table in SQL rather than by walking the
// Rows: the guard runs on every write a test Instance makes, and a health check
// must not pull a table into memory. json_array_length plus one join say exactly
// what the write path refuses.
func (t *tx) mountViolations(ctx context.Context, table catalog.Table) (MountViolations, error) {
	var violations MountViolations
	counts := []struct {
		target *int
		query  string
	}{
		{
			&violations.OrphanRows,
			`SELECT COUNT(*) FROM ` + dataTable(table.ID) +
				` WHERE row_state = 'live' AND json_array_length(route_leaf_ids) = 0`,
		},
		{
			&violations.MultiLeafRows,
			`SELECT COUNT(*) FROM ` + dataTable(table.ID) +
				` WHERE row_state = 'live' AND json_array_length(route_leaf_ids) > 1`,
		},
		{
			&violations.MismatchedMounts,
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
			return MountViolations{}, err
		}
		*count.target += value
	}
	return violations, nil
}

// linkScanPage bounds how many Rows the link check holds at once. The scan reads
// a page, closes its cursor, then point-reads the counterparts: holding one
// cursor open while opening another is how this kind of check deadlocks itself.
const linkScanPage = 200

// brokenLinks counts link entries that no longer hold: the target is gone or not
// live, or the target does not point back. Links are the one place this kernel
// stores the same fact twice, which is exactly why the one assertion site covers
// them too.
func (t *tx) brokenLinks(ctx context.Context, table catalog.Table) (int, error) {
	broken := 0
	after := ""
	for {
		rows, err := t.q().QueryContext(ctx, `SELECT row_id, links FROM `+dataTable(table.ID)+
			` WHERE row_state = 'live' AND row_id > ? ORDER BY row_id LIMIT ?`, after, linkScanPage)
		if err != nil {
			return 0, err
		}
		type pageRow struct{ id, links string }
		page := []pageRow{}
		for rows.Next() {
			entry := pageRow{}
			if err := rows.Scan(&entry.id, &entry.links); err != nil {
				_ = rows.Close()
				return 0, err
			}
			page = append(page, entry)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return 0, err
		}
		_ = rows.Close()
		if len(page) == 0 {
			return broken, nil
		}
		after = page[len(page)-1].id
		for _, entry := range page {
			links := []Link{}
			if err := decodeJSON(entry.links, &links); err != nil {
				return 0, err
			}
			pending := map[string]bool{}
			if len(links) > 0 {
				if pending, err = t.pendingRepairs(ctx, table.ID, entry.id); err != nil {
					return 0, err
				}
			}
			for _, link := range links {
				counterpartTable := table
				if link.TableID != "" && link.TableID != table.ID {
					resolved, err := t.tableByID(ctx, link.TableID)
					if err != nil {
						broken++
						continue
					}
					counterpartTable = resolved
				}
				counterpart, err := t.readRow(ctx, counterpartTable, link.RowID)
				if err != nil || counterpart.State != rowmodel.StateLive {
					// A link to a Row that is gone or superseded is legitimate
					// while it is queued for repair, and broken once it is not:
					// that is what keeps the queue honest instead of decorative.
					if !pending[linkKey(counterpartTable.ID, link.RowID)] {
						broken++
					}
					continue
				}
				answered := false
				for _, reverse := range counterpart.Links {
					if reverse.RowID == entry.id &&
						(reverse.TableID == "" || reverse.TableID == table.ID) {
						answered = true
					}
				}
				if !answered {
					broken++
				}
			}
		}
	}
}

// requireMountInvariant is the single place the mount invariant is asserted on a
// write, at the one commit point both autocommit and explicit transactions pass
// through. A test Instance opens with it on, so a path that leaves a live Row
// outside the one-to-one mount fails at the commit that produced it instead of
// surfacing later through doctor. A production Instance leaves it off and relies
// on the doctor command: it costs three queries per Table per write.
func (t *tx) requireMountInvariant(ctx context.Context) error {
	databases, err := t.loadDatabases(ctx)
	if err != nil {
		return err
	}
	for _, database := range databases {
		for _, table := range database.Tables {
			violations, err := t.mountViolations(ctx, table)
			if err != nil {
				return err
			}
			if violations.Total() > 0 {
				return fail(result.CodeInternal,
					"mount invariant violated in %s.%s: %d live rows with no leaf, %d with several, %d whose leaf points elsewhere",
					database.Name, table.Name, violations.OrphanRows, violations.MultiLeafRows, violations.MismatchedMounts)
			}
			units, err := t.brokenRecallUnits(ctx, table)
			if err != nil {
				return err
			}
			if units > 0 {
				return fail(result.CodeInternal,
					"recall index violated in %s.%s: %d units disagree with the live Rows",
					database.Name, table.Name, units)
			}
			broken, err := t.brokenLinks(ctx, table)
			if err != nil {
				return err
			}
			if broken > 0 {
				return fail(result.CodeInternal,
					"link invariant violated in %s.%s: %d entries are dangling or one-sided",
					database.Name, table.Name, broken)
			}
		}
	}
	return nil
}
