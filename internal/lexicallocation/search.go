package lexicallocation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/HW-Yue/Memora/internal/fulltext"
	"github.com/HW-Yue/Memora/internal/lexical"
)

const (
	Version       = "memora.lexical-locations/v1"
	cursorVersion = "memora.lexical-location-cursor/v1"
	maximumCursor = 4096
	maximumTerms  = 32
	maximumQuery  = 256
	maximumLimit  = 64
	minimumBytes  = 256
	maximumBytes  = 65536
)

var (
	ErrInvalid        = errors.New("lexical location request is invalid")
	ErrConflict       = errors.New("lexical location snapshot conflicts")
	ErrOutputTooSmall = errors.New("lexical location byte budget cannot contain one location")
	ErrSourceScope    = errors.New("lexical location source exceeded its database scope")
)

type Source interface {
	PostingsInDatabases(context.Context, []string, []string) ([]fulltext.Posting, error)
}

type Request struct {
	Query       string
	DatabaseIDs []string
	Cursor      string
	Limit       uint64
	ByteLimit   uint64
}

type Location struct {
	Kind              fulltext.ObjectKind `json:"kind"`
	DatabaseID        string              `json:"database_id"`
	TableID           string              `json:"table_id,omitempty"`
	ObjectID          string              `json:"object_id"`
	Revision          uint64              `json:"revision"`
	MatchedTermCount  uint64              `json:"matched_term_count"`
	MatchedFieldCount uint64              `json:"matched_field_count"`
	Frequency         uint64              `json:"frequency"`
	MatchedFieldIDs   []string            `json:"matched_field_ids"`
}

type Page struct {
	Version    string
	Locations  []Location
	Limit      uint64
	Cursor     string
	Snapshot   string
	Truncated  bool
	NextCursor string
}

type identity struct {
	kind       fulltext.ObjectKind
	databaseID string
	tableID    string
	objectID   string
	revision   uint64
}

type aggregate struct {
	location Location
	terms    map[string]struct{}
	fields   map[string]struct{}
}

type cursor struct {
	Version  string `json:"version"`
	Scope    string `json:"scope"`
	Snapshot string `json:"snapshot"`
	Offset   uint64 `json:"offset"`
	Checksum string `json:"checksum"`
}

type cursorCore struct {
	Version  string `json:"version"`
	Scope    string `json:"scope"`
	Snapshot string `json:"snapshot"`
	Offset   uint64 `json:"offset"`
}

func Search(ctx context.Context, source Source, request Request) (Page, error) {
	if ctx == nil {
		return Page{}, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return Page{}, err
	}
	terms, databaseIDs, scope, err := validateRequest(source, request)
	if err != nil {
		return Page{}, err
	}
	var continuation *cursor
	if request.Cursor != "" {
		decoded, decodeErr := decodeCursor(request.Cursor)
		if decodeErr != nil || decoded.Scope != scope {
			return Page{}, fmt.Errorf("%w: cursor", ErrInvalid)
		}
		continuation = &decoded
	}
	postings := []fulltext.Posting{}
	if len(databaseIDs) != 0 {
		postings, err = source.PostingsInDatabases(ctx, terms, databaseIDs)
		if err != nil {
			return Page{}, err
		}
	}
	locations, err := aggregatePostings(terms, databaseIDs, postings)
	if err != nil {
		return Page{}, err
	}
	snapshot, err := snapshotFor(scope, locations)
	if err != nil {
		return Page{}, fmt.Errorf("%w: snapshot", ErrInvalid)
	}
	offset := uint64(0)
	if continuation != nil {
		if continuation.Snapshot != snapshot {
			return Page{}, fmt.Errorf("%w: candidates changed", ErrConflict)
		}
		offset = continuation.Offset
		if offset == 0 || offset >= uint64(len(locations)) || offset > uint64(^uint(0)>>1) {
			return Page{}, fmt.Errorf("%w: cursor offset", ErrInvalid)
		}
	}
	end, used := offset, uint64(2)
	for end < uint64(len(locations)) && end-offset < request.Limit {
		encoded, encodeErr := json.Marshal(locations[int(end)])
		if encodeErr != nil {
			return Page{}, fmt.Errorf("%w: location encoding", ErrInvalid)
		}
		additional := uint64(len(encoded))
		if end > offset {
			additional++
		}
		if used+additional > request.ByteLimit {
			if end == offset {
				return Page{}, ErrOutputTooSmall
			}
			break
		}
		used += additional
		end++
	}
	truncated := end < uint64(len(locations))
	next := ""
	if truncated {
		next, err = encodeCursor(cursorCore{Version: cursorVersion, Scope: scope, Snapshot: snapshot, Offset: end})
		if err != nil {
			return Page{}, fmt.Errorf("%w: cursor encoding", ErrInvalid)
		}
	}
	page := Page{Version: Version, Locations: locations[int(offset):int(end)], Limit: request.Limit,
		Cursor: request.Cursor, Snapshot: snapshot, Truncated: truncated, NextCursor: next}
	if err := ValidatePage(page, request); err != nil {
		return Page{}, err
	}
	return page, nil
}

