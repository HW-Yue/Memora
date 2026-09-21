package sqlstore

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
)

// Link is one entry of a row's links field (docs/product/row-links.md): which
// row it points at, that row's summary, and the revision the summary was taken
// from. Both ends of a link carry it, written in one transaction.
type Link struct {
	RelationID  string `json:"relation_id,omitempty"`
	Direction   string `json:"direction,omitempty"`
	RowID       string `json:"row_id"`
	DatabaseID  string `json:"database_id,omitempty"`
	TableID     string `json:"table_id,omitempty"`
	Summary     string `json:"summary"`
	Revision    uint64 `json:"revision"`
	Type        string `json:"type,omitempty"`
	Description string `json:"description,omitempty"`
}

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
	if value.State != row.StateLive {
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
		if err != nil || counterpart.State != row.StateLive {
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
		if err := t.appendHistory(ctx, target, counterpart, history.OperationUpdate, row.WriteMetadata{}); err != nil {
			return nil, err
		}
		detached = append(detached, counterpart.ID)
	}
	return detached, nil
}
