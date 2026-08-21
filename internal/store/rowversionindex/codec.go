package rowversionindex

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/HW-Yue/Memora/internal/row"
)

const (
	keyVersion        = byte(1)
	keyRevision       = byte(1)
	keyCommit         = byte(2)
	keyIdentity       = byte(3)
	keySnapshotWater  = byte(4)
	keyLegacy         = byte(5)
	locatorVersion    = uint16(1)
	locatorHeaderSize = 48
	highWaterVersion  = uint16(1)
	highWaterSize     = 24
	maxComponentBytes = 2048
	// maxBodyBytes is the per-record budget from the Tablespace design: a Page is
	// 16 KiB and a leaf holds several entries, so one encoded Row targets 8 KiB.
	// Rows above it need overflow Pages, which do not exist yet.
	maxBodyBytes = 8 << 10
)

var (
	ErrInvalid  = errors.New("Row Version Index input is invalid")
	ErrNotFound = errors.New("Row Version Index key was not found")
	ErrConflict = errors.New("Row Version Index immutable locator conflicts")
	ErrCorrupt  = errors.New("Row Version Index is corrupt")

	locatorMagic   = [8]byte{'M', 'E', 'M', 'R', 'V', 'O', '0', '1'}
	highWaterMagic = [8]byte{'M', 'E', 'M', 'S', 'H', 'W', '0', '1'}
)

type Locator struct {
	DatabaseID     string
	TableID        string
	RowID          string
	SchemaRevision uint64
	Revision       uint64
	CommitSequence uint64
	State          row.State
	// Body is the encoded Row itself, carried only under the (rowID, revision)
	// key — that key is the clustered index, so reaching its leaf is reaching the
	// data. The secondary keys (identity, commit, legacy) store the same Locator
	// with Body empty; duplicating the Row under each of them would multiply the
	// file size by the number of keys.
	//
	// It is a string rather than a []byte so a Locator stays comparable: the
	// append path decides that a published revision is immutable by comparing the
	// stored Locator to the incoming one, and that check has to cover the Row.
	//
	// It never serializes. An encoded Row is arbitrary bytes, and json.Marshal
	// rewrites invalid UTF-8 in a string to U+FFFD, so letting it reach JSON
	// would silently corrupt the Row and destabilise any digest taken over it.
	Body string `json:"-"`
}

func revisionKey(rowID string, revision uint64) ([]byte, error) {
	if revision == 0 {
		return nil, fmt.Errorf("%w: zero revision", ErrInvalid)
	}
	key, err := rowPrefix(keyRevision, rowID)
	if err != nil {
		return nil, err
	}
	return binary.BigEndian.AppendUint64(key, revision), nil
}

func commitKey(rowID string, sequence, revision uint64) ([]byte, error) {
	if sequence == 0 || revision == 0 {
		return nil, fmt.Errorf("%w: zero commit sequence or revision", ErrInvalid)
	}
	key, err := rowPrefix(keyCommit, rowID)
	if err != nil {
		return nil, err
	}
	key = binary.BigEndian.AppendUint64(key, math.MaxUint64-sequence)
	return binary.BigEndian.AppendUint64(key, math.MaxUint64-revision), nil
}

func commitStart(rowID string, sequence uint64) ([]byte, error) {
	if sequence == 0 {
		return nil, fmt.Errorf("%w: zero commit sequence", ErrInvalid)
	}
	key, err := rowPrefix(keyCommit, rowID)
	if err != nil {
		return nil, err
	}
	return binary.BigEndian.AppendUint64(key, math.MaxUint64-sequence), nil
}

func commitRowPrefix(rowID string) ([]byte, error) {
	return rowPrefix(keyCommit, rowID)
}

func identityKey(rowID string) ([]byte, error) {
	return rowPrefix(keyIdentity, rowID)
}

func legacyKey(rowID string) ([]byte, error) {
	return rowPrefix(keyLegacy, rowID)
}

func snapshotHighWaterKey() []byte { return []byte{keyVersion, keySnapshotWater} }