// Validate checks the public request contract without reading postings.
func Validate(request Request) error {
	_, _, _, err := normalizeRequest(request)
	return err
}

// ValidatePage protects consumers of a narrow location port from malformed or
// out-of-scope implementations. It validates the visible page, not the hidden
// remainder used to derive its snapshot.
func ValidatePage(page Page, request Request) error {
	_, databaseIDs, scope, err := normalizeRequest(request)
	if err != nil {
		return err
	}
	if page.Version != Version || page.Limit != request.Limit || page.Cursor != request.Cursor ||
		!validDigest(page.Snapshot) || page.Truncated != (page.NextCursor != "") ||
		uint64(len(page.Locations)) > request.Limit || (page.Truncated && len(page.Locations) == 0) {
		return ErrInvalid
	}
	offset := uint64(0)
	if request.Cursor != "" {
		current, decodeErr := decodeCursor(request.Cursor)
		if decodeErr != nil || current.Scope != scope || current.Snapshot != page.Snapshot {
			return ErrInvalid
		}
		offset = current.Offset
	}
	allowed := make(map[string]struct{}, len(databaseIDs))
	for _, databaseID := range databaseIDs {
		allowed[databaseID] = struct{}{}
	}
	used := uint64(2)
	for index, location := range page.Locations {
		if _, ok := allowed[location.DatabaseID]; !ok {
			return ErrSourceScope
		}
		if !validLocation(location) || (index > 0 && lessLocation(location, page.Locations[index-1])) {
			return ErrInvalid
		}
		encoded, encodeErr := json.Marshal(location)
		if encodeErr != nil {
			return ErrInvalid
		}
		used += uint64(len(encoded))
		if index > 0 {
			used++
		}
	}
	if used > request.ByteLimit {
		return ErrInvalid
	}
	if page.NextCursor != "" {
		next, decodeErr := decodeCursor(page.NextCursor)
		if decodeErr != nil || next.Scope != scope || next.Snapshot != page.Snapshot ||
			next.Offset != offset+uint64(len(page.Locations)) {
			return ErrInvalid
		}
	}
	return nil
}

func validDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func validateRequest(source Source, request Request) ([]string, []string, string, error) {
	if source == nil {
		return nil, nil, "", ErrInvalid
	}
	return normalizeRequest(request)
}

