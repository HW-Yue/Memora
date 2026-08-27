package currentrowindex

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/HW-Yue/Memora/internal/row"
)

const (
	keyVersion        = byte(1)
	keyKindCurrentRow = byte(1)
	// keyKindRowIDCounter holds the Table's Row ID counter, so allocating an ID
	// and writing the Row it names land in the same commit. A counter kept
	// anywhere else could disagree with the Rows it numbered.
	// See docs/storage/per-table-tree-v1.md §3.1.
	keyKindRowIDCounter = byte(2)
	locatorVersion      = uint16(1)
	locatorHeaderSize   = 48
	maxComponentBytes   = 2048
	maxKeyBytes         = 6144
)

var (
	ErrInvalid  = errors.New("Current Row Index input is invalid")
	ErrNotFound = errors.New("Current Row Index key was not found")
	ErrConflict = errors.New("Current Row Index revision conflicts")
	ErrCorrupt  = errors.New("Current Row Index is corrupt")

	locatorMagic = [8]byte{'M', 'E', 'M', 'C', 'R', 'O', '0', '1'}
)

type Locator struct {
	DatabaseID     string
	TableID        string
	RowID          string
	SchemaRevision uint64
	Revision       uint64
	CommitSequence uint64
	State          row.State
}

// encodeKey builds a current Row key.
//
// The key is the Row ID alone. It used to carry the Table ID in front of it,
// back when every Table's current Rows lived in one Tree and the Table was a
// prefix to filter on. A Table now has a Tree of its own, so which Table a Row
// belongs to is answered by which Tree the key is in — repeating it in every
// key would be the same fact stored twice.
// See docs/storage/per-table-tree-v1.md §2.
func encodeKey(rowID string) ([]byte, error) {
	if !validComponent(rowID) {
		return nil, fmt.Errorf("%w: key component", ErrInvalid)
	}
	size := 4 + len(rowID)
	if size > maxKeyBytes {
		return nil, fmt.Errorf("%w: key size", ErrInvalid)
	}
	result := make([]byte, size)
	result[0], result[1] = keyVersion, keyKindCurrentRow
	binary.BigEndian.PutUint16(result[2:4], uint16(len(rowID)))
	copy(result[4:], rowID)
	return result, nil
}

// indexPrefix is the prefix every current Row key in a Tree shares. A scan
// bounded by it covers exactly one Table, because the Tree holds exactly one —
// and it excludes the reserved keys, which sort under a different kind byte.
func indexPrefix() []byte {
	return []byte{keyVersion, keyKindCurrentRow}
}

// rowIDCounterKey is the reserved key holding this Table's Row ID counter.
func rowIDCounterKey() []byte {
	return []byte{keyVersion, keyKindRowIDCounter}
}

func encodeRowIDCounter(value uint64) []byte {
	encoded := make([]byte, 8)
	binary.BigEndian.PutUint64(encoded, value)
	return encoded
}

func decodeRowIDCounter(encoded []byte) (uint64, error) {
	if len(encoded) != 8 {
		return 0, fmt.Errorf("%w: Row ID counter", ErrCorrupt)
	}
	return binary.BigEndian.Uint64(encoded), nil
}

// RowOrderKey exposes the stable within-Table ordering component used by the
// Current Row B+ Tree so transaction overlays can merge without changing cursor semantics.
func RowOrderKey(rowID string) ([]byte, error) {
	if !validComponent(rowID) {
		return nil, fmt.Errorf("%w: Row key component", ErrInvalid)
	}
	result := make([]byte, 2+len(rowID))
	binary.BigEndian.PutUint16(result[:2], uint16(len(rowID)))
	copy(result[2:], rowID)
	return result, nil
}

func decodeKey(encoded []byte) (string, error) {
	if len(encoded) < 5 ||
		encoded[0] != keyVersion ||
		encoded[1] != keyKindCurrentRow {
		return "", fmt.Errorf("%w: current Row key header", ErrCorrupt)
	}
	length := int(binary.BigEndian.Uint16(encoded[2:4]))
	if length == 0 || length > maxComponentBytes || 4+length != len(encoded) {
		return "", fmt.Errorf("%w: current Row key component", ErrCorrupt)
	}
	value := encoded[4:]
	if !utf8.Valid(value) {
		return "", fmt.Errorf("%w: current Row key UTF-8", ErrCorrupt)
	}
	if !validComponent(string(value)) {
		return "", fmt.Errorf("%w: current Row key shape", ErrCorrupt)
	}
	return string(value), nil
}

func prefixSuccessor(prefix []byte) ([]byte, error) {
	result := bytes.Clone(prefix)
	for index := len(result) - 1; index >= 0; index-- {
		if result[index] != 0xff {
			result[index]++
			return result[:index+1], nil
		}
	}
	return nil, fmt.Errorf("%w: key prefix has no successor", ErrInvalid)
}

