package nativesnapshot

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"sort"
	"time"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/logical"
	"github.com/HW-Yue/Memora/internal/nativecatalog"
	"github.com/HW-Yue/Memora/internal/nativerow"
	"github.com/HW-Yue/Memora/internal/relation"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/snapshot"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

const nativeSourceID = "logical-source"

type NativeService struct {
	file    *nativestore.File
	catalog *nativecatalog.Repository
	rows    *nativerow.Repository
}

type nativeSource struct {
	Source      json.RawMessage `json:"source"`
	Fingerprint string          `json:"fingerprint"`
}

func NewNative(file *nativestore.File) *NativeService {
	return &NativeService{file: file, catalog: nativecatalog.New(file), rows: nativerow.New(file)}
}

func (service *NativeService) Export() ([]byte, error) {
	value, err := service.nativeDocument()
	if err != nil {
		return nil, err
	}
	encoded, err := snapshot.EncodeLogical(value)
	if err != nil {
		return nil, err
	}
	fingerprint := nativeFingerprint(encoded)
	if payload, getErr := service.file.Get(nativestore.ObjectKindSnapshotMeta, nativeSourceID); getErr == nil {
		var source nativeSource
		if json.Unmarshal(payload, &source) != nil {
			return nil, nativeError(result.CodeInternal, "native snapshot metadata is corrupt")
		}
		if source.Fingerprint == fingerprint {
			return append([]byte(nil), source.Source...), nil
		}
		if previous, decodeErr := snapshot.DecodeLogical(source.Source); decodeErr == nil {
			value.Unknown = previous.Unknown
			return snapshot.EncodeLogical(value)
		}
	}
	return encoded, nil
}

func (service *NativeService) nativeDocument() (snapshot.LogicalDocument, error) {
	databases, err := service.catalog.Read()
	if err != nil {
		return snapshot.LogicalDocument{}, err
	}
	catalogJSON, err := json.Marshal(struct {
		Version   string             `json:"version"`
		Databases []catalog.Database `json:"databases"`
	}{catalog.Version, databases})
	if err != nil {
		return snapshot.LogicalDocument{}, err
	}
	rows, err := service.rows.AllRows()
	if err != nil {
		return snapshot.LogicalDocument{}, err
	}
	historyRows, err := service.rows.AllHistory()
	if err != nil {
		return snapshot.LogicalDocument{}, err
	}
	versions, err := service.rows.AllRelationVersions()
	if err != nil {
		return snapshot.LogicalDocument{}, err
	}
	value := snapshot.LogicalDocument{Catalog: catalogJSON, Rows: []json.RawMessage{}, History: []json.RawMessage{}, RelationNow: []json.RawMessage{}, RelationPast: []json.RawMessage{}, Unknown: map[string]json.RawMessage{}}
	for _, item := range rows {
		raw, _ := json.Marshal(item)
		value.Rows = append(value.Rows, raw)
		value.CommitSequence = max(value.CommitSequence, item.CommitSequence)
	}
	for _, item := range historyRows {
		raw, _ := json.Marshal(item)
		value.History = append(value.History, raw)
		value.CommitSequence = max(value.CommitSequence, item.CommitSequence)
	}
	latest := map[string]relation.Relation{}
	for _, item := range versions {
		raw, _ := json.Marshal(item)
		value.RelationPast = append(value.RelationPast, raw)
		if current, ok := latest[item.ID]; !ok || item.Revision > current.Revision {
			latest[item.ID] = item
		}
		value.CommitSequence = max(value.CommitSequence, item.CommitSequence)
	}
	ids := make([]string, 0, len(latest))
	for id := range latest {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		raw, _ := json.Marshal(latest[id])
		value.RelationNow = append(value.RelationNow, raw)
	}
	return value, nil
}

