package fulltextindex

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/HW-Yue/Memora/internal/fulltext"
	"github.com/HW-Yue/Memora/internal/lexical"
)

const (
	keyVersion     = byte(1)
	keyKindObject  = byte(1)
	keyKindOwner   = byte(2)
	keyKindPosting = byte(3)
	// keyKindMetadata holds the index's own bookkeeping rather than anything a
	// caller stored. It lives in this Tree, and not beside it, so that a
	// bookkeeping value and the documents it describes are written in one
	// transaction and can never disagree. It is a separate key kind rather than
	// a sixth fulltext.ObjectKind precisely so that Objects and AllPostings —
	// which scan keyKindObject and keyKindPosting — never see it.
	keyKindMetadata = byte(4)
	// metadataCursor names the one metadata entry that exists today: how far
	// through the committed change log this derived index has been brought.
	metadataCursor   = byte(1)
	codecVersion     = uint16(1)
	maximumComponent = 2048
	objectHeaderSize = 32
	postingValueSize = 32
	cursorValueSize  = 24
)

var (
	objectMagic  = [8]byte{'M', 'E', 'M', 'F', 'T', 'O', '0', '1'}
	postingMagic = [8]byte{'M', 'E', 'M', 'F', 'T', 'P', '0', '1'}
	cursorMagic  = [8]byte{'M', 'E', 'M', 'F', 'T', 'C', '0', '1'}
)

type objectRecord struct {
	kind       fulltext.ObjectKind
	databaseID string
	tableID    string
	objectID   string
	revision   uint64
	state      fulltext.State
	digest     string
}

type ownerRecord struct {
	kind     fulltext.ObjectKind
	objectID string
	term     string
	fieldID  string
}

type postingRecord struct {
	posting fulltext.Posting
}

func encodeObjectKey(kind fulltext.ObjectKind, objectID string) ([]byte, error) {
	kindCode, err := encodeKind(kind, ErrInvalid)
	if err != nil || !validIdentity(objectID) {
		return nil, fmt.Errorf("%w: object key", ErrInvalid)
	}
	result := []byte{keyVersion, keyKindObject, kindCode}
	return appendComponent(result, objectID, false), nil
}

func decodeObjectKey(encoded []byte) (fulltext.ObjectKind, string, error) {
	if len(encoded) < 6 || encoded[0] != keyVersion || encoded[1] != keyKindObject {
		return "", "", fmt.Errorf("%w: object key header", ErrCorrupt)
	}
	kind, err := decodeKind(encoded[2], ErrCorrupt)
	if err != nil {
		return "", "", err
	}
	objectID, offset, err := readComponent(encoded, 3, false)
	if err != nil || offset != len(encoded) || !validIdentity(objectID) {
		return "", "", fmt.Errorf("%w: object key shape", ErrCorrupt)
	}
	return kind, objectID, nil
}

func ownerPrefix(kind fulltext.ObjectKind, objectID string) ([]byte, error) {
	kindCode, err := encodeKind(kind, ErrInvalid)
	if err != nil || !validIdentity(objectID) {
		return nil, fmt.Errorf("%w: owner prefix", ErrInvalid)
	}
	result := []byte{keyVersion, keyKindOwner, kindCode}
	return appendComponent(result, objectID, false), nil
}

func encodeOwnerKey(posting fulltext.Posting) ([]byte, error) {
	result, err := ownerPrefix(posting.Kind, posting.ObjectID)
	if err != nil || !validTerm(posting.Term) || !validIdentity(posting.FieldID) {
		return nil, fmt.Errorf("%w: owner key", ErrInvalid)
	}
	result = appendComponent(result, posting.Term, false)
	return appendComponent(result, posting.FieldID, false), nil
}

func decodeOwnerKey(encoded []byte) (ownerRecord, error) {
	if len(encoded) < 9 || encoded[0] != keyVersion || encoded[1] != keyKindOwner {
		return ownerRecord{}, fmt.Errorf("%w: owner key header", ErrCorrupt)
	}
	kind, err := decodeKind(encoded[2], ErrCorrupt)
	if err != nil {
		return ownerRecord{}, err
	}
	offset := 3
	objectID, offset, err := readComponent(encoded, offset, false)
	if err != nil {
		return ownerRecord{}, err
	}
	term, offset, err := readComponent(encoded, offset, false)
	if err != nil {
		return ownerRecord{}, err
	}
	fieldID, offset, err := readComponent(encoded, offset, false)
	if err != nil || offset != len(encoded) || !validIdentity(objectID) || !validTerm(term) || !validIdentity(fieldID) {
		return ownerRecord{}, fmt.Errorf("%w: owner key shape", ErrCorrupt)
	}
	return ownerRecord{kind: kind, objectID: objectID, term: term, fieldID: fieldID}, nil
}

