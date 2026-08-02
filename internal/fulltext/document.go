package fulltext

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/HW-Yue/Memora/internal/lexical"
)

const DocumentVersion = "memora.fulltext.document/v1"

const (
	maximumFields    = 1024
	maximumValues    = 1024
	maximumTermBytes = 2048
)

var (
	ErrInvalidDocument  = errors.New("fulltext document is invalid")
	ErrRevisionConflict = errors.New("fulltext document revision conflicts with current state")
)

type ObjectKind string

const (
	KindDatabase ObjectKind = "database"
	KindTable    ObjectKind = "table"
	KindColumn   ObjectKind = "column"
	KindRoute    ObjectKind = "route"
	KindRow      ObjectKind = "row"
)

type State string

const (
	StateLive       State = "live"
	StateDeleted    State = "deleted"
	StateSuperseded State = "superseded"
)

type ValueKind string

const (
	ValueText       ValueKind = "text"
	ValueInteger    ValueKind = "integer"
	ValueBoolean    ValueKind = "boolean"
	ValueTimestamp  ValueKind = "timestamp"
	ValueRelationID ValueKind = "relation_id"
)

type Value struct {
	kind      ValueKind
	text      string
	integer   int64
	boolean   bool
	timestamp time.Time
}

func TextValue(value string) Value         { return Value{kind: ValueText, text: value} }
func IntegerValue(value int64) Value       { return Value{kind: ValueInteger, integer: value} }
func BooleanValue(value bool) Value        { return Value{kind: ValueBoolean, boolean: value} }
func TimestampValue(value time.Time) Value { return Value{kind: ValueTimestamp, timestamp: value} }
func RelationIDValue(value string) Value   { return Value{kind: ValueRelationID, text: value} }

type Field struct {
	ID     string
	Values []Value
}

type Document struct {
	Version        string
	Kind           ObjectKind
	DatabaseID     string
	TableID        string
	ObjectID       string
	Revision       uint64
	SchemaRevision uint64
	State          State
	Complete       bool
	Fields         []Field
}

// CompiledDocument is the validated canonical input shared by the reference
// model and durable posting stores. Callers must treat Postings as immutable.
type CompiledDocument struct {
	Kind       ObjectKind
	DatabaseID string
	TableID    string
	ObjectID   string
	Revision   uint64
	State      State
	Digest     string
	Postings   []Posting
}

func Compile(value Document) (CompiledDocument, error) {
	normalized, postings, digest, err := normalizeDocument(value)
	if err != nil {
		return CompiledDocument{}, err
	}
	return CompiledDocument{
		Kind: normalized.Kind, DatabaseID: normalized.DatabaseID, TableID: normalized.TableID,
		ObjectID: normalized.ObjectID, Revision: normalized.Revision, State: normalized.State,
		Digest: digest, Postings: append([]Posting(nil), postings...),
	}, nil
}

type normalizedValue struct {
	Kind ValueKind `json:"kind"`
	Text string    `json:"text"`
}

type normalizedField struct {
	ID     string            `json:"field_id"`
	Values []normalizedValue `json:"values"`
}

type normalizedDocument struct {
	Version        string            `json:"version"`
	Kind           ObjectKind        `json:"kind"`
	DatabaseID     string            `json:"database_id"`
	TableID        string            `json:"table_id,omitempty"`
	ObjectID       string            `json:"object_id"`
	Revision       uint64            `json:"revision"`
	SchemaRevision uint64            `json:"schema_revision,omitempty"`
	State          State             `json:"state"`
	Fields         []normalizedField `json:"fields"`
}

