package catalogindex

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	keyVersion        = byte(1)
	locatorVersion    = uint16(1)
	locatorHeaderSize = 32
	maxComponentBytes = 2048
	maxKeyBytes       = 6144
)

var (
	ErrInvalid  = errors.New("Catalog Lookup Index input is invalid")
	ErrNotFound = errors.New("Catalog Lookup Index key was not found")
	ErrConflict = errors.New("Catalog Lookup Index key conflicts")
	ErrCorrupt  = errors.New("Catalog Lookup Index is corrupt")

	locatorMagic = [8]byte{'M', 'E', 'M', 'C', 'L', 'O', '0', '1'}
)

type Kind uint16

const (
	KindDatabase Kind = iota + 1
	KindTable
	KindColumn
)

type Locator struct {
	Kind           Kind
	ID             string
	DatabaseID     string
	TableID        string
	SchemaRevision uint64
}

type keyKind byte

const (
	keyDatabaseID keyKind = iota + 1
	keyDatabaseName
	keyTableID
	keyTableName
	keyColumnID
	keyColumnName
)

func identityKey(kind keyKind, id string) ([]byte, error) {
	return encodeKey(kind, false, id)
}

func nameKey(kind keyKind, scope, name string) ([]byte, error) {
	canonical := strings.ToLower(strings.TrimSpace(name))
	if scope == "" {
		return encodeKey(kind, true, canonical)
	}
	return encodeKey(kind, false, scope, canonical)
}

func scopedNamePrefix(kind keyKind, scope string) ([]byte, error) {
	if (kind != keyTableName && kind != keyColumnName) ||
		scope == "" || !utf8.ValidString(scope) ||
		strings.TrimSpace(scope) != scope || len(scope) > maxComponentBytes {
		return nil, fmt.Errorf("%w: scoped name prefix", ErrInvalid)
	}
	result := make([]byte, 4+len(scope))
	result[0], result[1] = keyVersion, byte(kind)
	binary.BigEndian.PutUint16(result[2:4], uint16(len(scope)))
	copy(result[4:], scope)
	return result, nil
}

func keyPrefixSuccessor(prefix []byte) ([]byte, error) {
	result := bytes.Clone(prefix)
	for index := len(result) - 1; index >= 0; index-- {
		if result[index] != 0xff {
			result[index]++
			return result[:index+1], nil
		}
	}
	return nil, fmt.Errorf("%w: key prefix has no successor", ErrInvalid)
}

func encodeKey(kind keyKind, canonical bool, components ...string) ([]byte, error) {
	if kind < keyDatabaseID || kind > keyColumnName || len(components) == 0 {
		return nil, fmt.Errorf("%w: key shape", ErrInvalid)
	}
	size := 2
	for _, component := range components {
		if canonical {
			component = strings.ToLower(strings.TrimSpace(component))
		}
		if component == "" || !utf8.ValidString(component) ||
			(!canonical && strings.TrimSpace(component) != component) ||
			len(component) > maxComponentBytes {
			return nil, fmt.Errorf("%w: key component", ErrInvalid)
		}
		size += 2 + len(component)
	}
	if size > maxKeyBytes {
		return nil, fmt.Errorf("%w: key size", ErrInvalid)
	}
	result := make([]byte, size)
	result[0] = keyVersion
	result[1] = byte(kind)
	offset := 2
	for _, component := range components {
		if canonical {
			component = strings.ToLower(strings.TrimSpace(component))
		}
		binary.BigEndian.PutUint16(result[offset:offset+2], uint16(len(component)))
		offset += 2
		copy(result[offset:], component)
		offset += len(component)
	}
	return result, nil
}

