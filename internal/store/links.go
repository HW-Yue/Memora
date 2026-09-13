package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/change"
	"github.com/HW-Yue/Memora/internal/relation"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
)

// Link is one entry of a row's links field (docs/product/row-links.md): which
// row it points at, that row's summary, and the revision the summary was taken
// from. Both ends of a link carry it, written in one transaction.
type Link struct {
	RelationID  string `json:"relation_id"`
	Direction   string `json:"direction"`
	RowID       string `json:"row_id"`
	DatabaseID  string `json:"database_id"`
	TableID     string `json:"table_id"`
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

func (t *tx) endpointRow(ctx context.Context, endpoint row.RelationEndpoint) (catalog.Table, storedRow, error) {
	table, err := t.liveTable(ctx, endpoint.Database, endpoint.Table)
	if err != nil {
		return catalog.Table{}, storedRow{}, err
	}
	value, err := t.readRow(ctx, table, endpoint.RowID)
	if err != nil {
		return catalog.Table{}, storedRow{}, err
	}
	if value.State != row.StateLive {
		return catalog.Table{}, storedRow{}, fail(result.CodeNotFound, "row %q was not found", endpoint.RowID)
	}
	return table, value, nil
}

func (t *tx) saveRelation(ctx context.Context, value relation.Relation) error {
	_, err := t.q().ExecContext(ctx, `INSERT INTO mem_links(relation_id, body) VALUES (?, ?)
		ON CONFLICT(relation_id) DO UPDATE SET body = excluded.body`, value.ID, encodeJSON(value))
	return err
}

func (t *tx) loadRelation(ctx context.Context, id string) (relation.Relation, error) {
	var body string
	err := t.q().QueryRowContext(ctx, `SELECT body FROM mem_links WHERE relation_id = ?`, id).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return relation.Relation{}, fail(result.CodeNotFound, "relation %q was not found", id)
	}
	if err != nil {
		return relation.Relation{}, err
	}
	var value relation.Relation
	return value, decodeJSON(body, &value)
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

func (t *tx) relate(ctx context.Context, definition row.RelationDefinition) (relation.Relation, error) {
	kind := strings.TrimSpace(definition.Type)
	if kind == "" {
		kind = "related"
	}
	sourceTable, source, err := t.endpointRow(ctx, definition.Source)
	if err != nil {
		return relation.Relation{}, err
	}
	targetTable, target, err := t.endpointRow(ctx, definition.Target)
	if err != nil {
		return relation.Relation{}, err
	}
	if source.ID == target.ID {
		return relation.Relation{}, fail(result.CodeConstraint, "a row cannot link to itself")
	}
	for _, link := range source.Links {
		if link.Direction == "outgoing" && link.RowID == target.ID && link.Type == kind {
			return relation.Relation{}, fail(result.CodeAlreadyExists, "row %q already links to %q as %q", source.ID, target.ID, kind)
		}
	}
	sequence, err := t.commitSequence(ctx)
	if err != nil {
		return relation.Relation{}, err
	}
	value := relation.Relation{
		Version: relation.Version, ID: newID("rel_"),
		Source: relation.Endpoint{DatabaseID: sourceTable.DatabaseID, TableID: sourceTable.ID, RowID: source.ID},
		Target: relation.Endpoint{DatabaseID: targetTable.DatabaseID, TableID: targetTable.ID, RowID: target.ID},
		Type:   kind, Description: definition.Description, Revision: 1, CommitSequence: sequence,
		State: relation.StateLive, CreatedAt: t.now, UpdatedAt: t.now,
	}
	source.Links = append(source.Links, Link{
		RelationID: value.ID, Direction: "outgoing", RowID: target.ID, DatabaseID: targetTable.DatabaseID,
		TableID: targetTable.ID, Summary: summarize(targetTable, target), Revision: target.Revision,
		Type: kind, Description: definition.Description,
	})
	target.Links = append(target.Links, Link{
		RelationID: value.ID, Direction: "incoming", RowID: source.ID, DatabaseID: sourceTable.DatabaseID,
		TableID: sourceTable.ID, Summary: summarize(sourceTable, source), Revision: source.Revision,
		Type: kind, Description: definition.Description,
	})
	if err := t.setLinks(ctx, sourceTable, source.ID, source.Links); err != nil {
		return relation.Relation{}, err
	}
	if err := t.setLinks(ctx, targetTable, target.ID, target.Links); err != nil {
		return relation.Relation{}, err
	}
	if err := t.saveRelation(ctx, value); err != nil {
		return relation.Relation{}, err
	}
	t.recordEntry(change.Entry{
		ObjectKind: change.ObjectRelation, DatabaseID: sourceTable.DatabaseID, TableID: sourceTable.ID,
		ObjectID: value.ID, Operation: change.OperationInsert, AfterRevision: 1,
		RelatedObjectIDs: dedupe([]string{source.ID, target.ID}),
	}, change.Metadata{Actor: "system:links", Source: "msql", Reason: "link rows"})
	return value, nil
}

func (t *tx) deleteRelation(ctx context.Context, id string, expected uint64) (relation.Relation, error) {
	value, err := t.loadRelation(ctx, id)
	if err != nil {
		return relation.Relation{}, err
	}
	if value.State != relation.StateLive {
		return relation.Relation{}, fail(result.CodeNotFound, "relation %q was not found", id)
	}
	if expected != 0 && expected != value.Revision {
		return relation.Relation{}, fail(result.CodeRevisionConflict, "relation %q is at revision %d, not %d", id, value.Revision, expected)
	}
	for _, endpoint := range []relation.Endpoint{value.Source, value.Target} {
		table, err := t.tableByID(ctx, endpoint.TableID)
		if err != nil {
			return relation.Relation{}, err
		}
		stored, err := t.readRow(ctx, table, endpoint.RowID)
		if err != nil {
			continue
		}
		kept := stored.Links[:0]
		for _, link := range stored.Links {
			if link.RelationID != id {
				kept = append(kept, link)
			}
		}
		if err := t.setLinks(ctx, table, stored.ID, kept); err != nil {
			return relation.Relation{}, err
		}
	}
	value.Revision++
	value.State = relation.StateDeleted
	value.UpdatedAt = t.now
	if err := t.saveRelation(ctx, value); err != nil {
		return relation.Relation{}, err
	}
	t.recordEntry(change.Entry{
		ObjectKind: change.ObjectRelation, DatabaseID: value.Source.DatabaseID, TableID: value.Source.TableID,
		ObjectID: value.ID, Operation: change.OperationDelete, BeforeRevision: value.Revision - 1, AfterRevision: value.Revision,
	}, change.Metadata{Actor: "system:links", Source: "msql", Reason: "unlink rows"})
	return value, nil
}

func (t *tx) listRelations(ctx context.Context, endpoint row.RelationEndpoint, direction string) ([]relation.Relation, error) {
	_, value, err := t.endpointRow(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	relations := []relation.Relation{}
	for _, link := range value.Links {
		if link.Direction != direction {
			continue
		}
		stored, err := t.loadRelation(ctx, link.RelationID)
		if err != nil {
			return nil, fmt.Errorf("row %q links to a missing relation: %w", value.ID, err)
		}
		relations = append(relations, stored)
	}
	return relations, nil
}

// RowLinks returns a row's links with stale summaries refreshed in memory. A
// summary is stale when the linked row has moved past the revision it was
// taken from; the stored copy is rewritten lazily by RefreshLinkSummaries.
func (db *DB) RowLinks(ctx context.Context, databaseName, tableName, rowID string) ([]Link, error) {
	var links []Link
	err := db.view(ctx, func(t *tx) error {
		_, value, err := t.endpointRow(ctx, row.RelationEndpoint{Database: databaseName, Table: tableName, RowID: rowID})
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
