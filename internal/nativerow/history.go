package nativerow

import (
	"encoding/binary"
	"fmt"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/HW-Yue/Memora/internal/change"
	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/nativechange"
	"github.com/HW-Yue/Memora/internal/row"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

const historySchemaVersion = 1

type historyMetadata struct {
	rowID             string
	revision          uint64
	operation         history.Operation
	actor             string
	source            string
	sourceKind        history.SourceKind
	sourceReceiptID   string
	sourceLocator     string
	sourceContentHash string
	reason            string
	recordedAt        time.Time
}

// AppendHistory writes a per-Row History record — the retired storage for a
// revision's attribution.
//
// Nothing in production calls it. Attribution now lives once per transaction in
// the Change Log, and every write path, RESTORE included, records it there. The
// function survives because the contract "a Database written before that still
// reports its attribution" has to stay testable, and the only way to build such
// a Database is to write one of these records. Retiring the object kind outright
// (as kinds 9 and 13 were) would make that contract impossible to test from
// outside internal/store/native, and an untestable contract is one that breaks.
//
// See docs/storage/per-table-tree-v1.md §5.8.
func (repository *Repository) AppendHistory(value row.Row, operation history.Operation, metadata row.WriteMetadata, recordedAt time.Time) error {
	payload, err := historyPayload(value, operation, metadata, recordedAt)
	if err != nil {
		return err
	}
	return repository.file.Put(nativestore.ObjectKindHistory, historySchemaVersion, revisionRecordID(value.ID, value.Revision), payload)
}

func historyPayload(value row.Row, operation history.Operation, metadata row.WriteMetadata, recordedAt time.Time) ([]byte, error) {
	metadata = normalizedMetadata(metadata)
	return encodeHistory(historyMetadata{
		rowID: value.ID, revision: value.Revision, operation: operation,
		actor: metadata.Actor, source: metadata.Source, sourceKind: metadata.SourceKind,
		sourceReceiptID: metadata.SourceReceiptID, sourceLocator: metadata.SourceLocator,
		sourceContentHash: metadata.SourceContentHash, reason: metadata.Reason,
		recordedAt: recordedAt.UTC(),
	})
}

func (repository *Repository) History(databaseID, tableID, rowID string, limit int) ([]history.Record, bool, error) {
	if limit < 1 || limit > 1000 {
		return nil, false, fmt.Errorf("%w: history limit must be between 1 and 1000", ErrInvalid)
	}
	// Read one past the page so "more" is answered without walking the rest of
	// the chain: a caller asking for 3 of 40 revisions pays for 4, not 40.
	result, err := repository.historyWalk(databaseID, tableID, rowID, limit+1)
	if err != nil {
		return nil, false, err
	}
	more := len(result) > limit
	if more {
		result = result[:limit]
	}
	return result, more, nil
}

func (repository *Repository) HistoryAll(databaseID, tableID, rowID string) ([]history.Record, error) {
	return repository.historyWalk(databaseID, tableID, rowID, 0)
}

// historyWalk follows a Row's version chain from its newest revision backwards,
// newest first. Each hop is a direct read at the address the newer revision
// carries, so the walk costs one read per revision returned and never touches a
// revision the caller did not ask for. A limit of 0 walks to revision 1.
func (repository *Repository) historyWalk(
	databaseID, tableID, rowID string, limit int,
) ([]history.Record, error) {
	latest, err := repository.latestRevision(rowID)
	if err != nil {
		return nil, err
	}
	table, err := repository.table(databaseID, tableID)
	if err != nil {
		return nil, err
	}
	// Revisions are contiguous from 1, so walking backwards from the latest is a
	// sequence of point lookups on known record IDs — the cost is the revisions
	// returned, never the records the Database holds.
	result := make([]history.Record, 0, latest)
	for revision := latest; revision >= 1; revision-- {
		value, err := repository.readRecordWithTable(revisionRecordID(rowID, revision), table)
		if err != nil {
			return nil, err
		}
		if value.DatabaseID != databaseID || value.TableID != tableID {
			return nil, fmt.Errorf("%w: history Row belongs to another table", ErrCorrupt)
		}
		if value.Revision != revision {
			return nil, fmt.Errorf("%w: revision record %d identifies revision %d", ErrCorrupt, revision, value.Revision)
		}
		record, err := repository.attributionFor(value)
		if err != nil {
			return nil, err
		}
		result = append(result, record)
		if limit > 0 && len(result) == limit {
			break
		}
	}
	return result, nil
}

// HistoryRecordFromEnvelope assembles what SHOW HISTORY returns for one
// revision: the Row supplies the content, the transaction's envelope supplies
// the attribution. Attribution is recorded once per transaction, so every Row a
// write touched reports the same actor, source and reason.
//
// The operation comes from the envelope entry naming this Row and revision; an
// envelope that does not mention it cannot describe what happened to it.
func HistoryRecordFromEnvelope(value row.Row, envelope change.Envelope) (history.Record, bool) {
	operation, found := history.Operation(""), false
	for _, entry := range envelope.Entries {
		if entry.ObjectID == value.ID && entry.AfterRevision == value.Revision {
			operation, found = history.Operation(entry.Operation), true
			break
		}
	}
	if !found {
		return history.Record{}, false
	}
	sourceKind := history.SourceKind(envelope.SourceKind)
	if !validSourceKind(sourceKind) {
		sourceKind = history.SourceConversationAssertion
	}
	return historyRecord(historyMetadata{
		rowID: value.ID, revision: value.Revision, operation: operation,
		actor: envelope.Actor, source: envelope.Source, sourceKind: sourceKind,
		sourceReceiptID: envelope.SourceReceiptID, sourceLocator: envelope.SourceLocator,
		sourceContentHash: envelope.SourceContentHash, reason: envelope.Reason,
		recordedAt: envelope.CommittedAt,
	}, value), true
}

// historyRecord is the single definition of what SHOW HISTORY returns: the Row
// supplies the content, the metadata supplies the provenance. Both the record
// log path and the clustered tree path go through here so they cannot drift.
func historyRecord(item historyMetadata, value row.Row) history.Record {
	return history.Record{
		Version: history.Version, DatabaseID: value.DatabaseID, TableID: value.TableID,
		RowID: value.ID, SchemaVersion: value.SchemaVersion, Revision: value.Revision,
		CommitSequence: value.CommitSequence, Operation: item.operation, State: string(value.State),
		Values: value.Values, Actor: item.actor, Source: item.source, SourceKind: item.sourceKind,
		SourceReceiptID: item.sourceReceiptID, SourceLocator: item.sourceLocator,
		SourceContentHash: item.sourceContentHash, Reason: item.reason,
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt, RecordedAt: item.recordedAt,
	}
}

// attributionFor resolves who wrote one revision and why.
//
// Attribution belongs to the transaction, not to the revision: a write touching
// many Rows has one actor and one reason, recorded once in the Change Log. The
// revision carries the change sequence that names it, so the lookup is a point
// read on the envelope.
//
// A revision whose change sequence is zero predates that link. Those fall back
// to the per-Row History record, which is the only reason that record kind is
// still read — nothing writes a new one. See docs/storage/per-table-tree-v1.md
// §4.
func (repository *Repository) attributionFor(value row.Row) (history.Record, error) {
	if value.ChangeSequence != 0 {
		envelope, err := nativechange.New(repository.file).Get(value.ChangeSequence)
		if err == nil {
			if record, ok := HistoryRecordFromEnvelope(value, envelope); ok {
				return record, nil
			}
		}
	}
	item, err := repository.historyMetadataFor(value.ID, value.Revision)
	if err != nil {
		return history.Record{}, err
	}
	return historyRecord(item, value), nil
}

func (repository *Repository) historyMetadataFor(rowID string, revision uint64) (historyMetadata, error) {
	payload, err := repository.file.Get(nativestore.ObjectKindHistory, revisionRecordID(rowID, revision))
	if err != nil {
		return historyMetadata{}, err
	}
	item, err := decodeHistory(payload)
	if err != nil {
		return historyMetadata{}, err
	}
	if item.rowID != rowID || item.revision != revision {
		return historyMetadata{}, fmt.Errorf("%w: history record identifies another revision", ErrCorrupt)
	}
	return item, nil
}

func (repository *Repository) AllHistory() ([]history.Record, error) {
	rows, err := repository.AllRows()
	if err != nil {
		return nil, err
	}
	result := make([]history.Record, 0)
	for _, value := range rows {
		records, _, err := repository.History(value.DatabaseID, value.TableID, value.ID, 1000)
		if err != nil {
			return nil, err
		}
		result = append(result, records...)
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].RowID == result[right].RowID {
			return result[left].Revision < result[right].Revision
		}
		return result[left].RowID < result[right].RowID
	})
	return result, nil
}

