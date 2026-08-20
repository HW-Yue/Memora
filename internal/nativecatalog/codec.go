package nativecatalog

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"time"
	"unicode/utf8"
)

const (
	payloadVersion = 1
	maxFieldCount  = 64
)

var (
	ErrCorrupt = errors.New("native catalog record is corrupt")
	ErrInvalid = errors.New("native catalog value is invalid")
)

type fieldType uint8

const (
	fieldText fieldType = iota + 1
	fieldUint64
	fieldInt64
	fieldBool
	fieldTextList
)

type field struct {
	id   uint16
	kind fieldType
	data []byte
}

type fieldSet map[uint16]field

func encodeFields(fields ...field) ([]byte, error) {
	if len(fields) > maxFieldCount {
		return nil, fmt.Errorf("%w: too many fields", ErrInvalid)
	}
	size := 4
	var previous uint16
	for index, item := range fields {
		if item.id == 0 || (index > 0 && item.id <= previous) {
			return nil, fmt.Errorf("%w: field IDs must be strictly increasing", ErrInvalid)
		}
		previous = item.id
		size += 8 + len(item.data)
	}
	encoded := make([]byte, size)
	binary.LittleEndian.PutUint16(encoded[0:2], payloadVersion)
	binary.LittleEndian.PutUint16(encoded[2:4], uint16(len(fields)))
	offset := 4
	for _, item := range fields {
		binary.LittleEndian.PutUint16(encoded[offset:offset+2], item.id)
		encoded[offset+2] = byte(item.kind)
		binary.LittleEndian.PutUint32(encoded[offset+4:offset+8], uint32(len(item.data)))
		copy(encoded[offset+8:], item.data)
		offset += 8 + len(item.data)
	}
	return encoded, nil
}

func decodeFields(encoded []byte) (fieldSet, error) {
	if len(encoded) < 4 || binary.LittleEndian.Uint16(encoded[0:2]) != payloadVersion {
		return nil, fmt.Errorf("%w: invalid payload header", ErrCorrupt)
	}
	count := int(binary.LittleEndian.Uint16(encoded[2:4]))
	if count > maxFieldCount {
		return nil, fmt.Errorf("%w: too many fields", ErrCorrupt)
	}
	result := make(fieldSet, count)
	offset := 4
	var previous uint16
	for index := 0; index < count; index++ {
		if len(encoded)-offset < 8 {
			return nil, fmt.Errorf("%w: truncated field header", ErrCorrupt)
		}
		id := binary.LittleEndian.Uint16(encoded[offset : offset+2])
		kind := fieldType(encoded[offset+2])
		flags := encoded[offset+3]
		length := int(binary.LittleEndian.Uint32(encoded[offset+4 : offset+8]))
		if id == 0 || (index > 0 && id <= previous) || flags != 0 || length > len(encoded)-offset-8 {
			return nil, fmt.Errorf("%w: invalid field header", ErrCorrupt)
		}
		previous = id
		data := append([]byte(nil), encoded[offset+8:offset+8+length]...)
		result[id] = field{id: id, kind: kind, data: data}
		offset += 8 + length
	}
	if offset != len(encoded) {
		return nil, fmt.Errorf("%w: trailing payload bytes", ErrCorrupt)
	}
	return result, nil
}

func text(id uint16, value string) (field, error) {
	if !utf8.ValidString(value) {
		return field{}, fmt.Errorf("%w: field %d is not UTF-8", ErrInvalid, id)
	}
	return field{id: id, kind: fieldText, data: []byte(value)}, nil
}

func uintValue(id uint16, value uint64) field {
	data := make([]byte, 8)
	binary.LittleEndian.PutUint64(data, value)
	return field{id: id, kind: fieldUint64, data: data}
}

func intValue(id uint16, value int64) field {
	data := make([]byte, 8)
	binary.LittleEndian.PutUint64(data, uint64(value))
	return field{id: id, kind: fieldInt64, data: data}
}

func boolValue(id uint16, value bool) field {
	data := byte(0)
	if value {
		data = 1
	}
	return field{id: id, kind: fieldBool, data: []byte{data}}
}