func normalizeDocument(value Document) (normalizedDocument, []Posting, string, error) {
	if value.Version != DocumentVersion || !value.Complete || value.Revision == 0 ||
		!validKind(value.Kind) || !validState(value.State) || !validDocumentIdentity(value) {
		return normalizedDocument{}, nil, "", documentError("identity, revision, state, or completeness is invalid")
	}
	if value.Kind != KindRoute && value.SchemaRevision == 0 {
		return normalizedDocument{}, nil, "", documentError("schema revision is required")
	}
	if len(value.Fields) > maximumFields {
		return normalizedDocument{}, nil, "", documentError("field count exceeds %d", maximumFields)
	}
	if value.State == StateLive && len(value.Fields) == 0 {
		return normalizedDocument{}, nil, "", documentError("live document requires at least one field")
	}
	if value.State != StateLive && len(value.Fields) != 0 {
		return normalizedDocument{}, nil, "", documentError("inactive document cannot contain fields")
	}

	fields := make([]normalizedField, 0, len(value.Fields))
	seenFields := make(map[string]struct{}, len(value.Fields))
	for _, source := range value.Fields {
		if !validIdentity(source.ID) || len(source.Values) == 0 || len(source.Values) > maximumValues {
			return normalizedDocument{}, nil, "", documentError("field identity or value count is invalid")
		}
		if _, exists := seenFields[source.ID]; exists {
			return normalizedDocument{}, nil, "", documentError("field %q is duplicated", source.ID)
		}
		seenFields[source.ID] = struct{}{}
		field := normalizedField{ID: source.ID, Values: make([]normalizedValue, 0, len(source.Values))}
		for _, sourceValue := range source.Values {
			normalized, err := normalizeValue(sourceValue)
			if err != nil {
				return normalizedDocument{}, nil, "", err
			}
			field.Values = append(field.Values, normalized)
		}
		sort.Slice(field.Values, func(left, right int) bool {
			if field.Values[left].Kind != field.Values[right].Kind {
				return field.Values[left].Kind < field.Values[right].Kind
			}
			return field.Values[left].Text < field.Values[right].Text
		})
		fields = append(fields, field)
	}
	sort.Slice(fields, func(left, right int) bool { return fields[left].ID < fields[right].ID })
	normalized := normalizedDocument{
		Version: DocumentVersion, Kind: value.Kind, DatabaseID: value.DatabaseID, TableID: value.TableID,
		ObjectID: value.ObjectID, Revision: value.Revision, SchemaRevision: value.SchemaRevision,
		State: value.State, Fields: fields,
	}
	digest, err := documentDigest(normalized)
	if err != nil {
		return normalizedDocument{}, nil, "", err
	}
	postings, err := documentPostings(normalized)
	if err != nil {
		return normalizedDocument{}, nil, "", err
	}
	return normalized, postings, digest, nil
}

func normalizeValue(value Value) (normalizedValue, error) {
	var text string
	switch value.kind {
	case ValueText:
		text = value.text
	case ValueRelationID:
		if !validIdentity(value.text) {
			return normalizedValue{}, documentError("relation identity is invalid")
		}
		text = value.text
	case ValueInteger:
		text = strconv.FormatInt(value.integer, 10)
	case ValueBoolean:
		text = strconv.FormatBool(value.boolean)
	case ValueTimestamp:
		if value.timestamp.IsZero() {
			return normalizedValue{}, documentError("timestamp is zero")
		}
		text = value.timestamp.UTC().Format(time.RFC3339Nano)
	default:
		return normalizedValue{}, documentError("value kind is invalid")
	}
	if !utf8.ValidString(text) || strings.TrimSpace(text) == "" {
		return normalizedValue{}, documentError("value text is empty or invalid UTF-8")
	}
	return normalizedValue{Kind: value.kind, Text: text}, nil
}

func documentPostings(value normalizedDocument) ([]Posting, error) {
	postings := make([]Posting, 0)
	for _, field := range value.Fields {
		frequencies := make(map[string]uint64)
		for _, item := range field.Values {
			terms, err := lexical.Terms(item.Text)
			if err != nil {
				return nil, documentError("field %q contains invalid lexical text", field.ID)
			}
			for _, term := range terms {
				frequencies[term]++
			}
		}
		terms := make([]string, 0, len(frequencies))
		for term := range frequencies {
			terms = append(terms, term)
		}
		sort.Strings(terms)
		for _, term := range terms {
			if len(term) > maximumTermBytes {
				return nil, documentError("field %q contains a term larger than %d bytes", field.ID, maximumTermBytes)
			}
			postings = append(postings, Posting{
				Term: term, Kind: value.Kind, DatabaseID: value.DatabaseID, TableID: value.TableID,
				ObjectID: value.ObjectID, Revision: value.Revision, FieldID: field.ID,
				Frequency: frequencies[term],
			})
		}
	}
	sortPostings(postings)
	return postings, nil
}

func documentDigest(value normalizedDocument) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", documentError("encode canonical document: %v", err)
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func validKind(value ObjectKind) bool {
	return value == KindDatabase || value == KindTable || value == KindColumn || value == KindRoute || value == KindRow
}

func validState(value State) bool {
	return value == StateLive || value == StateDeleted || value == StateSuperseded
}

func validDocumentIdentity(value Document) bool {
	if !validIdentity(value.DatabaseID) || !validIdentity(value.ObjectID) {
		return false
	}
	switch value.Kind {
	case KindDatabase:
		return value.TableID == "" && value.ObjectID == value.DatabaseID
	case KindTable:
		return validIdentity(value.TableID) && value.ObjectID == value.TableID
	case KindColumn, KindRoute, KindRow:
		return validIdentity(value.TableID)
	default:
		return false
	}
}

func validIdentity(value string) bool {
	return value != "" && len(value) <= 200 && utf8.ValidString(value) && strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "\x00\r\n\t")
}

func documentError(format string, arguments ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidDocument, fmt.Sprintf(format, arguments...))
}
