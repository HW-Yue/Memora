package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/repair"
	"github.com/HW-Yue/Memora/internal/result"
	rowmodel "github.com/HW-Yue/Memora/internal/row"
)

// Link and LinkRef are the Row model's own types; the storage layer keeps its
// names for them because that is what its callers and tests use.
type (
	Link    = rowmodel.Link
	LinkRef = rowmodel.LinkRef
)

const maxSummaryRunes = 280

// summarize is what the other end of a link stores about a row: its title
// and summary columns when it has them, otherwise its first text values.
func summarize(table catalog.Table, value storedRow) string {
	parts := []string{}
	for _, role := range []string{"title", "summary"} {
		for _, column := range table.Columns {
			if canonical(column.SemanticRole) == role && !column.Archived() {
				if text, ok := value.Values[column.ID].(string); ok && strings.TrimSpace(text) != "" {
					parts = append(parts, strings.TrimSpace(text))
				}
			}
		}
	}
	if len(parts) == 0 {
		for _, column := range table.Columns {
			if text, ok := value.Values[column.ID].(string); ok && strings.TrimSpace(text) != "" && !column.Archived() {
				parts = append(parts, strings.TrimSpace(text))
				if len(parts) == 2 {
					break
				}
			}
		}
	}
	summary := strings.Join(parts, " — ")
	if utf8.RuneCountInString(summary) > maxSummaryRunes {
		runes := []rune(summary)
		summary = string(runes[:maxSummaryRunes]) + "…"
	}
	return summary
}

func (t *tx) endpointRow(ctx context.Context, databaseName, tableName, rowID string) (catalog.Table, storedRow, error) {
	table, err := t.liveTable(ctx, databaseName, tableName)
	if err != nil {
		return catalog.Table{}, storedRow{}, err
	}
	value, err := t.readRow(ctx, table, rowID)
	if err != nil {
		return catalog.Table{}, storedRow{}, err
	}
	if value.State != rowmodel.StateLive {
		return catalog.Table{}, storedRow{}, fail(result.CodeNotFound, "row %q was not found", rowID)
	}
	return table, value, nil
}

// RowLinks returns a row's links with stale summaries refreshed in memory. A
// summary is stale when the linked row has moved past the revision it was
// taken from; the stored copy is rewritten lazily by RefreshLinkSummaries.
func (db *DB) RowLinks(ctx context.Context, databaseName, tableName, rowID string) ([]Link, error) {
	var links []Link
	err := db.view(ctx, func(t *tx) error {
		_, value, err := t.endpointRow(ctx, databaseName, tableName, rowID)
		if err != nil {
			return err
		}
		links = value.Links
		for index := range links {
			table, err := t.tableByID(ctx, links[index].TableID)
			if err != nil {
				continue
			}
			other, err := t.readRow(ctx, table, links[index].RowID)
			if err == nil && other.Revision != links[index].Revision {
				links[index].Summary = summarize(table, other)
				links[index].Revision = other.Revision
			}
		}
		return nil
	})
	return links, err
}

// maxCascadeDetach bounds how many Rows one delete may detach a link from. The
// mount invariant got its bound from the write path; links have none of their
// own, so a delete needs one of its own rather than borrowing the target-Row
// budget — see docs/product/row-delete-archive.md「级联写的预算口径」.
const maxCascadeDetach = 1000