func postingPrefix(term string) ([]byte, error) {
	if !validTerm(term) {
		return nil, fmt.Errorf("%w: posting term", ErrInvalid)
	}
	return appendComponent([]byte{keyVersion, keyKindPosting}, term, false), nil
}

func postingDatabasePrefix(term, databaseID string) ([]byte, error) {
	result, err := postingPrefix(term)
	if err != nil || !validIdentity(databaseID) {
		return nil, fmt.Errorf("%w: posting Database prefix", ErrInvalid)
	}
	return appendComponent(result, databaseID, false), nil
}

func allPostingPrefix() []byte { return []byte{keyVersion, keyKindPosting} }

func encodePostingKey(posting fulltext.Posting) ([]byte, error) {
	kindCode, err := encodeKind(posting.Kind, ErrInvalid)
	if err != nil || !validPostingIdentity(posting) {
		return nil, fmt.Errorf("%w: posting key", ErrInvalid)
	}
	result := appendComponent([]byte{keyVersion, keyKindPosting}, posting.Term, false)
	result = appendComponent(result, posting.DatabaseID, false)
	result = appendComponent(result, posting.TableID, true)
	result = append(result, kindCode)
	result = appendComponent(result, posting.ObjectID, false)
	return appendComponent(result, posting.FieldID, false), nil
}

func decodePostingKey(encoded []byte) (postingRecord, error) {
	if len(encoded) < 12 || encoded[0] != keyVersion || encoded[1] != keyKindPosting {
		return postingRecord{}, fmt.Errorf("%w: posting key header", ErrCorrupt)
	}
	offset := 2
	term, offset, err := readComponent(encoded, offset, false)
	if err != nil {
		return postingRecord{}, err
	}
	databaseID, offset, err := readComponent(encoded, offset, false)
	if err != nil {
		return postingRecord{}, err
	}
	tableID, offset, err := readComponent(encoded, offset, true)
	if err != nil || offset >= len(encoded) {
		return postingRecord{}, fmt.Errorf("%w: posting key scope", ErrCorrupt)
	}
	kind, err := decodeKind(encoded[offset], ErrCorrupt)
	if err != nil {
		return postingRecord{}, err
	}
	offset++
	objectID, offset, err := readComponent(encoded, offset, false)
	if err != nil {
		return postingRecord{}, err
	}
	fieldID, offset, err := readComponent(encoded, offset, false)
	posting := fulltext.Posting{
		Term: term, Kind: kind, DatabaseID: databaseID, TableID: tableID,
		ObjectID: objectID, FieldID: fieldID,
	}
	if err != nil || offset != len(encoded) || !validPostingIdentity(posting) {
		return postingRecord{}, fmt.Errorf("%w: posting key shape", ErrCorrupt)
	}
	return postingRecord{posting: posting}, nil
}

func encodeObjectValue(value objectRecord) ([]byte, error) {
	if !validObjectRecord(value) {
		return nil, fmt.Errorf("%w: object value", ErrInvalid)
	}
	state, err := encodeState(value.state, ErrInvalid)
	if err != nil {
		return nil, err
	}
	result := make([]byte, objectHeaderSize, objectHeaderSize+len(value.databaseID)+len(value.tableID)+len(value.digest))
	copy(result[:8], objectMagic[:])
	binary.LittleEndian.PutUint16(result[8:10], codecVersion)
	binary.LittleEndian.PutUint16(result[10:12], state)
	binary.LittleEndian.PutUint64(result[16:24], value.revision)
	binary.LittleEndian.PutUint16(result[24:26], uint16(len(value.databaseID)))
	binary.LittleEndian.PutUint16(result[26:28], uint16(len(value.tableID)))
	binary.LittleEndian.PutUint16(result[28:30], uint16(len(value.digest)))
	result = append(result, value.databaseID...)
	result = append(result, value.tableID...)
	result = append(result, value.digest...)
	return result, nil
}

