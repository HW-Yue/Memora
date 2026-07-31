package btree

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/HW-Yue/Memora/internal/store/page"
)

const (
	nodeHeaderSize  = 48
	nodeSlotSize    = 8
	nodeVersion     = uint16(1)
	nodePayloadSize = page.Size - page.HeaderSize
)

var (
	ErrInvalid = errors.New("B+ Tree Node is invalid")
	ErrCorrupt = errors.New("B+ Tree Node payload is corrupt")
	ErrNoSpace = errors.New("B+ Tree Node does not fit in Page")
	nodeMagic  = [8]byte{'M', 'E', 'M', 'B', 'T', 'N', '0', '1'}
)

type Kind uint16

const (
	KindLeaf Kind = iota + 1
	KindInternal
)

type LeafEntry struct {
	Key   []byte
	Value []byte
}

type InternalEntry struct {
	Key        []byte
	RightChild uint64
}

type Node struct {
	Kind            Kind
	Level           uint16
	NextLeafPageID  uint64
	LeftmostChild   uint64
	LeafEntries     []LeafEntry
	InternalEntries []InternalEntry
}

func Encode(header page.Header, node Node) (page.Page, error) {
	if err := validateNode(header, node, ErrInvalid); err != nil {
		return page.Page{}, err
	}
	count := len(node.LeafEntries)
	if node.Kind == KindInternal {
		count = len(node.InternalEntries)
	}
	recordBytes := 0
	for index := 0; index < count; index++ {
		keyLength, recordLength := entryLengths(node, index)
		if keyLength > nodePayloadSize || recordLength > nodePayloadSize {
			return page.Page{}, fmt.Errorf("%w: entry %d", ErrNoSpace, index)
		}
		recordBytes += recordLength
	}
	freeStart := nodeHeaderSize + count*nodeSlotSize
	if freeStart > nodePayloadSize || recordBytes > nodePayloadSize-freeStart {
		return page.Page{}, fmt.Errorf("%w: %d entries need %d record bytes", ErrNoSpace, count, recordBytes)
	}

	payload := make([]byte, nodePayloadSize)
	copy(payload[:8], nodeMagic[:])
	binary.LittleEndian.PutUint16(payload[8:10], nodeVersion)
	binary.LittleEndian.PutUint16(payload[10:12], uint16(node.Kind))
	binary.LittleEndian.PutUint16(payload[12:14], node.Level)
	binary.LittleEndian.PutUint32(payload[16:20], uint32(count))
	binary.LittleEndian.PutUint16(payload[20:22], nodeSlotSize)
	binary.LittleEndian.PutUint16(payload[22:24], nodeHeaderSize)
	binary.LittleEndian.PutUint64(payload[24:32], node.LeftmostChild)
	binary.LittleEndian.PutUint64(payload[32:40], node.NextLeafPageID)
	binary.LittleEndian.PutUint16(payload[40:42], uint16(freeStart))

	cursor := nodePayloadSize
	for index := 0; index < count; index++ {
		key, record := encodedEntry(node, index)
		cursor -= len(record)
		copy(payload[cursor:], record)
		slot := nodeHeaderSize + index*nodeSlotSize
		binary.LittleEndian.PutUint16(payload[slot:slot+2], uint16(cursor))
		binary.LittleEndian.PutUint16(payload[slot+2:slot+4], uint16(len(record)))
		binary.LittleEndian.PutUint16(payload[slot+4:slot+6], uint16(len(key)))
	}
	binary.LittleEndian.PutUint16(payload[42:44], uint16(cursor))
	return page.Page{Header: header, Payload: payload}, nil
}

