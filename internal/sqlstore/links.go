package sqlstore

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/HW-Yue/Memora/internal/catalog"
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

// setLinkFields rewrites only the links column; a link change is not a content
// revision of the row.
func (t *tx) setLinks(ctx context.Context, table catalog.Table, rowID string, links []Link) error {
	if links == nil {
		links = []Link{}
	}
	_, err := t.q().ExecContext(ctx, `UPDATE `+dataTable(table.ID)+` SET links = ? WHERE row_id = ?`, encodeJSON(links), rowID)
	return err
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