func (service *NativeService) Import(encoded []byte) error {
	migrated, err := snapshot.DecodeLogical(encoded)
	if err != nil {
		return err
	}
	if err := service.requireEmpty(); err != nil {
		return err
	}
	var catalogValue struct {
		Version   string             `json:"version"`
		Databases []catalog.Database `json:"databases"`
	}
	if json.Unmarshal(migrated.Catalog, &catalogValue) != nil {
		return nativeError(result.CodeValidation, "logical snapshot Catalog is invalid")
	}
	normalizeCatalogSlices(catalogValue.Databases)
	tables := map[string]catalog.Table{}
	for _, database := range catalogValue.Databases {
		for _, table := range database.Tables {
			tables[table.ID] = table
		}
	}
	current := map[string]row.Row{}
	for _, raw := range migrated.Rows {
		value, err := decodeNativeRow(raw, tables)
		if err != nil {
			return err
		}
		current[value.ID] = value
	}
	historyRows := make([]history.Record, len(migrated.History))
	rowVersions := map[string][]history.Record{}
	for index, raw := range migrated.History {
		if json.Unmarshal(raw, &historyRows[index]) != nil {
			return nativeError(result.CodeValidation, "logical snapshot History is invalid")
		}
		rowVersions[historyRows[index].RowID] = append(rowVersions[historyRows[index].RowID], historyRows[index])
	}
	for id, latest := range current {
		versions := rowVersions[id]
		sort.Slice(versions, func(left, right int) bool { return versions[left].Revision < versions[right].Revision })
		if len(versions) != int(latest.Revision) || versions[len(versions)-1].Revision != latest.Revision {
			return nativeError(result.CodeValidation, "logical snapshot Row history is incomplete")
		}
	}
	transaction, err := service.file.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback() }()
	if err := service.catalog.StageSnapshot(transaction, catalogValue.Databases); err != nil {
		return err
	}
	for id, versions := range rowVersions {
		table := tables[current[id].TableID]
		for _, record := range versions {
			value, err := rowFromHistory(record, table)
			if err != nil {
				return err
			}
			if err := service.rows.StageSnapshotRow(transaction, value, table); err != nil {
				return err
			}
			if err := service.rows.StageHistory(transaction, value, record.Operation, row.WriteMetadata{Actor: record.Actor, Source: record.Source, Reason: record.Reason}, record.RecordedAt); err != nil {
				return err
			}
		}
		latestHistory, err := rowFromHistory(versions[len(versions)-1], table)
		if err != nil {
			return err
		}
		if latestHistory.State != current[id].State || !reflect.DeepEqual(latestHistory.Values, current[id].Values) {
			return nativeError(result.CodeValidation, "logical snapshot current Row disagrees with History")
		}
	}
	relationVersions := make([]relation.Relation, len(migrated.RelationPast))
	for index, raw := range migrated.RelationPast {
		if json.Unmarshal(raw, &relationVersions[index]) != nil {
			return nativeError(result.CodeValidation, "logical snapshot Relation is invalid")
		}
	}
	sort.Slice(relationVersions, func(left, right int) bool {
		if relationVersions[left].ID == relationVersions[right].ID {
			return relationVersions[left].Revision < relationVersions[right].Revision
		}
		return relationVersions[left].ID < relationVersions[right].ID
	})
	for _, value := range relationVersions {
		if err := service.rows.StageSnapshotRelation(transaction, value); err != nil {
			return err
		}
	}
	canonical, err := snapshot.EncodeLogical(migrated)
	if err != nil {
		return err
	}
	projected, err := buildProjectedDocument(catalogValue.Databases, current, historyRows, relationVersions)
	if err != nil {
		return err
	}
	metadata, _ := json.Marshal(nativeSource{Source: canonical, Fingerprint: nativeFingerprint(projected)})
	if err := transaction.Put(nativestore.ObjectKindSnapshotMeta, 1, nativeSourceID, metadata); err != nil {
		return err
	}
	return transaction.Commit()
}

func (service *NativeService) requireEmpty() error {
	for kind := nativestore.ObjectKindDatabase; kind <= nativestore.ObjectKindSnapshotMeta; kind++ {
		ids, err := service.file.IDs(kind)
		if err != nil {
			return err
		}
		if len(ids) > 0 {
			return nativeError(result.CodeAlreadyExists, "native snapshot target is not empty")
		}
	}
	return nil
}

func decodeNativeRow(raw []byte, tables map[string]catalog.Table) (row.Row, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value row.Row
	if decoder.Decode(&value) != nil {
		return row.Row{}, nativeError(result.CodeValidation, "logical snapshot Row is invalid")
	}
	table, ok := tables[value.TableID]
	if !ok {
		return row.Row{}, nativeError(result.CodeValidation, "logical snapshot Row references a missing Table")
	}
	converted, err := nativeValues(value.Values, table)
	if err != nil {
		return row.Row{}, err
	}
	value.Values = converted
	return value, nil
}

