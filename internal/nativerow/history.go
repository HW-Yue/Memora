package nativerow

import (
	"encoding/binary"
	"fmt"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/HW-Yue/Memora/internal/history"
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

func (repository *Repository) AppendHistory(value row.Row, operation history.Operation, metadata row.WriteMetadata, recordedAt time.Time) error {
	payload, err := historyPayload(value, operation, metadata, recordedAt)
	if err != nil {
		return err
	}
	return repository.file.Put(nativestore.ObjectKindHistory, historySchemaVersion, revisionRecordID(value.ID, value.Revision), payload)
}

func (repository *Repository) StageHistory(transaction *nativestore.Transaction, value row.Row, operation history.Operation, metadata row.WriteMetadata, recordedAt time.Time) error {
	if transaction == nil {
		return fmt.Errorf("%w: transaction is required", ErrInvalid)
	}
	payload, err := historyPayload(value, operation, metadata, recordedAt)
	if err != nil {
		return err
	}
	return transaction.Put(nativestore.ObjectKindHistory, historySchemaVersion, revisionRecordID(value.ID, value.Revision), payload)
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
	result, err := repository.HistoryAll(databaseID, tableID, rowID)
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
	ids, err := repository.file.IDs(nativestore.ObjectKindHistory)
	if err != nil {
		return nil, err
	}
	metadata := make([]historyMetadata, 0)
	for _, recordID := range ids {
		payload, err := repository.file.Get(nativestore.ObjectKindHistory, recordID)
		if err != nil {
			return nil, err
		}
		item, err := decodeHistory(payload)
		if err != nil {
			return nil, err
		}
		if item.rowID == rowID {
			metadata = append(metadata, item)
		}
	}
	sort.Slice(metadata, func(left, right int) bool { return metadata[left].revision > metadata[right].revision })
	result := make([]history.Record, 0, len(metadata))
	for _, item := range metadata {
		value, err := repository.ReadRevision(rowID, item.revision)
		if err != nil {
			return nil, err
		}
		if value.DatabaseID != databaseID || value.TableID != tableID {
			return nil, fmt.Errorf("%w: history Row belongs to another table", ErrCorrupt)
		}
		result = append(result, history.Record{
			Version: history.Version, DatabaseID: value.DatabaseID, TableID: value.TableID,
			RowID: value.ID, SchemaVersion: value.SchemaVersion, Revision: value.Revision,
			CommitSequence: value.CommitSequence, Operation: item.operation, State: string(value.State),
			Values: value.Values, Actor: item.actor, Source: item.source, SourceKind: item.sourceKind,
			SourceReceiptID: item.sourceReceiptID, SourceLocator: item.sourceLocator,
			SourceContentHash: item.sourceContentHash, Reason: item.reason,
			CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt, RecordedAt: item.recordedAt,
		})
	}
	return result, nil
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
