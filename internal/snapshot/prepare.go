package snapshot

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/relation"
	"github.com/HW-Yue/Memora/internal/result"
)

type catalogLayout struct {
	tableOrder    []string
	tableDatabase map[string]string
}

type rowHeader struct {
	ID             string                     `json:"row_id"`
	DatabaseID     string                     `json:"database_id"`
	TableID        string                     `json:"table_id"`
	SchemaVersion  uint64                     `json:"schema_version"`
	Revision       uint64                     `json:"revision"`
	CommitSequence uint64                     `json:"commit_sequence"`
	State          string                     `json:"row_state"`
	Values         map[string]json.RawMessage `json:"values"`
	CreatedAt      time.Time                  `json:"created_at"`
	UpdatedAt      time.Time                  `json:"updated_at"`
}

type historyHeader = history.Record

type preparedDocument struct {
	rows             []rowHeader
	rowIDs           map[string][]string
	history          []historyHeader
	historyRevisions map[string][]uint64
	relationNow      []relation.Relation
	relationPast     []relation.Relation
	relationOutgoing map[string][]string
	relationIncoming map[string][]string
	commitSequence   uint64
}

func decodeCatalog(encoded []byte) (catalogLayout, error) {
	var value struct {
		Version   string `json:"version"`
		Databases []struct {
			ID     string `json:"database_id"`
			Tables []struct {
				ID         string `json:"table_id"`
				DatabaseID string `json:"database_id"`
			} `json:"tables"`
		} `json:"databases"`
	}
	if json.Unmarshal(encoded, &value) != nil || value.Version != catalog.Version ||
		value.Databases == nil {
		return catalogLayout{}, snapshotError(result.CodeValidation, "logical snapshot Catalog is invalid")
	}
	layout := catalogLayout{
		tableOrder: []string{}, tableDatabase: map[string]string{},
	}
	databases := map[string]struct{}{}
	for _, database := range value.Databases {
		if !cleanID(database.ID) {
			return catalogLayout{}, snapshotError(result.CodeValidation, "logical snapshot Database identity is invalid")
		}
		if _, exists := databases[database.ID]; exists {
			return catalogLayout{}, snapshotError(result.CodeValidation, "logical snapshot Database identity is duplicated")
		}
		databases[database.ID] = struct{}{}
		for _, table := range database.Tables {
			databaseID := table.DatabaseID
			if databaseID == "" {
				databaseID = database.ID
			}
			if !cleanID(table.ID) || databaseID != database.ID {
				return catalogLayout{}, snapshotError(result.CodeValidation, "logical snapshot Table identity is invalid")
			}
			if _, exists := layout.tableDatabase[table.ID]; exists {
				return catalogLayout{}, snapshotError(result.CodeValidation, "logical snapshot Table identity is duplicated")
			}
			layout.tableDatabase[table.ID] = databaseID
			layout.tableOrder = append(layout.tableOrder, table.ID)
		}
	}
	return layout, nil
}