func encodeLocator(value Locator) ([]byte, error) {
	if err := validateLocator(value, ErrInvalid); err != nil {
		return nil, err
	}
	state, err := encodeState(value.State, ErrInvalid)
	if err != nil {
		return nil, err
	}
	size := locatorHeaderSize + len(value.DatabaseID) + len(value.TableID) + len(value.RowID)
	result := make([]byte, size)
	copy(result[:8], locatorMagic[:])
	binary.LittleEndian.PutUint16(result[8:10], locatorVersion)
	binary.LittleEndian.PutUint16(result[10:12], state)
	binary.LittleEndian.PutUint64(result[16:24], value.SchemaRevision)
	binary.LittleEndian.PutUint64(result[24:32], value.Revision)
	binary.LittleEndian.PutUint64(result[32:40], value.CommitSequence)
	binary.LittleEndian.PutUint16(result[40:42], uint16(len(value.DatabaseID)))
	binary.LittleEndian.PutUint16(result[42:44], uint16(len(value.TableID)))
	binary.LittleEndian.PutUint16(result[44:46], uint16(len(value.RowID)))
	offset := locatorHeaderSize
	for _, component := range []string{value.DatabaseID, value.TableID, value.RowID} {
		copy(result[offset:], component)
		offset += len(component)
	}
	return result, nil
}

func decodeLocator(encoded []byte) (Locator, error) {
	if len(encoded) < locatorHeaderSize ||
		!bytes.Equal(encoded[:8], locatorMagic[:]) ||
		binary.LittleEndian.Uint16(encoded[8:10]) != locatorVersion ||
		binary.LittleEndian.Uint32(encoded[12:16]) != 0 ||
		binary.LittleEndian.Uint16(encoded[46:48]) != 0 {
		return Locator{}, fmt.Errorf("%w: locator header", ErrCorrupt)
	}
	lengths := [3]int{
		int(binary.LittleEndian.Uint16(encoded[40:42])),
		int(binary.LittleEndian.Uint16(encoded[42:44])),
		int(binary.LittleEndian.Uint16(encoded[44:46])),
	}
	total := locatorHeaderSize
	for _, length := range lengths {
		if length == 0 || length > maxComponentBytes {
			return Locator{}, fmt.Errorf("%w: locator component size", ErrCorrupt)
		}
		total += length
	}
	if total != len(encoded) {
		return Locator{}, fmt.Errorf("%w: locator length", ErrCorrupt)
	}
	offset := locatorHeaderSize
	components := [3]string{}
	for index, length := range lengths {
		value := encoded[offset : offset+length]
		if !utf8.Valid(value) {
			return Locator{}, fmt.Errorf("%w: locator UTF-8", ErrCorrupt)
		}
		components[index] = string(value)
		offset += length
	}
	state, err := decodeState(binary.LittleEndian.Uint16(encoded[10:12]))
	if err != nil {
		return Locator{}, err
	}
	result := Locator{
		DatabaseID:     components[0],
		TableID:        components[1],
		RowID:          components[2],
		SchemaRevision: binary.LittleEndian.Uint64(encoded[16:24]),
		Revision:       binary.LittleEndian.Uint64(encoded[24:32]),
		CommitSequence: binary.LittleEndian.Uint64(encoded[32:40]),
		State:          state,
	}
	if err := validateLocator(result, ErrCorrupt); err != nil {
		return Locator{}, err
	}
	return result, nil
}

func validateLocator(value Locator, class error) error {
	if !validComponent(value.DatabaseID) ||
		!validComponent(value.TableID) ||
		!validComponent(value.RowID) ||
		value.SchemaRevision == 0 ||
		value.Revision == 0 {
		return fmt.Errorf("%w: locator identity or revision", class)
	}
	if _, err := encodeState(value.State, class); err != nil {
		return err
	}
	return nil
}

func validComponent(value string) bool {
	return value != "" &&
		len(value) <= maxComponentBytes &&
		utf8.ValidString(value) &&
		strings.TrimSpace(value) == value
}

func encodeState(value row.State, class error) (uint16, error) {
	switch value {
	case row.StateLive:
		return 1, nil
	case row.StateDeleted:
		return 2, nil
	case row.StateSuperseded:
		return 3, nil
	default:
		return 0, fmt.Errorf("%w: locator state", class)
	}
}

func decodeState(value uint16) (row.State, error) {
	switch value {
	case 1:
		return row.StateLive, nil
	case 2:
		return row.StateDeleted, nil
	case 3:
		return row.StateSuperseded, nil
	default:
		return "", fmt.Errorf("%w: locator state", ErrCorrupt)
	}
}
