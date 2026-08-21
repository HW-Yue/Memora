package change

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const Version = "memora.committed-change/v1"

const MaxEntries = 4096

const (
	maxIdentityBytes = 4 << 10
	maxActorBytes    = 256
	maxSourceBytes   = 256
	maxReasonBytes   = 4 << 10
	maxRelatedIDs    = 256
)

var ErrInvalid = errors.New("committed change envelope is invalid")

type ObjectKind string

const (
	ObjectDatabase        ObjectKind = "database"
	ObjectTable           ObjectKind = "table"
	ObjectColumn          ObjectKind = "column"
	ObjectRow             ObjectKind = "row"
	ObjectRelation        ObjectKind = "relation"
	ObjectRouteNode       ObjectKind = "route_node"
	ObjectRouteMembership ObjectKind = "route_membership"
	ObjectConfiguration   ObjectKind = "configuration"
)

type Operation string

const (
	OperationInsert     Operation = "INSERT"
	OperationUpdate     Operation = "UPDATE"
	OperationDelete     Operation = "DELETE"
	OperationCompensate Operation = "COMPENSATE"
	OperationSplit      Operation = "SPLIT"
	OperationMerge      Operation = "MERGE"
	OperationRestore    Operation = "RESTORE"
)

// Metadata is a committed transaction's attribution: who wrote it, why, and
// where the assertion came from. It is recorded once per transaction — a write
// touching fifty Rows has one envelope, not fifty copies.
//
// The three anchor fields are omitempty because envelopes written before they
// existed carry none of them: an absent field must serialize away entirely so
// those envelopes keep checksumming to the value they were sealed with.
type Metadata struct {
	Actor             string `json:"actor"`
	Source            string `json:"source"`
	Reason            string `json:"reason"`
	SourceReceiptID   string `json:"source_receipt_id,omitempty"`
	SourceKind        string `json:"source_kind,omitempty"`
	SourceLocator     string `json:"source_locator,omitempty"`
	SourceContentHash string `json:"source_content_hash,omitempty"`
}

type Entry struct {
	ObjectKind       ObjectKind `json:"object_kind"`
	DatabaseID       string     `json:"database_id,omitempty"`
	TableID          string     `json:"table_id,omitempty"`
	ObjectID         string     `json:"object_id"`
	Operation        Operation  `json:"operation"`
	BeforeRevision   uint64     `json:"before_revision,omitempty"`
	AfterRevision    uint64     `json:"after_revision"`
	SchemaVersion    uint64     `json:"schema_version,omitempty"`
	HistoryLocator   string     `json:"history_locator,omitempty"`
	RelatedObjectIDs []string   `json:"related_object_ids,omitempty"`
}

type Envelope struct {
	Version        string    `json:"version"`
	TransactionID  string    `json:"transaction_id"`
	CommitSequence uint64    `json:"commit_sequence"`
	CommittedAt    time.Time `json:"committed_at"`
	Metadata
	DatabaseIDs []string `json:"database_ids"`
	Entries     []Entry  `json:"entries"`
	Checksum    string   `json:"checksum"`
}

func NewEnvelope(
	sequence uint64,
	committedAt time.Time,
	metadata Metadata,
	entries []Entry,
) (Envelope, error) {
	value := Envelope{
		Version: Version, CommitSequence: sequence, CommittedAt: committedAt.UTC(),
		Metadata: Metadata{
			Actor: strings.TrimSpace(metadata.Actor), Source: strings.TrimSpace(metadata.Source),
			Reason:            strings.TrimSpace(metadata.Reason),
			SourceReceiptID:   strings.TrimSpace(metadata.SourceReceiptID),
			SourceKind:        strings.TrimSpace(metadata.SourceKind),
			SourceLocator:     strings.TrimSpace(metadata.SourceLocator),
			SourceContentHash: strings.TrimSpace(metadata.SourceContentHash),
		},
		Entries: cloneEntries(entries),
	}
	sortEntries(value.Entries)
	value.DatabaseIDs = databaseScopes(value.Entries)
	checksum, err := envelopeChecksum(value)
	if err != nil {
		return Envelope{}, err
	}
	value.Checksum = checksum
	value.TransactionID = "txn_" + checksum[:32]
	if err := value.Validate(); err != nil {
		return Envelope{}, err
	}
	return value, nil
}

func (value Envelope) Validate() error {
	if value.Version != Version || value.CommitSequence == 0 || value.CommittedAt.IsZero() ||
		strings.TrimSpace(value.Actor) == "" || strings.TrimSpace(value.Source) == "" ||
		strings.TrimSpace(value.Reason) == "" || len(value.Entries) == 0 || len(value.Entries) > MaxEntries ||
		value.CommittedAt.Location() != time.UTC ||
		!validText(value.Actor, maxActorBytes) || !validText(value.Source, maxSourceBytes) ||
		!validText(value.Reason, maxReasonBytes) || !validOptionalText(value.SourceReceiptID, maxIdentityBytes) ||
		!validOptionalText(value.SourceKind, maxIdentityBytes) ||
		!validOptionalText(value.SourceLocator, maxIdentityBytes) ||
		!validOptionalText(value.SourceContentHash, maxIdentityBytes) ||
		len(value.Checksum) != sha256.Size*2 || value.TransactionID != "txn_"+value.Checksum[:32] {
		return ErrInvalid
	}
	for index, entry := range value.Entries {
		if err := entry.validate(); err != nil {
			return err
		}
		if index > 0 && compareEntry(value.Entries[index-1], entry) >= 0 {
			return fmt.Errorf("%w: entries are duplicated or out of order", ErrInvalid)
		}
	}
	wantScopes := databaseScopes(value.Entries)
	if len(wantScopes) != len(value.DatabaseIDs) {
		return fmt.Errorf("%w: database scopes", ErrInvalid)
	}
	for index := range wantScopes {
		if wantScopes[index] != value.DatabaseIDs[index] {
			return fmt.Errorf("%w: database scopes", ErrInvalid)
		}
	}
	want, err := envelopeChecksum(value)
	if err != nil || want != value.Checksum {
		return fmt.Errorf("%w: checksum", ErrInvalid)
	}
	return nil
}