func prepare(value document) (preparedDocument, error) {
	layout, err := decodeCatalog(value.Catalog)
	if err != nil {
		return preparedDocument{}, err
	}
	prepared := preparedDocument{
		rows: []rowHeader{}, rowIDs: map[string][]string{},
		history: []historyHeader{}, historyRevisions: map[string][]uint64{},
		relationNow: []relation.Relation{}, relationPast: []relation.Relation{},
		relationOutgoing: map[string][]string{}, relationIncoming: map[string][]string{},
		commitSequence: value.CommitSequence,
	}
	rowKeys := map[string]string{}
	for _, raw := range value.Rows {
		var header rowHeader
		if json.Unmarshal(raw, &header) != nil || !validRow(header, layout) {
			return preparedDocument{}, snapshotError(result.CodeValidation, "logical snapshot Row is invalid")
		}
		key := header.TableID + "\x00" + header.ID
		if _, exists := rowKeys[key]; exists {
			return preparedDocument{}, snapshotError(result.CodeValidation, "logical snapshot Row is duplicated")
		}
		rowKeys[key] = header.DatabaseID
		prepared.rows = append(prepared.rows, header)
		prepared.rowIDs[header.TableID] = append(prepared.rowIDs[header.TableID], header.ID)
		prepared.commitSequence = max(prepared.commitSequence, header.CommitSequence)
	}
	for _, raw := range value.History {
		var record history.Record
		if json.Unmarshal(raw, &record) != nil || !validHistory(record) {
			return preparedDocument{}, snapshotError(result.CodeValidation, "logical snapshot History is invalid")
		}
		if databaseID, exists := rowKeys[record.TableID+"\x00"+record.RowID]; !exists || databaseID != record.DatabaseID {
			return preparedDocument{}, snapshotError(result.CodeValidation, "logical snapshot History references a missing Row")
		}
		key := record.TableID + "\x00" + record.RowID
		prepared.history = append(prepared.history, record)
		prepared.historyRevisions[key] = append(prepared.historyRevisions[key], record.Revision)
		prepared.commitSequence = max(prepared.commitSequence, record.CommitSequence)
	}
	for key, revisions := range prepared.historyRevisions {
		sort.Slice(revisions, func(left, right int) bool { return revisions[left] < revisions[right] })
		for index, revision := range revisions {
			if revision == 0 || (index > 0 && revision != revisions[index-1]+1) {
				return preparedDocument{}, snapshotError(result.CodeValidation, "logical snapshot History revisions are invalid")
			}
		}
		prepared.historyRevisions[key] = revisions
	}
	currentByID := map[string]relation.Relation{}
	for _, raw := range value.RelationNow {
		var current relation.Relation
		if json.Unmarshal(raw, &current) != nil || !validRelation(current, rowKeys) {
			return preparedDocument{}, snapshotError(result.CodeValidation, "logical snapshot Relation is invalid")
		}
		if _, exists := currentByID[current.ID]; exists {
			return preparedDocument{}, snapshotError(result.CodeValidation, "logical snapshot Relation is duplicated")
		}
		currentByID[current.ID] = current
		prepared.relationNow = append(prepared.relationNow, current)
		outKey, inKey := endpointKey(current.Source), endpointKey(current.Target)
		prepared.relationOutgoing[outKey] = append(prepared.relationOutgoing[outKey], current.ID)
		prepared.relationIncoming[inKey] = append(prepared.relationIncoming[inKey], current.ID)
		prepared.commitSequence = max(prepared.commitSequence, current.CommitSequence)
	}
	versions := map[string][]uint64{}
	for _, raw := range value.RelationPast {
		var version relation.Relation
		if json.Unmarshal(raw, &version) != nil || !validRelation(version, rowKeys) {
			return preparedDocument{}, snapshotError(result.CodeValidation, "logical snapshot Relation history is invalid")
		}
		if _, exists := currentByID[version.ID]; !exists {
			return preparedDocument{}, snapshotError(result.CodeValidation, "logical snapshot Relation history has no current record")
		}
		prepared.relationPast = append(prepared.relationPast, version)
		versions[version.ID] = append(versions[version.ID], version.Revision)
		prepared.commitSequence = max(prepared.commitSequence, version.CommitSequence)
	}
	for id, current := range currentByID {
		revisions := versions[id]
		sort.Slice(revisions, func(left, right int) bool { return revisions[left] < revisions[right] })
		if len(revisions) == 0 || revisions[0] != 1 ||
			revisions[len(revisions)-1] != current.Revision {
			return preparedDocument{}, snapshotError(result.CodeValidation, "logical snapshot Relation history is incomplete")
		}
		for index, revision := range revisions {
			if revision != uint64(index+1) {
				return preparedDocument{}, snapshotError(result.CodeValidation, "logical snapshot Relation revisions are invalid")
			}
		}
	}
	if value.CommitSequence > 0 && value.CommitSequence < prepared.commitSequence {
		return preparedDocument{}, snapshotError(result.CodeValidation, "logical snapshot commit sequence is stale")
	}
	return prepared, nil
}

func validRow(value rowHeader, layout catalogLayout) bool {
	databaseID, exists := layout.tableDatabase[value.TableID]
	return cleanID(value.ID) && exists && value.DatabaseID == databaseID &&
		value.SchemaVersion > 0 && value.Revision > 0 && value.CommitSequence > 0 &&
		(value.State == "live" || value.State == "deleted") && value.Values != nil &&
		!value.CreatedAt.IsZero() && !value.UpdatedAt.IsZero() &&
		!value.UpdatedAt.Before(value.CreatedAt)
}

func validHistory(value history.Record) bool {
	if value.Version != history.Version || !cleanID(value.DatabaseID) ||
		!cleanID(value.TableID) || !cleanID(value.RowID) ||
		value.SchemaVersion == 0 || value.Revision == 0 || value.CommitSequence == 0 ||
		(value.State != "live" && value.State != "deleted") || value.Values == nil ||
		value.CreatedAt.IsZero() || value.UpdatedAt.IsZero() || value.RecordedAt.IsZero() ||
		!cleanID(value.Actor) || !cleanID(value.Source) || !cleanID(value.Reason) {
		return false
	}
	switch value.Operation {
	case history.OperationInsert, history.OperationUpdate,
		history.OperationDelete, history.OperationCompensate:
		return true
	default:
		return false
	}
}

func validRelation(value relation.Relation, rows map[string]string) bool {
	if value.Version != relation.Version || !strings.HasPrefix(value.ID, "rel_") ||
		!cleanID(strings.TrimPrefix(value.ID, "rel_")) ||
		value.Revision == 0 || value.CommitSequence == 0 ||
		(value.State != relation.StateLive && value.State != relation.StateDeleted) ||
		value.CreatedAt.IsZero() || value.UpdatedAt.IsZero() ||
		!cleanID(value.Type) || !validEndpoint(value.Source, rows) ||
		!validEndpoint(value.Target, rows) {
		return false
	}
	return true
}

func validEndpoint(value relation.Endpoint, rows map[string]string) bool {
	if !cleanID(value.DatabaseID) || !cleanID(value.TableID) || !cleanID(value.RowID) {
		return false
	}
	databaseID, exists := rows[value.TableID+"\x00"+value.RowID]
	return exists && databaseID == value.DatabaseID
}

func endpointKey(value relation.Endpoint) string {
	return value.DatabaseID + "\x00" + value.TableID + "\x00" + value.RowID
}
