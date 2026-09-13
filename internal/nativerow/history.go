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

// historyWalk returns a Row's revisions newest first, in one pass over the log.
//
// It used to point-read each revision by its record ID, which was cheap while
// the record log carried a process-resident map of where every record lived.
// E8 stage 3 deleted that map, so N point reads became N passes; one pass that
// keeps this Row's records costs the same as reading one revision did. A limit
// of 0 walks to revision 1.
//
// This is the fallback. A Repository with a generation reads revisions out of
// the Table's clustered version Tree and never reaches here.
func (repository *Repository) historyWalk(
	databaseID, tableID, rowID string, limit int,
) ([]history.Record, error) {
	records, err := repository.revisionRecords(rowID)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, nativestore.ErrNotFound
	}
	table, err := repository.table(databaseID, tableID)
	if err != nil {
		return nil, err
	}
	values := make([]row.Row, 0, len(records))
	for index := len(records) - 1; index >= 0; index-- {
		revision := uint64(index + 1)
		value, err := decodeStoredRecordWithTable(records[index].ID, records[index].Payload, table)
		if err != nil {
			return nil, err
		}
		if value.DatabaseID != databaseID || value.TableID != tableID {
			return nil, fmt.Errorf("%w: history Row belongs to another table", ErrCorrupt)
		}
		if value.Revision != revision {
			return nil, fmt.Errorf("%w: revision record %d identifies revision %d", ErrCorrupt, revision, value.Revision)
		}
		values = append(values, value)
		if limit > 0 && len(values) == limit {
			break
		}
	}
	return repository.attributionsFor(rowID, values)
}

// attributionsFor resolves the attribution for a batch of revisions.
//
// One pass for the envelopes and, where a revision predates them, one for the
// legacy history records. Resolving them one revision at a time is what made
// SHOW HISTORY cost a pass per revision once the record log lost its
// process-resident map.
func (repository *Repository) attributionsFor(
	rowID string, values []row.Row,
) ([]history.Record, error) {
	envelopes, err := repository.changeEnvelopes(values)
	if err != nil {
		return nil, err
	}
	var legacy map[uint64]historyMetadata
	result := make([]history.Record, 0, len(values))
	for _, value := range values {
		if envelope, ok := envelopes[value.ChangeSequence]; ok && value.ChangeSequence != 0 {
			if record, found := HistoryRecordFromEnvelope(value, envelope); found {
				result = append(result, record)
				continue
			}
		}
		if legacy == nil {
			legacy, err = repository.historyMetadata(rowID)
			if err != nil {
				return nil, err
			}
		}
		item, ok := legacy[value.Revision]
		if !ok {
			return nil, fmt.Errorf(
				"%w: revision %d of Row %q has no attribution", ErrCorrupt, value.Revision, rowID,
			)
		}
		result = append(result, historyRecord(item, value))
	}
	return result, nil
}

// changeEnvelopes resolves every envelope a batch of revisions names.
//
// Through the generation's change index one at a time when there is one — those
// are Tree descents — and otherwise in one pass over the record log. A sequence
// the log does not hold is simply absent from the map; the caller falls back.
func (repository *Repository) changeEnvelopes(values []row.Row) (map[uint64]change.Envelope, error) {
	wanted := make(map[uint64]struct{}, len(values))
	for _, value := range values {
		if value.ChangeSequence != 0 {
			wanted[value.ChangeSequence] = struct{}{}
		}
	}
	found := make(map[uint64]change.Envelope, len(wanted))
	if len(wanted) == 0 {
		return found, nil
	}
	if source, ok := repository.objects.(changeSource); ok && source != nil {
		for sequence := range wanted {
			envelope, err := source.ChangeEnvelope(sequence)
			if err != nil {
				continue
			}
			found[sequence] = envelope
		}
		return found, nil
	}
	return nativechange.New(repository.file).GetAll(wanted)
}

// historyMetadata reads every legacy history record for one Row in one pass.
func (repository *Repository) historyMetadata(rowID string) (map[uint64]historyMetadata, error) {
	records, err := repository.file.RecordsMatching(
		nativestore.ObjectKindHistory, revisionIDPredicate(rowID),
	)
	if err != nil {
		return nil, err
	}
	items := make(map[uint64]historyMetadata, len(records))
	for _, record := range records {
		item, err := decodeHistory(record.Payload)
		if err != nil {
			return nil, err
		}
		if item.rowID != rowID {
			return nil, fmt.Errorf("%w: history record identifies another Row", ErrCorrupt)
		}
		items[item.revision] = item
	}
	return items, nil
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

// changeSource resolves a committed change from the generation's own index.
//
// It is optional on purpose: a Repository opened without a generation has no
// index to ask, and only the Authority implements it.
type changeSource interface {
	ChangeEnvelope(sequence uint64) (change.Envelope, error)
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