func validateStoredKey(encoded []byte) error {
	if len(encoded) < 4 || len(encoded) > maxKeyBytes || encoded[0] != keyVersion {
		return fmt.Errorf("%w: key header", ErrCorrupt)
	}
	kind := keyKind(encoded[1])
	count := 1
	switch kind {
	case keyDatabaseID, keyDatabaseName, keyTableID, keyColumnID:
	case keyTableName, keyColumnName:
		count = 2
	default:
		return fmt.Errorf("%w: key kind", ErrCorrupt)
	}
	offset := 2
	components := make([]string, 0, count)
	for index := 0; index < count; index++ {
		if offset+2 > len(encoded) {
			return fmt.Errorf("%w: key component length", ErrCorrupt)
		}
		length := int(binary.BigEndian.Uint16(encoded[offset : offset+2]))
		offset += 2
		if length == 0 || length > maxComponentBytes || offset+length > len(encoded) {
			return fmt.Errorf("%w: key component size", ErrCorrupt)
		}
		component := encoded[offset : offset+length]
		if !utf8.Valid(component) {
			return fmt.Errorf("%w: key UTF-8", ErrCorrupt)
		}
		components = append(components, string(component))
		offset += length
	}
	if offset != len(encoded) {
		return fmt.Errorf("%w: key trailing bytes", ErrCorrupt)
	}
	for index, component := range components {
		isName := (kind == keyDatabaseName && index == 0) ||
			((kind == keyTableName || kind == keyColumnName) && index == 1)
		if isName {
			if component != strings.ToLower(strings.TrimSpace(component)) {
				return fmt.Errorf("%w: non-canonical name key", ErrCorrupt)
			}
		} else if component != strings.TrimSpace(component) {
			return fmt.Errorf("%w: non-canonical identity key", ErrCorrupt)
		}
	}
	return nil
}

func encodeLocator(value Locator) ([]byte, error) {
	if err := validateLocator(value, ErrInvalid); err != nil {
		return nil, err
	}
	size := locatorHeaderSize + len(value.ID) + len(value.DatabaseID) + len(value.TableID)
	result := make([]byte, size)
	copy(result[:8], locatorMagic[:])
	binary.LittleEndian.PutUint16(result[8:10], locatorVersion)
	binary.LittleEndian.PutUint16(result[10:12], uint16(value.Kind))
	binary.LittleEndian.PutUint64(result[16:24], value.SchemaRevision)
	binary.LittleEndian.PutUint16(result[24:26], uint16(len(value.ID)))
	binary.LittleEndian.PutUint16(result[26:28], uint16(len(value.DatabaseID)))
	binary.LittleEndian.PutUint16(result[28:30], uint16(len(value.TableID)))
	offset := locatorHeaderSize
	for _, component := range []string{value.ID, value.DatabaseID, value.TableID} {
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
		binary.LittleEndian.Uint16(encoded[30:32]) != 0 {
		return Locator{}, fmt.Errorf("%w: locator header", ErrCorrupt)
	}
	lengths := [3]int{
		int(binary.LittleEndian.Uint16(encoded[24:26])),
		int(binary.LittleEndian.Uint16(encoded[26:28])),
		int(binary.LittleEndian.Uint16(encoded[28:30])),
	}
	total := locatorHeaderSize
	for _, length := range lengths {
		if length > maxComponentBytes {
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
	result := Locator{
		Kind:           Kind(binary.LittleEndian.Uint16(encoded[10:12])),
		ID:             components[0],
		DatabaseID:     components[1],
		TableID:        components[2],
		SchemaRevision: binary.LittleEndian.Uint64(encoded[16:24]),
	}
	if err := validateLocator(result, ErrCorrupt); err != nil {
		return Locator{}, err
	}
	return result, nil
}

func validateLocator(value Locator, class error) error {
	validComponent := func(component string, required bool) bool {
		if component == "" {
			return !required
		}
		return len(component) <= maxComponentBytes && utf8.ValidString(component) &&
			strings.TrimSpace(component) == component
	}
	if value.SchemaRevision == 0 || !validComponent(value.ID, true) {
		return fmt.Errorf("%w: locator identity or revision", class)
	}
	switch value.Kind {
	case KindDatabase:
		if value.DatabaseID != "" || value.TableID != "" {
			return fmt.Errorf("%w: Database locator hierarchy", class)
		}
	case KindTable:
		if !validComponent(value.DatabaseID, true) || value.TableID != "" {
			return fmt.Errorf("%w: Table locator hierarchy", class)
		}
	case KindColumn:
		if !validComponent(value.DatabaseID, true) || !validComponent(value.TableID, true) {
			return fmt.Errorf("%w: Column locator hierarchy", class)
		}
	default:
		return fmt.Errorf("%w: locator kind", class)
	}
	return nil
}