// detachCounterpartLinks removes the other end of every link the deleted Row
// carried. A link is stored at both ends, so the Row's own list names everyone
// pointing at it; walking it is the reverse index, and doing it here is what
// keeps the two directions equal once the Row is gone. The Rows that lose an
// entry are ordinary in-place modifications: revision up, history appended.
func (t *tx) detachCounterpartLinks(ctx context.Context, table catalog.Table, value storedRow) ([]string, error) {
	if len(value.Links) == 0 {
		return nil, nil
	}
	if len(value.Links) > maxCascadeDetach {
		return nil, fail(result.CodeConstraint,
			"deleting this Row would detach %d links; the limit is %d", len(value.Links), maxCascadeDetach)
	}
	detached := []string{}
	for _, link := range value.Links {
		if link.RowID == "" || link.RowID == value.ID {
			continue
		}
		target := table
		if link.TableID != "" && link.TableID != table.ID {
			// A link may cross tables: RowIDs are unique Instance-wide. A table
			// that no longer exists simply has nothing left to detach.
			resolved, err := t.tableByID(ctx, link.TableID)
			if err != nil {
				continue
			}
			target = resolved
		}
		counterpart, err := t.readRow(ctx, target, link.RowID)
		if err != nil || counterpart.State != rowmodel.StateLive {
			continue
		}
		remaining := make([]Link, 0, len(counterpart.Links))
		for _, candidate := range counterpart.Links {
			if candidate.RowID == value.ID &&
				(candidate.TableID == "" || candidate.TableID == table.ID) {
				continue
			}
			remaining = append(remaining, candidate)
		}
		if len(remaining) == len(counterpart.Links) {
			continue
		}
		counterpart.Links = remaining
		if err := t.advance(ctx, target, &counterpart); err != nil {
			return nil, err
		}
		if err := t.writeRow(ctx, target, counterpart, false); err != nil {
			return nil, err
		}
		if err := t.appendHistory(ctx, target, counterpart, history.OperationUpdate, rowmodel.WriteMetadata{}, nil); err != nil {
			return nil, err
		}
		detached = append(detached, counterpart.ID)
	}
	return detached, nil
}

// linkTarget is one resolved link reference: where the other Row lives and what
// it currently holds, read once so both directions describe the same revision.
type linkTarget struct {
	Table catalog.Table
	Row   storedRow
}

func linkKey(tableID, rowID string) string { return tableID + "|" + rowID }

// resolveLinks turns references into live Rows. A bare RowID resolves inside the
// Row's own Table first: a RowID is unique Instance-wide, but uniqueness is not
// addressability, so an ID that resolves nowhere is refused with a hint to name
// the Table rather than guessed at. Links stay inside one Database, because the
// caller's authorization is per Database.
func (t *tx) resolveLinks(ctx context.Context, databaseName string, table catalog.Table, actingRowID string, refs []rowmodel.LinkRef) ([]linkTarget, error) {
	targets := []linkTarget{}
	seen := map[string]bool{}
	for _, ref := range refs {
		if strings.TrimSpace(ref.RowID) == "" {
			return nil, fail(result.CodeValidation, "a link needs the RowID it points at")
		}
		if ref.RowID == actingRowID {
			return nil, fail(result.CodeValidation, "a Row cannot link to itself")
		}
		target := table
		if ref.Table != "" && !strings.EqualFold(strings.TrimSpace(ref.Table), table.Name) {
			resolved, err := t.liveTable(ctx, databaseName, strings.TrimSpace(ref.Table))
			if err != nil {
				return nil, err
			}
			target = resolved
		}
		if target.DatabaseID != table.DatabaseID {
			return nil, fail(result.CodeValidation,
				"links stay inside one Database: %q is in another", target.Name)
		}
		row, err := t.readRow(ctx, target, ref.RowID)
		if err != nil {
			if ref.Table == "" {
				return nil, fail(result.CodeNotFound,
					"no Row %q in %s: name its Table to link across Tables", ref.RowID, table.Name)
			}
			return nil, fail(result.CodeNotFound, "Row %q was not found in %s", ref.RowID, target.Name)
		}
		if row.State != rowmodel.StateLive {
			return nil, fail(result.CodeConstraint, "Row %q is not live, so nothing can link to it", ref.RowID)
		}
		if seen[linkKey(target.ID, row.ID)] {
			continue
		}
		seen[linkKey(target.ID, row.ID)] = true
		targets = append(targets, linkTarget{Table: target, Row: row})
	}
	return targets, nil
}