func normalizeRequest(request Request) ([]string, []string, string, error) {
	if !utf8.ValidString(request.Query) || strings.TrimSpace(request.Query) == "" ||
		utf8.RuneCountInString(request.Query) > maximumQuery || request.Limit == 0 || request.Limit > maximumLimit ||
		request.ByteLimit < minimumBytes || request.ByteLimit > maximumBytes || len(request.Cursor) > maximumCursor {
		return nil, nil, "", ErrInvalid
	}
	terms, err := lexical.UniqueTerms(request.Query)
	if err != nil || len(terms) == 0 || len(terms) > maximumTerms {
		return nil, nil, "", ErrInvalid
	}
	databaseIDs := append([]string(nil), request.DatabaseIDs...)
	sort.Strings(databaseIDs)
	for index, value := range databaseIDs {
		if strings.TrimSpace(value) == "" || !utf8.ValidString(value) || (index > 0 && value == databaseIDs[index-1]) {
			return nil, nil, "", ErrInvalid
		}
	}
	encoded, err := json.Marshal(struct {
		Version     string   `json:"version"`
		Query       string   `json:"query"`
		Terms       []string `json:"terms"`
		DatabaseIDs []string `json:"database_ids"`
	}{Version: Version, Query: request.Query, Terms: terms, DatabaseIDs: databaseIDs})
	if err != nil {
		return nil, nil, "", ErrInvalid
	}
	digest := sha256.Sum256(encoded)
	return terms, databaseIDs, "sha256:" + hex.EncodeToString(digest[:]), nil
}

func aggregatePostings(terms, databases []string, postings []fulltext.Posting) ([]Location, error) {
	allowedTerms := make(map[string]struct{}, len(terms))
	for _, term := range terms {
		allowedTerms[term] = struct{}{}
	}
	allowedDatabases := make(map[string]struct{}, len(databases))
	for _, databaseID := range databases {
		allowedDatabases[databaseID] = struct{}{}
	}
	values := make(map[identity]*aggregate)
	for _, posting := range postings {
		if _, ok := allowedDatabases[posting.DatabaseID]; !ok {
			return nil, ErrSourceScope
		}
		if _, ok := allowedTerms[posting.Term]; !ok || !validPosting(posting) {
			return nil, fmt.Errorf("%w: posting", ErrInvalid)
		}
		key := identity{kind: posting.Kind, databaseID: posting.DatabaseID, tableID: posting.TableID,
			objectID: posting.ObjectID, revision: posting.Revision}
		value := values[key]
		if value == nil {
			value = &aggregate{location: Location{Kind: posting.Kind, DatabaseID: posting.DatabaseID,
				TableID: posting.TableID, ObjectID: posting.ObjectID, Revision: posting.Revision},
				terms: map[string]struct{}{}, fields: map[string]struct{}{}}
			values[key] = value
		}
		value.terms[posting.Term] = struct{}{}
		value.fields[posting.FieldID] = struct{}{}
		if ^uint64(0)-value.location.Frequency < posting.Frequency {
			return nil, fmt.Errorf("%w: frequency overflow", ErrInvalid)
		}
		value.location.Frequency += posting.Frequency
	}
	result := make([]Location, 0, len(values))
	for _, value := range values {
		value.location.MatchedTermCount = uint64(len(value.terms))
		value.location.MatchedFieldCount = uint64(len(value.fields))
		value.location.MatchedFieldIDs = make([]string, 0, len(value.fields))
		for field := range value.fields {
			value.location.MatchedFieldIDs = append(value.location.MatchedFieldIDs, field)
		}
		sort.Strings(value.location.MatchedFieldIDs)
		result = append(result, value.location)
	}
	sort.Slice(result, func(left, right int) bool { return lessLocation(result[left], result[right]) })
	return result, nil
}

func validPosting(value fulltext.Posting) bool {
	if value.DatabaseID == "" || value.ObjectID == "" || value.FieldID == "" || value.Revision == 0 || value.Frequency == 0 {
		return false
	}
	switch value.Kind {
	case fulltext.KindDatabase:
		return value.TableID == ""
	case fulltext.KindTable:
		return value.TableID != "" && value.ObjectID == value.TableID
	case fulltext.KindColumn, fulltext.KindRoute, fulltext.KindRow:
		return value.TableID != ""
	default:
		return false
	}
}