func normalizedMetadata(value row.WriteMetadata) row.WriteMetadata {
	if value.Actor == "" {
		value.Actor = "system:direct-api"
	}
	if value.Source == "" {
		value.Source = "direct-api"
	}
	if value.Reason == "" {
		value.Reason = "row mutation"
	}
	if value.SourceKind == "" {
		value.SourceKind = history.SourceConversationAssertion
	}
	return value
}

func encodeHistory(value historyMetadata) ([]byte, error) {
	if value.sourceKind == "" {
		value.sourceKind = history.SourceConversationAssertion
	}
	text := []string{
		value.rowID, string(value.operation), value.actor, value.source, value.reason,
		string(value.sourceKind), value.sourceReceiptID, value.sourceLocator, value.sourceContentHash,
	}
	size := 2 + 8 + 8
	for _, item := range text {
		if !utf8.ValidString(item) {
			return nil, fmt.Errorf("%w: history text is not UTF-8", ErrInvalid)
		}
		size += 4 + len(item)
	}
	encoded := make([]byte, 0, size)
	encoded = binary.LittleEndian.AppendUint16(encoded, historySchemaVersion)
	encoded = binary.LittleEndian.AppendUint64(encoded, value.revision)
	encoded = binary.LittleEndian.AppendUint64(encoded, uint64(value.recordedAt.UnixNano()))
	for _, item := range text {
		encoded = binary.LittleEndian.AppendUint32(encoded, uint32(len(item)))
		encoded = append(encoded, item...)
	}
	return encoded, nil
}