func Decode(value page.Page) (Node, error) {
	payload := value.Payload
	if len(payload) != nodePayloadSize || !bytes.Equal(payload[:8], nodeMagic[:]) ||
		binary.LittleEndian.Uint16(payload[8:10]) != nodeVersion ||
		binary.LittleEndian.Uint16(payload[20:22]) != nodeSlotSize ||
		binary.LittleEndian.Uint16(payload[22:24]) != nodeHeaderSize ||
		!allZero(payload[14:16]) || !allZero(payload[44:48]) {
		return Node{}, fmt.Errorf("%w: header", ErrCorrupt)
	}
	kind := Kind(binary.LittleEndian.Uint16(payload[10:12]))
	count := uint64(binary.LittleEndian.Uint32(payload[16:20]))
	if count > uint64((nodePayloadSize-nodeHeaderSize)/nodeSlotSize) {
		return Node{}, fmt.Errorf("%w: entry count", ErrCorrupt)
	}
	freeStart := uint64(binary.LittleEndian.Uint16(payload[40:42]))
	freeEnd := uint64(binary.LittleEndian.Uint16(payload[42:44]))
	wantFreeStart := uint64(nodeHeaderSize) + count*nodeSlotSize
	if freeStart != wantFreeStart || freeEnd < freeStart || freeEnd > nodePayloadSize ||
		!allZero(payload[freeStart:freeEnd]) {
		return Node{}, fmt.Errorf("%w: free space", ErrCorrupt)
	}
	node := Node{
		Kind:           kind,
		Level:          binary.LittleEndian.Uint16(payload[12:14]),
		LeftmostChild:  binary.LittleEndian.Uint64(payload[24:32]),
		NextLeafPageID: binary.LittleEndian.Uint64(payload[32:40]),
	}
	expectedEnd := uint64(nodePayloadSize)
	var previousKey []byte
	for index := uint64(0); index < count; index++ {
		slot := uint64(nodeHeaderSize) + index*nodeSlotSize
		offset := uint64(binary.LittleEndian.Uint16(payload[slot : slot+2]))
		length := uint64(binary.LittleEndian.Uint16(payload[slot+2 : slot+4]))
		keyLength := uint64(binary.LittleEndian.Uint16(payload[slot+4 : slot+6]))
		if !allZero(payload[slot+6:slot+8]) || length == 0 || keyLength == 0 ||
			offset < freeEnd || offset+length != expectedEnd || keyLength > length {
			return Node{}, fmt.Errorf("%w: slot %d", ErrCorrupt, index)
		}
		record := payload[offset : offset+length]
		key := bytes.Clone(record[:keyLength])
		if previousKey != nil && bytes.Compare(previousKey, key) >= 0 {
			return Node{}, fmt.Errorf("%w: key order", ErrCorrupt)
		}
		previousKey = key
		switch kind {
		case KindLeaf:
			if keyLength == length {
				return Node{}, fmt.Errorf("%w: empty leaf value", ErrCorrupt)
			}
			node.LeafEntries = append(node.LeafEntries, LeafEntry{Key: key, Value: bytes.Clone(record[keyLength:])})
		case KindInternal:
			if length != keyLength+8 {
				return Node{}, fmt.Errorf("%w: internal record", ErrCorrupt)
			}
			child := binary.LittleEndian.Uint64(record[keyLength:])
			if child == 0 {
				return Node{}, fmt.Errorf("%w: zero child", ErrCorrupt)
			}
			node.InternalEntries = append(node.InternalEntries, InternalEntry{Key: key, RightChild: child})
		default:
			return Node{}, fmt.Errorf("%w: kind", ErrCorrupt)
		}
		expectedEnd = offset
	}
	if expectedEnd != freeEnd {
		return Node{}, fmt.Errorf("%w: record packing", ErrCorrupt)
	}
	if err := validateNode(value.Header, node, ErrCorrupt); err != nil {
		return Node{}, err
	}
	return node, nil
}

func validateNode(header page.Header, node Node, class error) error {
	if header.PageID == 0 || header.Flags != 0 {
		return fmt.Errorf("%w: Page header", class)
	}
	switch node.Kind {
	case KindLeaf:
		if header.Type != page.TypeBTreeLeaf || node.Level != 0 || node.LeftmostChild != 0 || len(node.InternalEntries) != 0 {
			return fmt.Errorf("%w: leaf shape", class)
		}
		previous := []byte(nil)
		for _, entry := range node.LeafEntries {
			if len(entry.Key) == 0 || len(entry.Value) == 0 || (previous != nil && bytes.Compare(previous, entry.Key) >= 0) {
				return fmt.Errorf("%w: leaf entry", class)
			}
			previous = entry.Key
		}
	case KindInternal:
		if header.Type != page.TypeBTreeInternal || node.Level == 0 || node.LeftmostChild == 0 ||
			node.NextLeafPageID != 0 || len(node.LeafEntries) != 0 {
			return fmt.Errorf("%w: internal shape", class)
		}
		previous := []byte(nil)
		for _, entry := range node.InternalEntries {
			if len(entry.Key) == 0 || entry.RightChild == 0 || (previous != nil && bytes.Compare(previous, entry.Key) >= 0) {
				return fmt.Errorf("%w: internal entry", class)
			}
			previous = entry.Key
		}
	default:
		return fmt.Errorf("%w: kind", class)
	}
	return nil
}

func entryLengths(node Node, index int) (int, int) {
	if node.Kind == KindLeaf {
		entry := node.LeafEntries[index]
		return len(entry.Key), len(entry.Key) + len(entry.Value)
	}
	return len(node.InternalEntries[index].Key), len(node.InternalEntries[index].Key) + 8
}

func encodedEntry(node Node, index int) ([]byte, []byte) {
	if node.Kind == KindLeaf {
		entry := node.LeafEntries[index]
		return entry.Key, append(append(make([]byte, 0, len(entry.Key)+len(entry.Value)), entry.Key...), entry.Value...)
	}
	entry := node.InternalEntries[index]
	record := make([]byte, len(entry.Key)+8)
	copy(record, entry.Key)
	binary.LittleEndian.PutUint64(record[len(entry.Key):], entry.RightChild)
	return entry.Key, record
}

func allZero(value []byte) bool {
	for _, current := range value {
		if current != 0 {
			return false
		}
	}
	return true
}