func rowPrefix(kind byte, rowID string) ([]byte, error) {
	if kind < keyRevision || kind > keyLegacy || kind == keySnapshotWater || !validComponent(rowID) {
		return nil, fmt.Errorf("%w: key kind or Row ID", ErrInvalid)
	}
	result := make([]byte, 4+len(rowID))
	result[0], result[1] = keyVersion, kind
	binary.BigEndian.PutUint16(result[2:4], uint16(len(rowID)))
	copy(result[4:], rowID)
	return result, nil
}

func encodeHighWater(sequence uint64) []byte {
	result := make([]byte, highWaterSize)
	copy(result[:8], highWaterMagic[:])
	binary.LittleEndian.PutUint16(result[8:10], highWaterVersion)
	binary.LittleEndian.PutUint64(result[16:24], sequence)
	return result
}

func decodeHighWater(encoded []byte) (uint64, error) {
	if len(encoded) != highWaterSize ||
		!bytes.Equal(encoded[:8], highWaterMagic[:]) ||
		binary.LittleEndian.Uint16(encoded[8:10]) != highWaterVersion ||
		binary.LittleEndian.Uint16(encoded[10:12]) != 0 ||
		binary.LittleEndian.Uint32(encoded[12:16]) != 0 {
		return 0, fmt.Errorf("%w: snapshot high-water", ErrCorrupt)
	}
	return binary.LittleEndian.Uint64(encoded[16:24]), nil
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
	size := locatorHeaderSize + len(value.DatabaseID) + len(value.TableID) + len(value.RowID) + len(value.Body)
	result := make([]byte, size)
	copy(result[:8], locatorMagic[:])
	binary.LittleEndian.PutUint16(result[8:10], locatorVersion)
	binary.LittleEndian.PutUint16(result[10:12], state)
	// A bodyless Locator encodes to exactly the bytes it always did: the body
	// length occupies what used to be a reserved zero word.
	binary.LittleEndian.PutUint32(result[12:16], uint32(len(value.Body)))
	binary.LittleEndian.PutUint64(result[16:24], value.SchemaRevision)
	binary.LittleEndian.PutUint64(result[24:32], value.Revision)
	binary.LittleEndian.PutUint64(result[32:40], value.CommitSequence)
	binary.LittleEndian.PutUint16(result[40:42], uint16(len(value.DatabaseID)))
	binary.LittleEndian.PutUint16(result[42:44], uint16(len(value.TableID)))
	binary.LittleEndian.PutUint16(result[44:46], uint16(len(value.RowID)))
	offset := locatorHeaderSize
	for _, component := range []string{value.DatabaseID, value.TableID, value.RowID, value.Body} {
		copy(result[offset:], component)
		offset += len(component)
	}
	return result, nil
}

func decodeLocator(encoded []byte) (Locator, error) {
	if len(encoded) < locatorHeaderSize ||
		!bytes.Equal(encoded[:8], locatorMagic[:]) ||
		binary.LittleEndian.Uint16(encoded[8:10]) != locatorVersion ||
		binary.LittleEndian.Uint16(encoded[46:48]) != 0 {
		return Locator{}, fmt.Errorf("%w: locator header", ErrCorrupt)
	}
	bodyLength := int(binary.LittleEndian.Uint32(encoded[12:16]))
	if bodyLength > maxBodyBytes {
		return Locator{}, fmt.Errorf("%w: locator body size", ErrCorrupt)
	}
	lengths := [3]int{
		int(binary.LittleEndian.Uint16(encoded[40:42])),
		int(binary.LittleEndian.Uint16(encoded[42:44])),
		int(binary.LittleEndian.Uint16(encoded[44:46])),
	}
	total := locatorHeaderSize + bodyLength
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
	// The Row body is opaque bytes — it is an encoded record, not text — so it is
	// taken verbatim rather than validated as UTF-8 like the identity components.
	body := string(encoded[offset : offset+bodyLength])
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
		Body:           body,
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
	// A leaf entry shares a 16 KiB Page with its neighbours, so one encoded
	// record is capped well below a full Page. Refusing by name beats splitting
	// the Row across Pages before overflow Pages exist.
	if len(value.Body) > maxBodyBytes {
		return fmt.Errorf(
			"%w: Row %q revision %d encodes to %d bytes, over the %d byte record limit",
			class, value.RowID, value.Revision, len(value.Body), maxBodyBytes,
		)
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
