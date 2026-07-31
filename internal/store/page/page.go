package page

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
)

const (
	Size       = 16 * 1024
	HeaderSize = 64

	formatVersion  = uint16(1)
	checksumOffset = 56
	maxPayloadSize = Size - HeaderSize
)

var (
	ErrCorrupt            = errors.New("page bytes are corrupt")
	ErrInvalid            = errors.New("page value is invalid")
	ErrUnsupportedVersion = errors.New("page format version is unsupported")
)

var (
	pageMagic     = [8]byte{'M', 'E', 'M', 'O', 'P', 'G', '0', '1'}
	checksumTable = crc32.MakeTable(crc32.Castagnoli)
)

type Type uint16

const (
	TypeData Type = iota + 1
	TypeBTreeInternal
	TypeBTreeLeaf
	TypeFree
	TypeManifest
	TypeOverflow
	TypeTreeControl
)

type Header struct {
	Type       Type
	Flags      uint16
	SpaceID    uint64
	PageID     uint64
	Generation uint64
	LSN        uint64
}

type Page struct {
	Header  Header
	Payload []byte
}

func Encode(value Page) ([]byte, error) {
	if !validType(value.Header.Type) {
		return nil, fmt.Errorf("%w: unknown page type %d", ErrInvalid, value.Header.Type)
	}
	if value.Header.Flags != 0 {
		return nil, fmt.Errorf("%w: unsupported flags %#x", ErrInvalid, value.Header.Flags)
	}
	if len(value.Payload) > maxPayloadSize {
		return nil, fmt.Errorf(
			"%w: payload length %d exceeds %d",
			ErrInvalid,
			len(value.Payload),
			maxPayloadSize,
		)
	}

	encoded := make([]byte, Size)
	copy(encoded[0:8], pageMagic[:])
	binary.LittleEndian.PutUint16(encoded[8:10], formatVersion)
	binary.LittleEndian.PutUint16(encoded[10:12], HeaderSize)
	binary.LittleEndian.PutUint16(encoded[12:14], uint16(value.Header.Type))
	binary.LittleEndian.PutUint16(encoded[14:16], value.Header.Flags)
	binary.LittleEndian.PutUint64(encoded[16:24], value.Header.SpaceID)
	binary.LittleEndian.PutUint64(encoded[24:32], value.Header.PageID)
	binary.LittleEndian.PutUint64(encoded[32:40], value.Header.Generation)
	binary.LittleEndian.PutUint64(encoded[40:48], value.Header.LSN)
	binary.LittleEndian.PutUint32(encoded[48:52], uint32(len(value.Payload)))
	copy(encoded[HeaderSize:], value.Payload)
	rewriteChecksum(encoded)
	return encoded, nil
}

func Decode(encoded []byte) (Page, error) {
	if len(encoded) != Size {
		return Page{}, fmt.Errorf("%w: page length %d, want %d", ErrCorrupt, len(encoded), Size)
	}
	wantChecksum := binary.LittleEndian.Uint32(encoded[checksumOffset : checksumOffset+4])
	if gotChecksum := calculateChecksum(encoded); gotChecksum != wantChecksum {
		return Page{}, fmt.Errorf(
			"%w: checksum %#x, want %#x",
			ErrCorrupt,
			gotChecksum,
			wantChecksum,
		)
	}
	if !bytes.Equal(encoded[0:8], pageMagic[:]) {
		return Page{}, fmt.Errorf("%w: invalid magic", ErrCorrupt)
	}
	version := binary.LittleEndian.Uint16(encoded[8:10])
	if version != formatVersion {
		return Page{}, fmt.Errorf("%w: got %d, want %d", ErrUnsupportedVersion, version, formatVersion)
	}
	if binary.LittleEndian.Uint16(encoded[10:12]) != HeaderSize {
		return Page{}, fmt.Errorf("%w: invalid header size", ErrCorrupt)
	}

	pageType := Type(binary.LittleEndian.Uint16(encoded[12:14]))
	flags := binary.LittleEndian.Uint16(encoded[14:16])
	payloadLength := binary.LittleEndian.Uint32(encoded[48:52])
	if !validType(pageType) {
		return Page{}, fmt.Errorf("%w: unknown page type %d", ErrCorrupt, pageType)
	}
	if flags != 0 {
		return Page{}, fmt.Errorf("%w: unsupported flags %#x", ErrCorrupt, flags)
	}
	if binary.LittleEndian.Uint32(encoded[52:56]) != 0 ||
		binary.LittleEndian.Uint32(encoded[60:64]) != 0 {
		return Page{}, fmt.Errorf("%w: reserved header bytes are nonzero", ErrCorrupt)
	}
	if payloadLength > maxPayloadSize {
		return Page{}, fmt.Errorf("%w: payload length %d exceeds Page", ErrCorrupt, payloadLength)
	}

	payloadEnd := HeaderSize + int(payloadLength)
	return Page{
		Header: Header{
			Type:       pageType,
			Flags:      flags,
			SpaceID:    binary.LittleEndian.Uint64(encoded[16:24]),
			PageID:     binary.LittleEndian.Uint64(encoded[24:32]),
			Generation: binary.LittleEndian.Uint64(encoded[32:40]),
			LSN:        binary.LittleEndian.Uint64(encoded[40:48]),
		},
		Payload: bytes.Clone(encoded[HeaderSize:payloadEnd]),
	}, nil
}

func validType(value Type) bool {
	return value >= TypeData && value <= TypeTreeControl
}

func calculateChecksum(encoded []byte) uint32 {
	digest := crc32.New(checksumTable)
	_, _ = digest.Write(encoded[:checksumOffset])
	_, _ = digest.Write([]byte{0, 0, 0, 0})
	_, _ = digest.Write(encoded[checksumOffset+4:])
	return digest.Sum32()
}

func rewriteChecksum(encoded []byte) {
	binary.LittleEndian.PutUint32(encoded[checksumOffset:checksumOffset+4], 0)
	binary.LittleEndian.PutUint32(
		encoded[checksumOffset:checksumOffset+4],
		calculateChecksum(encoded),
	)
}