func textList(id uint16, values []string) (field, error) {
	size := 4
	for _, value := range values {
		if !utf8.ValidString(value) || len(value) > math.MaxUint32 {
			return field{}, fmt.Errorf("%w: invalid text list", ErrInvalid)
		}
		size += 4 + len(value)
	}
	data := make([]byte, size)
	binary.LittleEndian.PutUint32(data[0:4], uint32(len(values)))
	offset := 4
	for _, value := range values {
		binary.LittleEndian.PutUint32(data[offset:offset+4], uint32(len(value)))
		copy(data[offset+4:], value)
		offset += 4 + len(value)
	}
	return field{id: id, kind: fieldTextList, data: data}, nil
}

func (fields fieldSet) text(id uint16) (string, error) {
	item, err := fields.required(id, fieldText)
	if err != nil || !utf8.Valid(item.data) {
		return "", corruptField(id, err)
	}
	return string(item.data), nil
}

func (fields fieldSet) optionalText(id uint16) (string, error) {
	if _, ok := fields[id]; !ok {
		return "", nil
	}
	return fields.text(id)
}

func (fields fieldSet) uint64(id uint16) (uint64, error) {
	item, err := fields.required(id, fieldUint64)
	if err != nil || len(item.data) != 8 {
		return 0, corruptField(id, err)
	}
	return binary.LittleEndian.Uint64(item.data), nil
}

func (fields fieldSet) int64(id uint16) (int64, error) {
	item, err := fields.required(id, fieldInt64)
	if err != nil || len(item.data) != 8 {
		return 0, corruptField(id, err)
	}
	return int64(binary.LittleEndian.Uint64(item.data)), nil
}

func (fields fieldSet) bool(id uint16) (bool, error) {
	item, err := fields.required(id, fieldBool)
	if err != nil || len(item.data) != 1 || item.data[0] > 1 {
		return false, corruptField(id, err)
	}
	return item.data[0] == 1, nil
}

func (fields fieldSet) optionalBool(id uint16) (bool, error) {
	if _, ok := fields[id]; !ok {
		return false, nil
	}
	return fields.bool(id)
}

// optionalTimestamp decodes an absent field as nil rather than the zero time,
// so "never archived" and "archived at the Unix epoch" stay distinguishable.
func (fields fieldSet) optionalTimestamp(id uint16) (*time.Time, error) {
	if _, ok := fields[id]; !ok {
		return nil, nil
	}
	nanoseconds, err := fields.int64(id)
	if err != nil {
		return nil, err
	}
	value := time.Unix(0, nanoseconds).UTC()
	return &value, nil
}

func (fields fieldSet) textList(id uint16) ([]string, error) {
	item, err := fields.required(id, fieldTextList)
	if err != nil || len(item.data) < 4 {
		return nil, corruptField(id, err)
	}
	count := int(binary.LittleEndian.Uint32(item.data[0:4]))
	result := make([]string, 0, count)
	offset := 4
	for index := 0; index < count; index++ {
		if len(item.data)-offset < 4 {
			return nil, corruptField(id, nil)
		}
		length := int(binary.LittleEndian.Uint32(item.data[offset : offset+4]))
		if length > len(item.data)-offset-4 {
			return nil, corruptField(id, nil)
		}
		value := item.data[offset+4 : offset+4+length]
		if !utf8.Valid(value) {
			return nil, corruptField(id, nil)
		}
		result = append(result, string(value))
		offset += 4 + length
	}
	if offset != len(item.data) {
		return nil, corruptField(id, nil)
	}
	if len(result) == 0 {
		return nil, nil
	}
	return result, nil
}

func (fields fieldSet) required(id uint16, kind fieldType) (field, error) {
	item, ok := fields[id]
	if !ok || item.kind != kind {
		return field{}, fmt.Errorf("missing or mistyped field")
	}
	return item, nil
}

func corruptField(id uint16, cause error) error {
	if cause == nil {
		return fmt.Errorf("%w: invalid field %d", ErrCorrupt, id)
	}
	return fmt.Errorf("%w: invalid field %d: %v", ErrCorrupt, id, cause)
}