func (entry Entry) validate() error {
	if !validObjectKind(entry.ObjectKind) || !validOperation(entry.Operation) ||
		strings.TrimSpace(entry.ObjectID) == "" || !validText(entry.ObjectID, maxIdentityBytes) ||
		!validOptionalText(entry.DatabaseID, maxIdentityBytes) ||
		!validOptionalText(entry.TableID, maxIdentityBytes) ||
		!validOptionalText(entry.HistoryLocator, maxIdentityBytes) || entry.AfterRevision == 0 ||
		entry.BeforeRevision >= entry.AfterRevision {
		return fmt.Errorf("%w: change entry", ErrInvalid)
	}
	if entry.ObjectKind == ObjectRow &&
		(entry.DatabaseID == "" || entry.TableID == "" || entry.SchemaVersion == 0 || entry.HistoryLocator == "") {
		return fmt.Errorf("%w: Row change locator", ErrInvalid)
	}
	if entry.TableID != "" && entry.DatabaseID == "" {
		return fmt.Errorf("%w: Table scope", ErrInvalid)
	}
	if entry.ObjectKind != ObjectConfiguration && entry.DatabaseID == "" {
		return fmt.Errorf("%w: Database scope", ErrInvalid)
	}
	switch entry.ObjectKind {
	case ObjectTable, ObjectColumn, ObjectRow, ObjectRelation, ObjectRouteNode, ObjectRouteMembership:
		if entry.TableID == "" {
			return fmt.Errorf("%w: Table scope", ErrInvalid)
		}
	}
	if (entry.ObjectKind == ObjectDatabase || entry.ObjectKind == ObjectTable ||
		entry.ObjectKind == ObjectColumn || entry.ObjectKind == ObjectRow) && entry.SchemaVersion == 0 {
		return fmt.Errorf("%w: Schema version", ErrInvalid)
	}
	if len(entry.RelatedObjectIDs) > maxRelatedIDs {
		return fmt.Errorf("%w: related object ID budget", ErrInvalid)
	}
	for index, id := range entry.RelatedObjectIDs {
		if strings.TrimSpace(id) == "" || !validText(id, maxIdentityBytes) ||
			(index > 0 && entry.RelatedObjectIDs[index-1] >= id) {
			return fmt.Errorf("%w: related object IDs", ErrInvalid)
		}
	}
	return nil
}

func validText(value string, limit int) bool {
	return utf8.ValidString(value) && len(value) > 0 && len(value) <= limit
}

func validOptionalText(value string, limit int) bool {
	return value == "" || validText(value, limit)
}

func validObjectKind(kind ObjectKind) bool {
	switch kind {
	case ObjectDatabase, ObjectTable, ObjectColumn, ObjectRow, ObjectRelation,
		ObjectRouteNode, ObjectRouteMembership, ObjectConfiguration:
		return true
	default:
		return false
	}
}

func validOperation(operation Operation) bool {
	switch operation {
	case OperationInsert, OperationUpdate, OperationDelete, OperationCompensate,
		OperationSplit, OperationMerge, OperationRestore:
		return true
	default:
		return false
	}
}

func envelopeChecksum(value Envelope) (string, error) {
	payload := struct {
		Version        string    `json:"version"`
		CommitSequence uint64    `json:"commit_sequence"`
		CommittedAt    time.Time `json:"committed_at"`
		Metadata
		DatabaseIDs []string `json:"database_ids"`
		Entries     []Entry  `json:"entries"`
	}{value.Version, value.CommitSequence, value.CommittedAt, value.Metadata, value.DatabaseIDs, value.Entries}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func cloneEntries(entries []Entry) []Entry {
	result := make([]Entry, len(entries))
	for index, entry := range entries {
		result[index] = entry
		result[index].RelatedObjectIDs = append([]string(nil), entry.RelatedObjectIDs...)
		sort.Strings(result[index].RelatedObjectIDs)
	}
	return result
}

func sortEntries(entries []Entry) {
	sort.Slice(entries, func(left, right int) bool { return compareEntry(entries[left], entries[right]) < 0 })
}

func compareEntry(left, right Entry) int {
	leftKey := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%020d", left.ObjectKind, left.DatabaseID, left.TableID, left.ObjectID, left.AfterRevision)
	rightKey := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%020d", right.ObjectKind, right.DatabaseID, right.TableID, right.ObjectID, right.AfterRevision)
	return strings.Compare(leftKey, rightKey)
}

func databaseScopes(entries []Entry) []string {
	seen := make(map[string]struct{})
	for _, entry := range entries {
		if entry.DatabaseID != "" {
			seen[entry.DatabaseID] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for id := range seen {
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}
