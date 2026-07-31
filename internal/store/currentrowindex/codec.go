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
	locatorVersion    = uint16(1)
	locatorHeaderSize = 48
	maxComponentBytes = 2048
	maxKeyBytes       = 6144
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

func encodeKey(tableID, rowID string) ([]byte, error) {
	components := []string{tableID, rowID}
	size := 2
	for _, component := range components {
		if !validComponent(component) {
			return nil, fmt.Errorf("%w: key component", ErrInvalid)
		}
		size += 2 + len(component)
	}
	if size > maxKeyBytes {
		return nil, fmt.Errorf("%w: key size", ErrInvalid)
	}
	result := make([]byte, size)
	result[0], result[1] = keyVersion, keyKindCurrentRow
	offset := 2
	for _, component := range components {
		binary.BigEndian.PutUint16(result[offset:offset+2], uint16(len(component)))
		offset += 2
		copy(result[offset:], component)
		offset += len(component)
	}
	return result, nil
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
		value.Revision == 0 ||
		value.CommitSequence == 0 {
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