func decodeObjectValue(encoded []byte, kind fulltext.ObjectKind, objectID string) (objectRecord, error) {
	if len(encoded) < objectHeaderSize || !bytes.Equal(encoded[:8], objectMagic[:]) ||
		binary.LittleEndian.Uint16(encoded[8:10]) != codecVersion ||
		binary.LittleEndian.Uint32(encoded[12:16]) != 0 || binary.LittleEndian.Uint16(encoded[30:32]) != 0 {
		return objectRecord{}, fmt.Errorf("%w: object value header", ErrCorrupt)
	}
	lengths := []int{
		int(binary.LittleEndian.Uint16(encoded[24:26])),
		int(binary.LittleEndian.Uint16(encoded[26:28])),
		int(binary.LittleEndian.Uint16(encoded[28:30])),
	}
	total := objectHeaderSize + lengths[0] + lengths[1] + lengths[2]
	if total != len(encoded) {
		return objectRecord{}, fmt.Errorf("%w: object value length", ErrCorrupt)
	}
	offset := objectHeaderSize
	parts := make([]string, 3)
	for index, length := range lengths {
		part := encoded[offset : offset+length]
		if !utf8.Valid(part) {
			return objectRecord{}, fmt.Errorf("%w: object value UTF-8", ErrCorrupt)
		}
		parts[index] = string(part)
		offset += length
	}
	state, err := decodeState(binary.LittleEndian.Uint16(encoded[10:12]), ErrCorrupt)
	if err != nil {
		return objectRecord{}, err
	}
	result := objectRecord{
		kind: kind, objectID: objectID, databaseID: parts[0], tableID: parts[1],
		revision: binary.LittleEndian.Uint64(encoded[16:24]), state: state, digest: parts[2],
	}
	if !validObjectRecord(result) {
		return objectRecord{}, fmt.Errorf("%w: object value payload", ErrCorrupt)
	}
	return result, nil
}

func encodePostingValue(revision, frequency uint64) ([]byte, error) {
	if revision == 0 || frequency == 0 {
		return nil, fmt.Errorf("%w: posting value", ErrInvalid)
	}
	result := make([]byte, postingValueSize)
	copy(result[:8], postingMagic[:])
	binary.LittleEndian.PutUint16(result[8:10], codecVersion)
	binary.LittleEndian.PutUint64(result[16:24], revision)
	binary.LittleEndian.PutUint64(result[24:32], frequency)
	return result, nil
}

func decodePostingValue(encoded []byte) (uint64, uint64, error) {
	if len(encoded) != postingValueSize || !bytes.Equal(encoded[:8], postingMagic[:]) ||
		binary.LittleEndian.Uint16(encoded[8:10]) != codecVersion ||
		binary.LittleEndian.Uint16(encoded[10:12]) != 0 || binary.LittleEndian.Uint32(encoded[12:16]) != 0 {
		return 0, 0, fmt.Errorf("%w: posting value header", ErrCorrupt)
	}
	revision := binary.LittleEndian.Uint64(encoded[16:24])
	frequency := binary.LittleEndian.Uint64(encoded[24:32])
	if revision == 0 || frequency == 0 {
		return 0, 0, fmt.Errorf("%w: posting value payload", ErrCorrupt)
	}
	return revision, frequency, nil
}

// cursorKey is the single fixed key the catch-up cursor lives under.
func cursorKey() []byte {
	return []byte{keyVersion, keyKindMetadata, metadataCursor}
}

func encodeCursorValue(sequence uint64) ([]byte, error) {
	if sequence == 0 {
		return nil, fmt.Errorf("%w: cursor value", ErrInvalid)
	}
	result := make([]byte, cursorValueSize)
	copy(result[:8], cursorMagic[:])
	binary.LittleEndian.PutUint16(result[8:10], codecVersion)
	binary.LittleEndian.PutUint64(result[16:24], sequence)
	return result, nil
}

func decodeCursorValue(encoded []byte) (uint64, error) {
	if len(encoded) != cursorValueSize || !bytes.Equal(encoded[:8], cursorMagic[:]) ||
		binary.LittleEndian.Uint16(encoded[8:10]) != codecVersion ||
		binary.LittleEndian.Uint16(encoded[10:12]) != 0 || binary.LittleEndian.Uint32(encoded[12:16]) != 0 {
		return 0, fmt.Errorf("%w: cursor value header", ErrCorrupt)
	}
	sequence := binary.LittleEndian.Uint64(encoded[16:24])
	if sequence == 0 {
		return 0, fmt.Errorf("%w: cursor value payload", ErrCorrupt)
	}
	return sequence, nil
}

func appendComponent(target []byte, value string, allowEmpty bool) []byte {
	result := make([]byte, len(target)+2+len(value))
	copy(result, target)
	binary.BigEndian.PutUint16(result[len(target):len(target)+2], uint16(len(value)))
	copy(result[len(target)+2:], value)
	return result
}