func rowFromHistory(record history.Record, table catalog.Table) (row.Row, error) {
	values, err := nativeValues(record.Values, table)
	if err != nil {
		return row.Row{}, err
	}
	return row.Row{ID: record.RowID, DatabaseID: record.DatabaseID, TableID: record.TableID, SchemaVersion: record.SchemaVersion, Revision: record.Revision, CommitSequence: record.CommitSequence, State: row.State(record.State), Values: values, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt}, nil
}

func nativeValues(values map[string]any, table catalog.Table) (map[string]any, error) {
	converted := make(map[string]any, len(values))
	for _, column := range table.Columns {
		value := values[column.ID]
		if value == nil {
			converted[column.ID] = nil
			continue
		}
		switch logical.Kind(column.Type) {
		case logical.KindInteger:
			switch number := value.(type) {
			case json.Number:
				integer, err := number.Int64()
				if err != nil {
					return nil, nativeError(result.CodeValidation, "logical snapshot INTEGER is invalid")
				}
				value = integer
			case float64:
				value = int64(number)
			}
		case logical.KindTimestamp:
			if text, ok := value.(string); ok {
				parsed, err := time.Parse(time.RFC3339Nano, text)
				if err != nil {
					return nil, nativeError(result.CodeValidation, "logical snapshot TIMESTAMP is invalid")
				}
				value = parsed
			}
		}
		converted[column.ID] = value
	}
	return converted, nil
}

func buildProjectedDocument(databases []catalog.Database, rows map[string]row.Row, records []history.Record, relations []relation.Relation) ([]byte, error) {
	catalogJSON, _ := json.Marshal(struct {
		Version   string             `json:"version"`
		Databases []catalog.Database `json:"databases"`
	}{catalog.Version, databases})
	value := snapshot.LogicalDocument{Catalog: catalogJSON, Rows: []json.RawMessage{}, History: []json.RawMessage{}, RelationNow: []json.RawMessage{}, RelationPast: []json.RawMessage{}, Unknown: map[string]json.RawMessage{}}
	ids := make([]string, 0, len(rows))
	for id := range rows {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		raw, _ := json.Marshal(rows[id])
		value.Rows = append(value.Rows, raw)
		value.CommitSequence = max(value.CommitSequence, rows[id].CommitSequence)
	}
	sort.Slice(records, func(left, right int) bool {
		if records[left].RowID == records[right].RowID {
			return records[left].Revision < records[right].Revision
		}
		return records[left].RowID < records[right].RowID
	})
	for _, record := range records {
		raw, _ := json.Marshal(record)
		value.History = append(value.History, raw)
		value.CommitSequence = max(value.CommitSequence, record.CommitSequence)
	}
	latest := map[string]relation.Relation{}
	sort.Slice(relations, func(left, right int) bool {
		if relations[left].ID == relations[right].ID {
			return relations[left].Revision < relations[right].Revision
		}
		return relations[left].ID < relations[right].ID
	})
	for _, item := range relations {
		raw, _ := json.Marshal(item)
		value.RelationPast = append(value.RelationPast, raw)
		latest[item.ID] = item
		value.CommitSequence = max(value.CommitSequence, item.CommitSequence)
	}
	ids = ids[:0]
	for id := range latest {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		raw, _ := json.Marshal(latest[id])
		value.RelationNow = append(value.RelationNow, raw)
	}
	return snapshot.EncodeLogical(value)
}

func nativeFingerprint(encoded []byte) string {
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func normalizeCatalogSlices(databases []catalog.Database) {
	for databaseIndex := range databases {
		database := &databases[databaseIndex]
		if len(database.Aliases) == 0 {
			database.Aliases = nil
		}
		for tableIndex := range database.Tables {
			table := &database.Tables[tableIndex]
			if len(table.Aliases) == 0 {
				table.Aliases = nil
			}
			for columnIndex := range table.Columns {
				if len(table.Columns[columnIndex].Aliases) == 0 {
					table.Columns[columnIndex].Aliases = nil
				}
			}
		}
	}
}

func nativeError(code result.Code, message string) error {
	return &snapshot.Error{Code: code, Message: message}
}