func validLocation(value Location) bool {
	if value.DatabaseID == "" || value.ObjectID == "" || value.Revision == 0 ||
		value.MatchedTermCount == 0 || value.MatchedFieldCount == 0 || value.Frequency == 0 ||
		uint64(len(value.MatchedFieldIDs)) != value.MatchedFieldCount {
		return false
	}
	for index, fieldID := range value.MatchedFieldIDs {
		if fieldID == "" || (index > 0 && fieldID <= value.MatchedFieldIDs[index-1]) {
			return false
		}
	}
	switch value.Kind {
	case fulltext.KindDatabase:
		return value.TableID == ""
	case fulltext.KindTable:
		return value.TableID != "" && value.ObjectID == value.TableID
	case fulltext.KindColumn, fulltext.KindRoute, fulltext.KindRow:
		return value.TableID != ""
	default:
		return false
	}
}

func lessLocation(left, right Location) bool {
	if left.MatchedTermCount != right.MatchedTermCount {
		return left.MatchedTermCount > right.MatchedTermCount
	}
	if left.MatchedFieldCount != right.MatchedFieldCount {
		return left.MatchedFieldCount > right.MatchedFieldCount
	}
	if left.Frequency != right.Frequency {
		return left.Frequency > right.Frequency
	}
	if kindRank(left.Kind) != kindRank(right.Kind) {
		return kindRank(left.Kind) > kindRank(right.Kind)
	}
	if left.DatabaseID != right.DatabaseID {
		return left.DatabaseID < right.DatabaseID
	}
	if left.TableID != right.TableID {
		return left.TableID < right.TableID
	}
	if left.Kind != right.Kind {
		return left.Kind < right.Kind
	}
	if left.ObjectID != right.ObjectID {
		return left.ObjectID < right.ObjectID
	}
	return left.Revision < right.Revision
}

func kindRank(kind fulltext.ObjectKind) int {
	switch kind {
	case fulltext.KindRow:
		return 5
	case fulltext.KindRoute:
		return 4
	case fulltext.KindColumn:
		return 3
	case fulltext.KindTable:
		return 2
	case fulltext.KindDatabase:
		return 1
	default:
		return 0
	}
}

func snapshotFor(scope string, locations []Location) (string, error) {
	encoded, err := json.Marshal(struct {
		Scope     string     `json:"scope"`
		Locations []Location `json:"locations"`
	}{Scope: scope, Locations: locations})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func encodeCursor(core cursorCore) (string, error) {
	checksum, err := cursorChecksum(core)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(cursor{Version: core.Version, Scope: core.Scope,
		Snapshot: core.Snapshot, Offset: core.Offset, Checksum: checksum})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func decodeCursor(value string) (cursor, error) {
	encoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(encoded) == 0 || len(encoded) > maximumCursor {
		return cursor{}, ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var decoded cursor
	if err := decoder.Decode(&decoded); err != nil || decoded.Version != cursorVersion || decoded.Scope == "" ||
		decoded.Snapshot == "" || decoded.Offset == 0 || decoded.Checksum == "" {
		return cursor{}, ErrInvalid
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return cursor{}, ErrInvalid
	}
	canonical, err := json.Marshal(decoded)
	if err != nil || !bytes.Equal(encoded, canonical) {
		return cursor{}, ErrInvalid
	}
	want, err := cursorChecksum(cursorCore{Version: decoded.Version, Scope: decoded.Scope,
		Snapshot: decoded.Snapshot, Offset: decoded.Offset})
	if err != nil || subtle.ConstantTimeCompare([]byte(decoded.Checksum), []byte(want)) != 1 {
		return cursor{}, ErrInvalid
	}
	return decoded, nil
}

func cursorChecksum(core cursorCore) (string, error) {
	encoded, err := json.Marshal(core)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}
