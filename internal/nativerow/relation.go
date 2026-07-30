package nativerow

import (
	"encoding/binary"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/HW-Yue/Memora/internal/relation"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

const relationSchemaVersion = 1

func (repository *Repository) PutRelation(value relation.Relation) error {
	payload, err := encodeRelation(value)
	if err != nil {
		return err
	}
	recordID := revisionRecordID(value.ID, value.Revision)
	return repository.file.Put(nativestore.ObjectKindRelation, relationSchemaVersion, recordID, payload)
}

func (repository *Repository) StageRelation(transaction *nativestore.Transaction, value relation.Relation) error {
	if transaction == nil {
		return fmt.Errorf("%w: transaction is required", ErrInvalid)
	}
	if value.Revision > 1 {
		latest, err := repository.GetRelation(value.ID, true)
		if err != nil {
			return err
		}
		if latest.State == relation.StateDeleted || value.Revision != latest.Revision+1 || value.Source != latest.Source || value.Target != latest.Target || !value.CreatedAt.Equal(latest.CreatedAt) {
			return ErrRevisionConflict
		}
	}
	payload, err := encodeRelation(value)
	if err != nil {
		return err
	}
	return transaction.Put(nativestore.ObjectKindRelation, relationSchemaVersion, revisionRecordID(value.ID, value.Revision), payload)
}

func (repository *Repository) GetRelation(id string, includeDeleted bool) (relation.Relation, error) {
	ids, err := repository.file.IDs(nativestore.ObjectKindRelation)
	if err != nil {
		return relation.Relation{}, err
	}
	var latest relation.Relation
	found := false
	for _, recordID := range ids {
		if recordID != id && !strings.HasPrefix(recordID, id+"@") {
			continue
		}
		payload, err := repository.file.Get(nativestore.ObjectKindRelation, recordID)
		if err != nil {
			return relation.Relation{}, err
		}
		value, err := decodeRelation(payload)
		if err != nil {
			return relation.Relation{}, err
		}
		if value.ID == id && (!found || value.Revision > latest.Revision) {
			latest, found = value, true
		}
	}
	if !found || (!includeDeleted && latest.State == relation.StateDeleted) {
		return relation.Relation{}, nativestore.ErrNotFound
	}
	return latest, nil
}

func (repository *Repository) ListRelations(endpoint relation.Endpoint, outgoing bool) ([]relation.Relation, error) {
	ids, err := repository.file.IDs(nativestore.ObjectKindRelation)
	if err != nil {
		return nil, err
	}
	logicalIDs := make(map[string]struct{})
	for _, recordID := range ids {
		payload, err := repository.file.Get(nativestore.ObjectKindRelation, recordID)
		if err != nil {
			return nil, err
		}
		value, err := decodeRelation(payload)
		if err != nil {
			return nil, err
		}
		logicalIDs[value.ID] = struct{}{}
	}
	result := make([]relation.Relation, 0)
	for id := range logicalIDs {
		value, err := repository.GetRelation(id, false)
		if err != nil {
			continue
		}
		candidate := value.Target
		if outgoing {
			candidate = value.Source
		}
		if candidate == endpoint {
			result = append(result, value)
		}
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result, nil
}

func encodeRelation(value relation.Relation) ([]byte, error) {
	texts := []string{value.ID, value.Source.DatabaseID, value.Source.TableID, value.Source.RowID, value.Type, value.Target.DatabaseID, value.Target.TableID, value.Target.RowID, value.Description, string(value.State)}
	size := 2 + 8*5
	for _, item := range texts {
		if !utf8.ValidString(item) {
			return nil, fmt.Errorf("%w: relation text is not UTF-8", ErrInvalid)
		}
		size += 4 + len(item)
	}
	encoded := make([]byte, 0, size)
	encoded = binary.LittleEndian.AppendUint16(encoded, relationSchemaVersion)
	encoded = binary.LittleEndian.AppendUint64(encoded, value.Revision)
	encoded = binary.LittleEndian.AppendUint64(encoded, value.CommitSequence)
	encoded = binary.LittleEndian.AppendUint64(encoded, uint64(value.CreatedAt.UnixNano()))
	encoded = binary.LittleEndian.AppendUint64(encoded, uint64(value.UpdatedAt.UnixNano()))
	for _, item := range texts {
		encoded = binary.LittleEndian.AppendUint32(encoded, uint32(len(item)))
		encoded = append(encoded, item...)
	}
	return encoded, nil
}

func decodeRelation(payload []byte) (relation.Relation, error) {
	input := decoder{bytes: payload}
	version, err := input.u16()
	if err != nil || version != relationSchemaVersion {
		return relation.Relation{}, fmt.Errorf("%w: invalid relation version", ErrCorrupt)
	}
	revision, err := input.u64()
	if err != nil || revision == 0 {
		return relation.Relation{}, fmt.Errorf("%w: invalid relation revision", ErrCorrupt)
	}
	commit, err := input.u64()
	if err != nil {
		return relation.Relation{}, err
	}
	created, err := input.i64()
	if err != nil {
		return relation.Relation{}, err
	}
	updated, err := input.i64()
	if err != nil {
		return relation.Relation{}, err
	}
	texts := make([]string, 10)
	for index := range texts {
		texts[index], err = input.text()
		if err != nil {
			return relation.Relation{}, err
		}
	}
	state := relation.State(texts[9])
	if input.offset != len(payload) || (state != relation.StateLive && state != relation.StateDeleted) {
		return relation.Relation{}, fmt.Errorf("%w: invalid relation payload", ErrCorrupt)
	}
	return relation.Relation{Version: relation.Version, ID: texts[0], Source: relation.Endpoint{DatabaseID: texts[1], TableID: texts[2], RowID: texts[3]}, Type: texts[4], Target: relation.Endpoint{DatabaseID: texts[5], TableID: texts[6], RowID: texts[7]}, Description: texts[8], Revision: revision, CommitSequence: commit, State: state, CreatedAt: time.Unix(0, created).UTC(), UpdatedAt: time.Unix(0, updated).UTC()}, nil
}
