package sqlstore

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/history"
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
func (t *tx) syncLinks(ctx context.Context, databaseName string, table catalog.Table, value *storedRow, refs []rowmodel.LinkRef) error {
	if refs == nil {
		return nil
	}
	targets, err := t.resolveLinks(ctx, databaseName, table, value.ID, refs)
	if err != nil {
		return err
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
			return err
		}
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
			return err
		}
	}
	value.Links = linked
	return nil
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
