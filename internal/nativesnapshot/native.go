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
	"github.com/HW-Yue/Memora/internal/change"
	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/logical"
	"github.com/HW-Yue/Memora/internal/nativecatalog"
	"github.com/HW-Yue/Memora/internal/nativechange"
	"github.com/HW-Yue/Memora/internal/nativeconfig"
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
			for key, raw := range previous.Unknown {
				if _, current := value.Unknown[key]; !current {
					value.Unknown[key] = raw
				}
			}
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
	normalizeCatalogForSnapshot(databases)
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
	configuration, err := nativeconfig.Existing(service.file).History()
	if err != nil {
		return snapshot.LogicalDocument{}, err
	}
	if len(configuration) > 0 {
		raw, marshalErr := json.Marshal(configuration)
		if marshalErr != nil {
			return snapshot.LogicalDocument{}, marshalErr
		}
		value.Unknown[nativeconfig.SnapshotKey] = raw
	}
	policy, err := nativeconfig.Existing(service.file).PolicyHistory()
	if err != nil {
		return snapshot.LogicalDocument{}, err
	}
	if len(policy) > 0 {
		raw, marshalErr := json.Marshal(policy)
		if marshalErr != nil {
			return snapshot.LogicalDocument{}, marshalErr
		}
		value.Unknown[nativeconfig.PolicySnapshotKey] = raw
	}
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
	normalizeCatalogForSnapshot(catalogValue.Databases)
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
		if historyRows[index].SourceKind == "" {
			historyRows[index].SourceKind = history.SourceConversationAssertion
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
	// Restored revisions are stamped with a change sequence and their
	// attribution goes into the Change Log, the one place attribution lives.
	// The snapshot's own commit sequences are not reused for this: a commit
	// sequence and a change sequence are different numbering spaces, and
	// borrowing one for the other would collide with the sequences ordinary
	// writes go on to allocate.
	changeSequence, err := nativechange.New(service.file).NextSequence(0)
	if err != nil {
		return err
	}
	for _, id := range sortedRowIDs(rowVersions) {
		versions := rowVersions[id]
		table := tables[current[id].TableID]
		for _, record := range versions {
			value, err := rowFromHistory(record, table)
			if err != nil {
				return err
			}
			value.ChangeSequence = changeSequence
			if err := service.rows.StageSnapshotRow(transaction, value, table); err != nil {
				return err
			}
			envelope, err := restoredEnvelope(changeSequence, value, record)
			if err != nil {
				return err
			}
			if err := nativechange.Stage(transaction, envelope); err != nil {
				return err
			}
			changeSequence++
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
	if raw, ok := migrated.Unknown[nativeconfig.SnapshotKey]; ok {
		var configuration []nativeconfig.Revision
		if json.Unmarshal(raw, &configuration) != nil {
			return nativeError(result.CodeValidation, "logical snapshot query budget configuration is invalid")
		}
		if err := nativeconfig.Existing(service.file).StageHistory(transaction, configuration); err != nil {
			return err
		}
	}
	if raw, ok := migrated.Unknown[nativeconfig.PolicySnapshotKey]; ok {
		var policy []nativeconfig.PolicyRevision
		if json.Unmarshal(raw, &policy) != nil {
			return nativeError(result.CodeValidation, "logical snapshot Route policy configuration is invalid")
		}
		if err := nativeconfig.Existing(service.file).StagePolicyHistory(transaction, policy); err != nil {
			return err
		}
	}
	canonical, err := snapshot.EncodeLogical(migrated)
	if err != nil {
		return err
	}
	projectedUnknown := map[string]json.RawMessage{}
	if raw, ok := migrated.Unknown[nativeconfig.SnapshotKey]; ok {
		projectedUnknown[nativeconfig.SnapshotKey] = raw
	}
	if raw, ok := migrated.Unknown[nativeconfig.PolicySnapshotKey]; ok {
		projectedUnknown[nativeconfig.PolicySnapshotKey] = raw
	}
	projected, err := buildProjectedDocument(catalogValue.Databases, current, historyRows, relationVersions, projectedUnknown)
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
	for kind := nativestore.ObjectKindDatabase; kind <= nativestore.ObjectKindConfiguration; kind++ {
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

// restoredEnvelope is the Change Log entry for one replayed revision: the
// snapshot recorded who wrote it and why, and this is where that now lives.
//
// One envelope per revision rather than one per restore, because the
// attribution the snapshot carries is per revision — collapsing them would
// report one actor for writes that had many, which is a different history from
// the one being restored.
func restoredEnvelope(
	sequence uint64, value row.Row, record history.Record,
) (change.Envelope, error) {
	committedAt := record.RecordedAt.UTC()
	if committedAt.IsZero() {
		committedAt = value.UpdatedAt.UTC()
	}
	return change.NewEnvelope(sequence, committedAt, nativechange.RowMetadata(row.WriteMetadata{
		Actor: record.Actor, Source: record.Source, SourceKind: record.SourceKind,
		SourceReceiptID: record.SourceReceiptID, SourceLocator: record.SourceLocator,
		SourceContentHash: record.SourceContentHash, Reason: record.Reason,
	}), []change.Entry{nativechange.RowEntry(value, record.Operation)})
}

// sortedRowIDs fixes the order revisions are replayed in, so two restores of
// the same snapshot allocate the same change sequences and produce the same
// bytes. Map order would make a restore non-reproducible.
func sortedRowIDs(versions map[string][]history.Record) []string {
	ids := make([]string, 0, len(versions))
	for id := range versions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
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

func buildProjectedDocument(
	databases []catalog.Database,
	rows map[string]row.Row,
	records []history.Record,
	relations []relation.Relation,
	unknown map[string]json.RawMessage,
) ([]byte, error) {
	catalogJSON, _ := json.Marshal(struct {
		Version   string             `json:"version"`
		Databases []catalog.Database `json:"databases"`
	}{catalog.Version, databases})
	value := snapshot.LogicalDocument{Catalog: catalogJSON, Rows: []json.RawMessage{}, History: []json.RawMessage{}, RelationNow: []json.RawMessage{}, RelationPast: []json.RawMessage{}, Unknown: unknown}
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

func normalizeCatalogForSnapshot(databases []catalog.Database) {
	for databaseIndex := range databases {
		database := &databases[databaseIndex]
		if database.Aliases == nil {
			database.Aliases = []string{}
		}
		for tableIndex := range database.Tables {
			table := &database.Tables[tableIndex]
			if table.Aliases == nil {
				table.Aliases = []string{}
			}
			for columnIndex := range table.Columns {
				if table.Columns[columnIndex].Aliases == nil {
					table.Columns[columnIndex].Aliases = []string{}
				}
			}
		}
	}
}

func nativeError(code result.Code, message string) error {
	return &snapshot.Error{Code: code, Message: message}
}