// syncLinks makes a Row's link membership match the references it was given.
//
// A link lives at both ends, so every addition and removal is written twice in
// the caller's transaction. The other Row's list is edited in place rather than
// recomputed from this snapshot, or this write would wipe the links it holds to
// everyone else. Summaries and revisions are written at the moment of the write;
// they are staleness markers, never refreshed in cascade here.
// It returns the counterpart endpoints it rewrote, because those summaries are
// already fresh: queueing them would make a link write schedule its own repair.
func (t *tx) syncLinks(ctx context.Context, databaseName string, table catalog.Table, value *storedRow, refs []rowmodel.LinkRef) (map[string]bool, error) {
	refreshed := map[string]bool{}
	if refs == nil {
		return refreshed, nil
	}
	targets, err := t.resolveLinks(ctx, databaseName, table, value.ID, refs)
	if err != nil {
		return nil, err
	}
	current := map[string]Link{}
	for _, link := range value.Links {
		current[linkKey(link.TableID, link.RowID)] = link
	}
	keep := map[string]bool{}
	linked := []Link{}
	for _, target := range targets {
		key := linkKey(target.Table.ID, target.Row.ID)
		keep[key] = true
		if existing, present := current[key]; present {
			linked = append(linked, existing)
			continue
		}
		revision, err := t.attachCounterpart(ctx, table, *value, target)
		if err != nil {
			return nil, err
		}
		refreshed[linkKey(target.Table.ID, target.Row.ID)] = true
		linked = append(linked, Link{
			RowID: target.Row.ID, DatabaseID: target.Table.DatabaseID, TableID: target.Table.ID,
			Table: target.Table.Name, Summary: summarize(target.Table, target.Row), Revision: revision,
		})
	}
	for key, link := range current {
		if keep[key] {
			continue
		}
		if err := t.detachOneLink(ctx, table, *value, link); err != nil {
			return nil, err
		}
		refreshed[linkKey(link.TableID, link.RowID)] = true
	}
	value.Links = linked
	return refreshed, nil
}

// attachCounterpart adds this Row's side to the other Row and returns the
// revision that Row now sits at, which is what the summary here describes.
func (t *tx) attachCounterpart(ctx context.Context, table catalog.Table, value storedRow, target linkTarget) (uint64, error) {
	counterpart := target.Row
	counterpart.Links = append(withoutLink(counterpart.Links, table.ID, value.ID), Link{
		RowID: value.ID, DatabaseID: table.DatabaseID, TableID: table.ID, Table: table.Name,
		Summary: summarize(table, value), Revision: value.Revision,
	})
	if err := t.advance(ctx, target.Table, &counterpart); err != nil {
		return 0, err
	}
	if err := t.writeRow(ctx, target.Table, counterpart, false); err != nil {
		return 0, err
	}
	if err := t.appendHistory(ctx, target.Table, counterpart, history.OperationUpdate, rowmodel.WriteMetadata{}, nil); err != nil {
		return 0, err
	}
	return counterpart.Revision, nil
}

// detachOneLink removes the other end of one link. A counterpart that is gone or
// no longer live has nothing left to detach.
func (t *tx) detachOneLink(ctx context.Context, table catalog.Table, value storedRow, link Link) error {
	target := table
	if link.TableID != "" && link.TableID != table.ID {
		resolved, err := t.tableByID(ctx, link.TableID)
		if err != nil {
			return nil
		}
		target = resolved
	}
	counterpart, err := t.readRow(ctx, target, link.RowID)
	if err != nil || counterpart.State != rowmodel.StateLive {
		return nil
	}
	remaining := withoutLink(counterpart.Links, table.ID, value.ID)
	if len(remaining) == len(counterpart.Links) {
		return nil
	}
	counterpart.Links = remaining
	if err := t.advance(ctx, target, &counterpart); err != nil {
		return err
	}
	if err := t.writeRow(ctx, target, counterpart, false); err != nil {
		return err
	}
	return t.appendHistory(ctx, target, counterpart, history.OperationUpdate, rowmodel.WriteMetadata{}, nil)
}

