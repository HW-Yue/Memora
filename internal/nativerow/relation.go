package nativerow

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/relation"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
	"github.com/HW-Yue/Memora/internal/store/objectindex"
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
		if value.Revision != latest.Revision+1 || value.Source != latest.Source || value.Target != latest.Target || !value.CreatedAt.Equal(latest.CreatedAt) {
			return ErrRevisionConflict
		}
		// Deleting a Relation is final: a link is cheap to recreate, so no
		// revision may follow its tombstone.
		if latest.State == relation.StateDeleted {
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
	if index := repository.relationIndex(); index != nil {
		// One descent. The Tree holds the current revision, so there is nothing
		// to sweep for and nothing to compare revisions of.
		stored, err := index.Lookup(RelationObjectKind, id)
		if errors.Is(err, objectindex.ErrNotFound) {
			return relation.Relation{}, nativestore.ErrNotFound
		}
		if err != nil {
			return relation.Relation{}, err
		}
		value, err := decodeStoredRelation(stored)
		if err != nil {
			return relation.Relation{}, err
		}
		if !includeDeleted && value.State == relation.StateDeleted {
			return relation.Relation{}, nativestore.ErrNotFound
		}
		return value, nil
	}
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
	if index := repository.relationIndex(); index != nil {
		// A range scan over one kind. What it costs is how many Relations exist,
		// not how many Relation records were ever written — and the record-log
		// version was worse than one sweep: it swept to collect the logical IDs
		// and then swept again inside GetRelation for each one.
		values, err := walkObjectRelations(index)
		if err != nil {
			return nil, err
		}
		result := make([]relation.Relation, 0)
		for _, value := range values {
			if value.State == relation.StateDeleted {
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

func (repository *Repository) AllRelationVersions() ([]relation.Relation, error) {
	ids, err := repository.file.IDs(nativestore.ObjectKindRelation)
	if err != nil {
		return nil, err
	}
	result := make([]relation.Relation, 0, len(ids))
	for _, recordID := range ids {
		payload, err := repository.file.Get(nativestore.ObjectKindRelation, recordID)
		if err != nil {
			return nil, err
		}
		value, err := decodeRelation(payload)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].ID == result[right].ID {
			return result[left].Revision < result[right].Revision
		}
		return result[left].ID < result[right].ID
	})
	return result, nil
}

func (repository *Repository) StageSnapshotRelation(transaction *nativestore.Transaction, value relation.Relation) error {
	if transaction == nil {
		return fmt.Errorf("%w: transaction is required", ErrInvalid)
	}
	payload, err := encodeRelation(value)
	if err != nil {
		return err
	}
	return transaction.Put(nativestore.ObjectKindRelation, relationSchemaVersion, revisionRecordID(value.ID, value.Revision), payload)
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

// RelationObjectKind is the Relation's slot in the objects Tree's key space. It
// is the record log's own object kind, so one Relation has one kind wherever it
// is stored and the two indexes cannot drift into disagreeing about what it is.
const RelationObjectKind = uint16(nativestore.ObjectKindRelation)

// EncodeRelation and DecodeRelation expose the Relation record codec.
//
// The objects Tree stores Relation bodies verbatim — the same bytes the record
// log holds — so the Tree is a copy of the authority rather than a translation
// of it, and a codec change stays one change instead of two that have to agree.
func EncodeRelation(value relation.Relation) ([]byte, error) { return encodeRelation(value) }

func DecodeRelation(payload []byte) (relation.Relation, error) { return decodeRelation(payload) }

// ObjectSource hands out the objects Tree that currently holds the Relations.
//
// It is asked on every read rather than captured once: a COW rebuild replaces
// the generation, and with it the Tree, while the Database stays open.
type ObjectSource interface {
	RelationObjects() *objectindex.Index

	// TableByIdentity resolves a Table by its IDs. A Row cannot be encoded or
	// decoded without its schema, so every Row read and every Row write needs
	// one — and resolving it by rebuilding the whole Catalog from the record log
	// is how a single Row write used to cost a sweep of the file.
	//
	// It is deliberately not the Authority's own DescribeTable: this is reached
	// from inside publications, where the Authority lock is already held.
	TableByIdentity(ctx context.Context, databaseID, tableID string) (catalog.Table, error)
}

// CurrentRelations returns the current revision of every Relation, tombstones
// included: a deleted Relation's tombstone is its current revision, and the
// objects Tree holds it so that "this Relation is gone" is an answer the Tree
// can give rather than a silence.
//
// It reads the whole record log, which is what it is for: seeding the Tree a
// generation is built with. The read path uses the Tree.
func (repository *Repository) CurrentRelations() ([]relation.Relation, error) {
	versions, err := repository.AllRelationVersions()
	if err != nil {
		return nil, err
	}
	latest := make(map[string]relation.Relation, len(versions))
	for _, value := range versions {
		if held, exists := latest[value.ID]; !exists || value.Revision > held.Revision {
			latest[value.ID] = value
		}
	}
	result := make([]relation.Relation, 0, len(latest))
	for _, value := range latest {
		result = append(result, value)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result, nil
}

// RelationObjectRecords encodes Relations for the objects Tree.
func RelationObjectRecords(values []relation.Relation) ([]objectindex.Record, error) {
	records := make([]objectindex.Record, 0, len(values))
	for _, value := range values {
		body, err := encodeRelation(value)
		if err != nil {
			return nil, err
		}
		records = append(records, objectindex.Record{
			Kind: RelationObjectKind, ID: value.ID, Revision: value.Revision, Body: body,
		})
	}
	return records, nil
}

// RelationObjectUpdates turns published Relation revisions into Tree updates,
// each naming the revision it succeeds. See nativerouter's equivalent: two
// independent checks of one rule is how a derived structure stays a copy.
func RelationObjectUpdates(values []relation.Relation) ([]objectindex.Update, error) {
	records, err := RelationObjectRecords(values)
	if err != nil {
		return nil, err
	}
	updates := make([]objectindex.Update, 0, len(records))
	for index, record := range records {
		updates = append(updates, objectindex.Update{
			Record: record, ExpectedRevision: values[index].Revision - 1,
		})
	}
	return updates, nil
}

// relationIndex returns the objects Tree to read through, or nil to read the
// record log.
func (repository *Repository) relationIndex() *objectindex.Index {
	if repository == nil || repository.objects == nil {
		return nil
	}
	return repository.objects.RelationObjects()
}

// decodeStoredRelation checks that the body agrees with the entry that carried
// it. The key and header say what was stored; the body says what it is.
func decodeStoredRelation(stored objectindex.Record) (relation.Relation, error) {
	value, err := decodeRelation(stored.Body)
	if err != nil {
		return relation.Relation{}, err
	}
	if value.ID != stored.ID || value.Revision != stored.Revision {
		return relation.Relation{}, fmt.Errorf("%w: relation identity mismatch", ErrCorrupt)
	}
	return value, nil
}

// walkObjectRelations reads every current Relation out of the objects Tree.
func walkObjectRelations(index *objectindex.Index) ([]relation.Relation, error) {
	result := make([]relation.Relation, 0)
	cursor := ""
	for {
		walked, err := index.Page(RelationObjectKind, cursor, relationWalkBatch)
		if err != nil {
			return nil, err
		}
		for _, stored := range walked.Records {
			value, err := decodeStoredRelation(stored)
			if err != nil {
				return nil, err
			}
			result = append(result, value)
		}
		if !walked.Truncated {
			return result, nil
		}
		cursor = walked.NextAfterID
	}
}

// relationWalkBatch is how many Relations one range scan step returns.
const relationWalkBatch = 512