func readComponent(encoded []byte, offset int, allowEmpty bool) (string, int, error) {
	if offset+2 > len(encoded) {
		return "", offset, fmt.Errorf("%w: key component length", ErrCorrupt)
	}
	length := int(binary.BigEndian.Uint16(encoded[offset : offset+2]))
	offset += 2
	if (!allowEmpty && length == 0) || length > maximumComponent || offset+length > len(encoded) {
		return "", offset, fmt.Errorf("%w: key component size", ErrCorrupt)
	}
	value := encoded[offset : offset+length]
	if !utf8.Valid(value) {
		return "", offset, fmt.Errorf("%w: key component UTF-8", ErrCorrupt)
	}
	return string(value), offset + length, nil
}

func prefixSuccessor(prefix []byte) ([]byte, error) {
	result := bytes.Clone(prefix)
	for index := len(result) - 1; index >= 0; index-- {
		if result[index] != 0xff {
			result[index]++
			return result[:index+1], nil
		}
	}
	return nil, fmt.Errorf("%w: prefix has no successor", ErrInvalid)
}

func encodeKind(value fulltext.ObjectKind, class error) (byte, error) {
	switch value {
	case fulltext.KindDatabase:
		return 1, nil
	case fulltext.KindTable:
		return 2, nil
	case fulltext.KindColumn:
		return 3, nil
	case fulltext.KindRoute:
		return 4, nil
	case fulltext.KindRow:
		return 5, nil
	default:
		return 0, fmt.Errorf("%w: object kind", class)
	}
}

func decodeKind(value byte, class error) (fulltext.ObjectKind, error) {
	switch value {
	case 1:
		return fulltext.KindDatabase, nil
	case 2:
		return fulltext.KindTable, nil
	case 3:
		return fulltext.KindColumn, nil
	case 4:
		return fulltext.KindRoute, nil
	case 5:
		return fulltext.KindRow, nil
	default:
		return "", fmt.Errorf("%w: object kind", class)
	}
}

func encodeState(value fulltext.State, class error) (uint16, error) {
	switch value {
	case fulltext.StateLive:
		return 1, nil
	case fulltext.StateDeleted:
		return 2, nil
	case fulltext.StateSuperseded:
		return 3, nil
	default:
		return 0, fmt.Errorf("%w: object state", class)
	}
}

func decodeState(value uint16, class error) (fulltext.State, error) {
	switch value {
	case 1:
		return fulltext.StateLive, nil
	case 2:
		return fulltext.StateDeleted, nil
	case 3:
		return fulltext.StateSuperseded, nil
	default:
		return "", fmt.Errorf("%w: object state", class)
	}
}

func validObjectRecord(value objectRecord) bool {
	if !validIdentity(value.databaseID) || !validIdentity(value.objectID) || value.revision == 0 || !validDigest(value.digest) {
		return false
	}
	switch value.kind {
	case fulltext.KindDatabase:
		return value.tableID == "" && value.objectID == value.databaseID && validState(value.state)
	case fulltext.KindTable:
		return validIdentity(value.tableID) && value.objectID == value.tableID && validState(value.state)
	case fulltext.KindColumn, fulltext.KindRoute, fulltext.KindRow:
		return validIdentity(value.tableID) && validState(value.state)
	default:
		return false
	}
}

func validPostingIdentity(value fulltext.Posting) bool {
	if !validTerm(value.Term) || !validIdentity(value.DatabaseID) || !validIdentity(value.ObjectID) ||
		!validIdentity(value.FieldID) {
		return false
	}
	switch value.Kind {
	case fulltext.KindDatabase:
		return value.TableID == "" && value.ObjectID == value.DatabaseID
	case fulltext.KindTable:
		return validIdentity(value.TableID) && value.ObjectID == value.TableID
	case fulltext.KindColumn, fulltext.KindRoute, fulltext.KindRow:
		return validIdentity(value.TableID)
	default:
		return false
	}
}

func validIdentity(value string) bool {
	return value != "" && len(value) <= 200 && utf8.ValidString(value) && strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "\x00\r\n\t")
}

func validTerm(value string) bool {
	if value == "" || len(value) > maximumComponent || !utf8.ValidString(value) || strings.TrimSpace(value) != value ||
		strings.ContainsAny(value, "\x00\r\n\t") {
		return false
	}
	terms, err := lexical.Terms(value)
	return err == nil && len(terms) == 1 && terms[0] == value
}

func validDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil && len(decoded) == 32
}

func validState(value fulltext.State) bool {
	return value == fulltext.StateLive || value == fulltext.StateDeleted || value == fulltext.StateSuperseded
}