func withoutLink(links []Link, tableID, rowID string) []Link {
	remaining := make([]Link, 0, len(links))
	for _, link := range links {
		if link.RowID == rowID && (link.TableID == "" || link.TableID == tableID) {
			continue
		}
		remaining = append(remaining, link)
	}
	return remaining
}

// The repair vocabulary lives in package repair; these aliases are what the
// storage layer and its tests call it.
const (
	RepairStaleSummary   = repair.StaleSummary
	RepairStaleReference = repair.StaleReference
)

// enqueueRepair records that one link endpoint needs repair. The queue is an
// ordinary table keyed by the endpoint, so queueing the same one twice is a
// no-op and a crash mid-repair simply leaves it queued.
func (t *tx) enqueueRepair(ctx context.Context, table catalog.Table, holderRowID, counterpartTableID, counterpartRowID, reason string) error {
	_, err := t.q().ExecContext(ctx, `INSERT OR IGNORE INTO mem_repairs
		(database_id, table_id, row_id, counterpart_table_id, counterpart_row_id, reason, queued_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		table.DatabaseID, table.ID, holderRowID, counterpartTableID, counterpartRowID, reason, formatTime(t.now))
	return err
}

// pendingRepairs lists the endpoints a Row already has queued, so the invariant
// can tell "waiting for repair" apart from "broken".
func (t *tx) pendingRepairs(ctx context.Context, tableID, rowID string) (map[string]bool, error) {
	rows, err := t.q().QueryContext(ctx, `SELECT counterpart_table_id, counterpart_row_id FROM mem_repairs
		WHERE table_id = ? AND row_id = ?`, tableID, rowID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	pending := map[string]bool{}
	for rows.Next() {
		var counterpartTable, counterpartRow string
		if err := rows.Scan(&counterpartTable, &counterpartRow); err != nil {
			return nil, err
		}
		pending[linkKey(counterpartTable, counterpartRow)] = true
	}
	return pending, rows.Err()
}

// enqueueInboundRepairs queues every endpoint that still points at a Row. The
// writer does this inside its own transaction: a reader cannot write (writers
// are serialised), and waiting for someone to read would leave the queue empty
// across a crash. A Row's own links are the reverse inventory — a link is stored
// at both ends, so whoever points here is listed here.
func (t *tx) enqueueInboundRepairs(ctx context.Context, table catalog.Table, value storedRow, reason string, skip map[string]bool) error {
	for _, link := range value.Links {
		counterpartTable := table.ID
		if link.TableID != "" {
			counterpartTable = link.TableID
		}
		if skip[linkKey(counterpartTable, link.RowID)] {
			continue
		}
		if err := t.enqueueRepair(ctx, table, link.RowID, counterpartTable, value.ID, reason); err != nil {
			return err
		}
	}
	return nil
}

// RepairReceipt reports what one bounded repair pass did. The executor speaks
// this shape without importing the storage layer.
type RepairReceipt = repair.Receipt

// maxRepairHops bounds following a successor chain, which a reshape can extend.
const maxRepairHops = 16

// RepairLinks drains a bounded batch of queued endpoints. Every entry is
// re-checked before it is applied: the queue records what was true when it was
// written, and a repair that no longer has anything to do is discarded rather
// than applied blindly.
func (t *tx) repairLinks(ctx context.Context, databaseName string, limit int) (RepairReceipt, error) {
	receipt := RepairReceipt{}
	database, err := t.resolveDatabase(ctx, databaseName)
	if err != nil {
		return receipt, err
	}
	databaseID := database.ID
	type queued struct {
		tableID, rowID, counterpartTableID, counterpartRowID, reason string
	}
	rows, err := t.q().QueryContext(ctx, `SELECT table_id, row_id, counterpart_table_id, counterpart_row_id, reason
		FROM mem_repairs WHERE database_id = ? ORDER BY queued_at, row_id LIMIT ?`, databaseID, limit+1)
	if err != nil {
		return receipt, err
	}
	entries := []queued{}
	for rows.Next() {
		entry := queued{}
		if err := rows.Scan(&entry.tableID, &entry.rowID, &entry.counterpartTableID,
			&entry.counterpartRowID, &entry.reason); err != nil {
			_ = rows.Close()
			return receipt, err
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return receipt, err
	}
	_ = rows.Close()
	if len(entries) > limit {
		receipt.Remaining = len(entries) - limit
		entries = entries[:limit]
	}

	receipt.Failures = []repair.Failure{}
	for _, entry := range entries {
		outcome, err := t.repairEndpoint(ctx, entry.tableID, entry.rowID, entry.counterpartTableID,
			entry.counterpartRowID, entry.reason)
		if err != nil {
			return receipt, err
		}
		// An entry leaves the queue only when this pass decided it: applied, or
		// judged no longer worth applying. An endpoint that could not be read
		// was decided neither way, so it stays queued and stays reported — the
		// alternative is a pass that reports a clean queue it never repaired.
		if outcome.problem != nil {
			receipt.Failed++
			receipt.Failures = append(receipt.Failures, repair.Failure{
				RowID: entry.rowID, CounterpartRowID: entry.counterpartRowID,
				Reason: entry.reason, Message: outcome.problem.Error(),
			})
			continue
		}
		if outcome.applied {
			receipt.Repaired++
		} else {
			receipt.Discarded++
		}
		if _, err := t.q().ExecContext(ctx, `DELETE FROM mem_repairs
			WHERE table_id = ? AND row_id = ? AND counterpart_table_id = ? AND counterpart_row_id = ?`,
			entry.tableID, entry.rowID, entry.counterpartTableID, entry.counterpartRowID); err != nil {
			return receipt, err
		}
	}
	var remaining int
	if err := t.q().QueryRowContext(ctx, `SELECT COUNT(*) FROM mem_repairs WHERE database_id = ?`,
		databaseID).Scan(&remaining); err != nil {
		return receipt, err
	}
	receipt.Remaining = remaining
	return receipt, nil
}

// repairEndpoint applies one queued endpoint and reports whether it changed
// anything. Writes here go straight to the Row: the repair deliberately does not
// queue the endpoints it touches, or repairing one link would queue the next
// repair forever.
// endpointOutcome is what one queued entry came to. applied and the zero value
// are both decisions — repaired, or discarded. problem is neither: the pass
// could not read an endpoint, and a read that failed says nothing about whether
// the link is still legitimate, so it decides nothing.
type endpointOutcome struct {
	applied bool
	problem error
}

func (t *tx) repairEndpoint(ctx context.Context, tableID, holderID, counterpartTableID, counterpartID, reason string) (endpointOutcome, error) {
	holderTable, err := t.tableByID(ctx, tableID)
	if err != nil {
		return unreadable(err), nil
	}
	holder, err := t.readRow(ctx, holderTable, holderID)
	if err != nil {
		return unreadable(err), nil
	}
	if holder.State != rowmodel.StateLive {
		return endpointOutcome{}, nil
	}
	counterpartTable := holderTable
	if counterpartTableID != "" && counterpartTableID != tableID {
		resolved, err := t.tableByID(ctx, counterpartTableID)
		if err != nil {
			return unreadable(err), nil
		}
		counterpartTable = resolved
	}
	counterpart, counterpartErr := t.readRow(ctx, counterpartTable, counterpartID)
	if counterpartErr != nil {
		if !missing(counterpartErr) {
			return unreadable(counterpartErr), nil
		}
		// The counterpart is really not there: the link has nothing to point at
		// any more. This is the only disappearance a repair may act on.
		applied, err := t.dropHolderLink(ctx, holderTable, holder, counterpartTable.ID, counterpartID)
		return endpointOutcome{applied: applied}, err
	}

	switch reason {
	case RepairStaleReference:
		terminals, err := t.successorTail(ctx, counterpartTable, counterpart)
		if err != nil {
			return unreadable(err), nil
		}
		applied, err := t.repointLink(ctx, holderTable, holder, counterpartTable, counterpart, terminals)
		return endpointOutcome{applied: applied}, err
	case RepairStaleSummary:
		if counterpart.State != rowmodel.StateLive {
			terminals, err := t.successorTail(ctx, counterpartTable, counterpart)
			if err != nil {
				return unreadable(err), nil
			}
			applied, err := t.repointLink(ctx, holderTable, holder, counterpartTable, counterpart, terminals)
			return endpointOutcome{applied: applied}, err
		}
		applied, err := t.refreshSummary(ctx, holderTable, holder, counterpartTable, counterpart)
		return endpointOutcome{applied: applied}, err
	default:
		return endpointOutcome{}, nil
	}
}

// unreadable turns a failed read into an outcome. An object that is not there
// is a decision — the endpoint is gone, so the entry is discarded; anything
// else is storage this transaction could not read, and the entry waits.
func unreadable(err error) endpointOutcome {
	if missing(err) {
		return endpointOutcome{}
	}
	return endpointOutcome{problem: err}
}

// missing reports whether err is the storage layer saying the object is not
// there. Nothing else counts as gone: a value that does not decode, or a read
// that failed, is storage this transaction cannot read — which says nothing
// about whether the Row, the Table or the link still exists.
func missing(err error) bool {
	if errors.Is(err, sql.ErrNoRows) {
		return true
	}
	var stable interface{ StableCode() string }
	if errors.As(err, &stable) {
		return result.Code(stable.StableCode()) == result.CodeNotFound
	}
	return false
}

// successorTail follows a superseded Row to the Rows that stand for it now.
func (t *tx) successorTail(ctx context.Context, table catalog.Table, value storedRow) ([]linkTarget, error) {
	frontier := []string{value.ID}
	visited := map[string]bool{value.ID: true}
	terminal := []linkTarget{}
	for hops := 0; hops < maxRepairHops && len(frontier) > 0; hops++ {
		next := []string{}
		for _, id := range frontier {
			current, err := t.readRow(ctx, table, id)
			if err != nil {
				// A successor that is not there ends that branch of the walk. A
				// successor that cannot be read ends the whole repair instead:
				// repointing on a partial tail would drop the endpoint it could
				// not see.
				if missing(err) {
					continue
				}
				return nil, err
			}
			if current.State == rowmodel.StateSuperseded && len(current.SuccessorIDs) > 0 {
				for _, successor := range current.SuccessorIDs {
					if visited[successor] {
						continue
					}
					visited[successor] = true
					next = append(next, successor)
				}
				continue
			}
			if current.State == rowmodel.StateLive {
				terminal = append(terminal, linkTarget{Table: table, Row: current})
			}
		}
		frontier = next
	}
	return terminal, nil
}

// repointLink moves one endpoint from a superseded Row to whatever stands for it
// now: the old back edge goes, and every terminal gains the link. Both ends of
// every edge are written together, so the repair cannot leave a stale summary of
// its own making behind.
func (t *tx) repointLink(ctx context.Context, holderTable catalog.Table, holder storedRow,
	oldTable catalog.Table, old storedRow, terminals []linkTarget) (bool, error) {
	changed := false
	holder.Links = withoutLink(holder.Links, oldTable.ID, old.ID)
	if err := t.advance(ctx, holderTable, &holder); err != nil {
		return false, err
	}
	for _, terminal := range terminals {
		terminal.Row.Links = append(withoutLink(terminal.Row.Links, holderTable.ID, holder.ID), Link{
			RowID: holder.ID, DatabaseID: holderTable.DatabaseID, TableID: holderTable.ID,
			Table: holderTable.Name, Summary: summarize(holderTable, holder), Revision: holder.Revision,
		})
		if err := t.advance(ctx, terminal.Table, &terminal.Row); err != nil {
			return false, err
		}
		if err := t.writeRow(ctx, terminal.Table, terminal.Row, false); err != nil {
			return false, err
		}
		if err := t.appendHistory(ctx, terminal.Table, terminal.Row, history.OperationUpdate, rowmodel.WriteMetadata{}, nil); err != nil {
			return false, err
		}
		holder.Links = append(holder.Links, Link{
			RowID: terminal.Row.ID, DatabaseID: terminal.Table.DatabaseID, TableID: terminal.Table.ID,
			Table: terminal.Table.Name, Summary: summarize(terminal.Table, terminal.Row), Revision: terminal.Row.Revision,
		})
		changed = true
	}
	if old.State == rowmodel.StateSuperseded {
		old.Links = withoutLink(old.Links, holderTable.ID, holder.ID)
		if err := t.advance(ctx, oldTable, &old); err != nil {
			return false, err
		}
		if err := t.writeRow(ctx, oldTable, old, false); err != nil {
			return false, err
		}
	}
	if err := t.writeRow(ctx, holderTable, holder, false); err != nil {
		return false, err
	}
	if err := t.appendHistory(ctx, holderTable, holder, history.OperationUpdate, rowmodel.WriteMetadata{}, nil); err != nil {
		return false, err
	}
	return changed, nil
}

// refreshSummary rewrites one endpoint's summary from the counterpart's current
// revision, and the counterpart's entry about the holder at the same time: two
// summaries written together cannot make each other stale.
func (t *tx) refreshSummary(ctx context.Context, holderTable catalog.Table, holder storedRow,
	counterpartTable catalog.Table, counterpart storedRow) (bool, error) {
	holderRevision := holder.Revision + 1
	counterpartRevision := counterpart.Revision + 1

	holder.Links = replaceLink(holder.Links, counterpartTable.ID, Link{
		RowID: counterpart.ID, DatabaseID: counterpartTable.DatabaseID, TableID: counterpartTable.ID,
		Table: counterpartTable.Name, Summary: summarize(counterpartTable, counterpart),
		Revision: counterpartRevision,
	})
	counterpart.Links = replaceLink(counterpart.Links, holderTable.ID, Link{
		RowID: holder.ID, DatabaseID: holderTable.DatabaseID, TableID: holderTable.ID,
		Table: holderTable.Name, Summary: summarize(holderTable, holder), Revision: holderRevision,
	})
	if err := t.advance(ctx, counterpartTable, &counterpart); err != nil {
		return false, err
	}
	if err := t.writeRow(ctx, counterpartTable, counterpart, false); err != nil {
		return false, err
	}
	if err := t.advance(ctx, holderTable, &holder); err != nil {
		return false, err
	}
	if err := t.writeRow(ctx, holderTable, holder, false); err != nil {
		return false, err
	}
	if err := t.appendHistory(ctx, holderTable, holder, history.OperationUpdate, rowmodel.WriteMetadata{}, nil); err != nil {
		return false, err
	}
	return true, nil
}

// dropHolderLink removes an endpoint whose counterpart no longer exists.
func (t *tx) dropHolderLink(ctx context.Context, holderTable catalog.Table, holder storedRow,
	counterpartTableID, counterpartID string) (bool, error) {
	remaining := withoutLink(holder.Links, counterpartTableID, counterpartID)
	if len(remaining) == len(holder.Links) {
		return false, nil
	}
	holder.Links = remaining
	if err := t.advance(ctx, holderTable, &holder); err != nil {
		return false, err
	}
	if err := t.writeRow(ctx, holderTable, holder, false); err != nil {
		return false, err
	}
	if err := t.appendHistory(ctx, holderTable, holder, history.OperationUpdate, rowmodel.WriteMetadata{}, nil); err != nil {
		return false, err
	}
	return true, nil
}

// replaceLink rewrites the entry pointing at tableID, or appends it when there
// is none, which is what "refresh" means for one side of a link.
func replaceLink(links []Link, tableID string, replacement Link) []Link {
	updated := make([]Link, 0, len(links)+1)
	replaced := false
	for _, link := range links {
		if link.RowID == replacement.RowID && (link.TableID == "" || link.TableID == tableID) {
			if !replaced {
				updated = append(updated, replacement)
				replaced = true
			}
			continue
		}
		updated = append(updated, link)
	}
	if !replaced {
		updated = append(updated, replacement)
	}
	return updated
}
