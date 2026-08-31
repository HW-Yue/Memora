package objectindex

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"unicode/utf8"
)

var (
	ErrInvalid  = errors.New("Object Index input is invalid")
	ErrNotFound = errors.New("Object Index key was not found")
	ErrConflict = errors.New("Object Index record is immutable")
	ErrCorrupt  = errors.New("Object Index is corrupt")
)

var recordMagic = [8]byte{'M', 'E', 'M', 'O', 'B', 'J', '0', '1'}

const (
	keyVersion byte = 1
	// recordVersion 2 added the revision. A Tree written by version 1 is not
	// read: the objects Tree is derived from the record log and a generation
	// that carries one is rebuilt rather than upgraded in place.
	recordVersion uint16 = 2

	recordHeaderSize = 28
	maxIDBytes       = 2048
	// maxBodyBytes is the per-record budget from the Tablespace design: a Page is
	// 16 KiB and a leaf holds several entries, so one encoded object targets
	// 8 KiB. Objects above it need overflow Pages, which do not exist yet.
	maxBodyBytes = 8 << 10
)

// Record is one stored object: its type, its identity, which revision of it
// this is, and its bytes. The bytes are opaque here — every object kind keeps
// its own codec — so this tree stays the one place that knows how to put an
// object on a Page and find it again.
//
// Revision numbers from 1 and is carried in the header rather than left to the
// body, because the Tree compares revisions on every write and cannot decode a
// body to do it.
type Record struct {
	Kind     uint16
	ID       string
	Revision uint64
	Body     []byte
}

// recordKey lays the kind before the ID so one kind occupies a contiguous key
// range. That is what makes walking a single kind a range scan rather than a
// pass over every record in the Database.
func recordKey(kind uint16, id string) ([]byte, error) {
	if err := validateIdentity(kind, id, ErrInvalid); err != nil {
		return nil, err
	}
	key := make([]byte, 0, 3+len(id))
	key = append(key, keyVersion)
	key = binary.BigEndian.AppendUint16(key, kind)
	return append(key, id...), nil
}

func kindPrefix(kind uint16) ([]byte, error) {
	if kind == 0 {
		return nil, fmt.Errorf("%w: object kind is required", ErrInvalid)
	}
	prefix := make([]byte, 0, 3)
	prefix = append(prefix, keyVersion)
	return binary.BigEndian.AppendUint16(prefix, kind), nil
}

// prefixSuccessor is the exclusive end of a prefix's key range.
func prefixSuccessor(prefix []byte) ([]byte, error) {
	result := append([]byte(nil), prefix...)
	for index := len(result) - 1; index >= 0; index-- {
		if result[index] != 0xff {
			result[index]++
			return result[:index+1], nil
		}
	}
	return nil, fmt.Errorf("%w: key prefix has no successor", ErrInvalid)
}

func encodeRecord(value Record) ([]byte, error) {
	if err := validateRecord(value, ErrInvalid); err != nil {
		return nil, err
	}
	encoded := make([]byte, recordHeaderSize+len(value.ID)+len(value.Body))
	copy(encoded[:8], recordMagic[:])
	binary.LittleEndian.PutUint16(encoded[8:10], recordVersion)
	binary.LittleEndian.PutUint16(encoded[10:12], value.Kind)
	binary.LittleEndian.PutUint32(encoded[12:16], uint32(len(value.ID)))
	binary.LittleEndian.PutUint32(encoded[16:20], uint32(len(value.Body)))
	binary.LittleEndian.PutUint64(encoded[20:28], value.Revision)
	copy(encoded[recordHeaderSize:], value.ID)
	copy(encoded[recordHeaderSize+len(value.ID):], value.Body)
	return encoded, nil
}

// decodeRecord rebuilds a Record and re-checks the identity it claims. The key
// says what was looked up; the value says what was stored. A disagreement is
// corruption, so both are carried and the caller compares them.
func decodeRecord(encoded []byte) (Record, error) {
	if len(encoded) < recordHeaderSize ||
		!bytes.Equal(encoded[:8], recordMagic[:]) ||
		binary.LittleEndian.Uint16(encoded[8:10]) != recordVersion {
		return Record{}, fmt.Errorf("%w: record header", ErrCorrupt)
	}
	kind := binary.LittleEndian.Uint16(encoded[10:12])
	idLength := int(binary.LittleEndian.Uint32(encoded[12:16]))
	bodyLength := int(binary.LittleEndian.Uint32(encoded[16:20]))
	if idLength == 0 || idLength > maxIDBytes || bodyLength > maxBodyBytes ||
		recordHeaderSize+idLength+bodyLength != len(encoded) {
		return Record{}, fmt.Errorf("%w: record length", ErrCorrupt)
	}
	id := encoded[recordHeaderSize : recordHeaderSize+idLength]
	if !utf8.Valid(id) {
		return Record{}, fmt.Errorf("%w: record ID UTF-8", ErrCorrupt)
	}
	value := Record{
		Kind:     kind,
		ID:       string(id),
		Revision: binary.LittleEndian.Uint64(encoded[20:28]),
		// The body is opaque bytes, so it is taken verbatim rather than validated.
		Body: append([]byte(nil), encoded[recordHeaderSize+idLength:]...),
	}
	if err := validateRecord(value, ErrCorrupt); err != nil {
		return Record{}, err
	}
	return value, nil
}

func validateRecord(value Record, class error) error {
	if err := validateIdentity(value.Kind, value.ID, class); err != nil {
		return err
	}
	if value.Revision == 0 {
		return fmt.Errorf("%w: record %q of kind %d has no revision", class, value.ID, value.Kind)
	}
	// A leaf entry shares a 16 KiB Page with its neighbours, so one encoded
	// record is capped well below a full Page. Refusing by name beats splitting
	// an object across Pages before overflow Pages exist.
	if len(value.Body) > maxBodyBytes {
		return fmt.Errorf(
			"%w: record %q of kind %d encodes to %d bytes, over the %d byte record limit",
			class, value.ID, value.Kind, len(value.Body), maxBodyBytes,
		)
	}
	return nil
}

func validateIdentity(kind uint16, id string, class error) error {
	if kind == 0 {
		return fmt.Errorf("%w: object kind is required", class)
	}
	if id == "" || len(id) > maxIDBytes || !utf8.ValidString(id) {
		return fmt.Errorf("%w: record ID %q", class, id)
	}
	return nil
}