func decodeHistory(payload []byte) (historyMetadata, error) {
	input := decoder{bytes: payload}
	version, err := input.u16()
	if err != nil || version != historySchemaVersion {
		return historyMetadata{}, fmt.Errorf("%w: invalid history version", ErrCorrupt)
	}
	revision, err := input.u64()
	if err != nil || revision == 0 {
		return historyMetadata{}, fmt.Errorf("%w: invalid history revision", ErrCorrupt)
	}
	recorded, err := input.i64()
	if err != nil {
		return historyMetadata{}, err
	}
	values := make([]string, 5)
	for index := range values {
		values[index], err = input.text()
		if err != nil {
			return historyMetadata{}, err
		}
	}
	operation := history.Operation(values[1])
	sourceKind := history.SourceConversationAssertion
	provenance := []string{"", "", ""}
	if input.offset < len(payload) {
		sourceKindText, provenanceErr := input.text()
		if provenanceErr != nil {
			return historyMetadata{}, provenanceErr
		}
		sourceKind = history.SourceKind(sourceKindText)
		for index := range provenance {
			provenance[index], provenanceErr = input.text()
			if provenanceErr != nil {
				return historyMetadata{}, provenanceErr
			}
		}
	}
	if input.offset != len(payload) || !validSourceKind(sourceKind) || (operation != history.OperationInsert && operation != history.OperationUpdate && operation != history.OperationDelete && operation != history.OperationCompensate && operation != history.OperationSplit && operation != history.OperationMerge) {
		return historyMetadata{}, fmt.Errorf("%w: invalid history payload", ErrCorrupt)
	}
	return historyMetadata{
		rowID: values[0], revision: revision, operation: operation,
		actor: values[2], source: values[3], reason: values[4], sourceKind: sourceKind,
		sourceReceiptID: provenance[0], sourceLocator: provenance[1],
		sourceContentHash: provenance[2], recordedAt: time.Unix(0, recorded).UTC(),
	}, nil
}

func validSourceKind(value history.SourceKind) bool {
	return value == history.SourceConversationAssertion || value == history.SourceDocumentAnchor ||
		value == history.SourceRepositoryAnchor || value == history.SourceReviewed
}
