package wal

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"
)

const (
	segmentHeaderSize = 64
	recordHeaderSize  = 56
	formatVersion     = uint16(1)
	maxPayloadSize    = 16 << 20
)

var (
	ErrClosed             = errors.New("WAL segment is closed")
	ErrCorrupt            = errors.New("WAL segment is corrupt")
	ErrInvalid            = errors.New("WAL value is invalid")
	ErrUnsupportedVersion = errors.New("WAL version is unsupported")
)

var (
	segmentMagic = [8]byte{'M', 'E', 'M', 'W', 'A', 'L', '0', '1'}
	recordMagic  = [4]byte{'M', 'W', 'A', 'L'}
	crcTable     = crc32.MakeTable(crc32.Castagnoli)
)

type Type uint16

const (
	TypePageInit Type = iota + 1
	TypePageDelta
	TypeFullPageImage
	TypeRoot
	TypeAllocator
	TypeCommit
	TypeCheckpoint
)

type Record struct {
	Type          Type
	LSN           uint64
	TransactionID uint64
	SpaceID       uint64
	PageID        uint64
	Payload       []byte
}

type segmentFile interface {
	ReadAt([]byte, int64) (int, error)
	WriteAt([]byte, int64) (int, error)
	Sync() error
	Stat() (os.FileInfo, error)
	Close() error
}

type Segment struct {
	mu         sync.RWMutex
	file       segmentFile
	segmentID  uint64
	startLSN   uint64
	nextLSN    uint64
	durableLSN uint64
	closed     bool
}

func Create(path string, segmentID, startLSN uint64) (*Segment, error) {
	if path == "" || startLSN > math.MaxUint64-segmentHeaderSize {
		return nil, fmt.Errorf("%w: path or start LSN", ErrInvalid)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create WAL segment: %w", err)
	}
	removeOnError := true
	defer func() {
		if removeOnError {
			_ = file.Close()
			_ = os.Remove(path)
		}
	}()

	header := encodeSegmentHeader(segmentID, startLSN)
	if err := writeFullAt(file, header, 0); err != nil {
		return nil, fmt.Errorf("write WAL segment header: %w", err)
	}
	if err := file.Sync(); err != nil {
		return nil, fmt.Errorf("sync WAL segment header: %w", err)
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("sync WAL directory: %w", err)
	}

	firstLSN := startLSN + segmentHeaderSize
	removeOnError = false
	return &Segment{
		file:       file,
		segmentID:  segmentID,
		startLSN:   startLSN,
		nextLSN:    firstLSN,
		durableLSN: firstLSN,
	}, nil
}

func Open(path string, expectedSegmentID, expectedStartLSN uint64) (*Segment, error) {
	if path == "" {
		return nil, fmt.Errorf("%w: path is required", ErrInvalid)
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open WAL segment: %w", err)
	}
	segment, err := openSegment(file, expectedSegmentID, expectedStartLSN)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return segment, nil
}

func openSegment(file segmentFile, expectedSegmentID, expectedStartLSN uint64) (*Segment, error) {
	if expectedStartLSN > math.MaxUint64-segmentHeaderSize {
		return nil, fmt.Errorf("%w: start LSN overflow", ErrInvalid)
	}
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat WAL segment: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() < segmentHeaderSize {
		return nil, fmt.Errorf("%w: invalid segment file", ErrCorrupt)
	}

	header := make([]byte, segmentHeaderSize)
	if err := readFullAt(file, header, 0); err != nil {
		return nil, fmt.Errorf("read WAL segment header: %w", err)
	}
	segmentID, startLSN, err := decodeSegmentHeader(header)
	if err != nil {
		return nil, err
	}
	if segmentID != expectedSegmentID || startLSN != expectedStartLSN {
		return nil, fmt.Errorf("%w: segment identity mismatch", ErrCorrupt)
	}
	records, nextLSN, err := scanRecords(file, startLSN, info.Size())
	_ = records
	if err != nil {
		return nil, err
	}
	return &Segment{
		file:       file,
		segmentID:  segmentID,
		startLSN:   startLSN,
		nextLSN:    nextLSN,
		durableLSN: nextLSN,
	}, nil
}

func (segment *Segment) Append(value Record) (uint64, error) {
	segment.mu.Lock()
	defer segment.mu.Unlock()

	if segment.closed {
		return 0, ErrClosed
	}
	if value.LSN != 0 || !validType(value.Type) || len(value.Payload) > maxPayloadSize {
		return 0, fmt.Errorf("%w: invalid WAL Record", ErrInvalid)
	}
	totalLength := uint64(recordHeaderSize + len(value.Payload))
	if segment.nextLSN > math.MaxUint64-totalLength {
		return 0, fmt.Errorf("%w: LSN overflow", ErrInvalid)
	}
	value.LSN = segment.nextLSN
	encoded := encodeRecord(value)
	fileOffset := value.LSN - segment.startLSN
	if fileOffset > math.MaxInt64 {
		return 0, fmt.Errorf("%w: WAL file offset overflow", ErrInvalid)
	}
	offset := int64(fileOffset)
	if err := writeFullAt(segment.file, encoded, offset); err != nil {
		return 0, fmt.Errorf("append WAL Record: %w", err)
	}
	segment.nextLSN += totalLength
	return value.LSN, nil
}

func (segment *Segment) Scan() ([]Record, error) {
	segment.mu.RLock()
	defer segment.mu.RUnlock()

	if segment.closed {
		return nil, ErrClosed
	}
	fileSize := int64(segment.nextLSN - segment.startLSN)
	records, _, err := scanRecords(segment.file, segment.startLSN, fileSize)
	return records, err
}

func (segment *Segment) Sync() error {
	segment.mu.Lock()
	defer segment.mu.Unlock()

	if segment.closed {
		return ErrClosed
	}
	if err := segment.file.Sync(); err != nil {
		return fmt.Errorf("sync WAL segment: %w", err)
	}
	segment.durableLSN = segment.nextLSN
	return nil
}

func (segment *Segment) NextLSN() (uint64, error) {
	segment.mu.RLock()
	defer segment.mu.RUnlock()
	if segment.closed {
		return 0, ErrClosed
	}
	return segment.nextLSN, nil
}

func (segment *Segment) DurableLSN() (uint64, error) {
	segment.mu.RLock()
	defer segment.mu.RUnlock()
	if segment.closed {
		return 0, ErrClosed
	}
	return segment.durableLSN, nil
}

func (segment *Segment) Close() error {
	segment.mu.Lock()
	defer segment.mu.Unlock()
	if segment.closed {
		return nil
	}
	segment.closed = true
	if err := segment.file.Close(); err != nil {
		return fmt.Errorf("close WAL segment: %w", err)
	}
	return nil
}

func encodeSegmentHeader(segmentID, startLSN uint64) []byte {
	encoded := make([]byte, segmentHeaderSize)
	copy(encoded[0:8], segmentMagic[:])
	binary.LittleEndian.PutUint16(encoded[8:10], formatVersion)
	binary.LittleEndian.PutUint16(encoded[10:12], segmentHeaderSize)
	binary.LittleEndian.PutUint64(encoded[16:24], segmentID)
	binary.LittleEndian.PutUint64(encoded[24:32], startLSN)
	binary.LittleEndian.PutUint32(encoded[60:64], checksum(encoded, 60))
	return encoded
}

func decodeSegmentHeader(encoded []byte) (uint64, uint64, error) {
	if len(encoded) != segmentHeaderSize {
		return 0, 0, fmt.Errorf("%w: segment header length", ErrCorrupt)
	}
	if binary.LittleEndian.Uint32(encoded[60:64]) != checksum(encoded, 60) {
		return 0, 0, fmt.Errorf("%w: segment header checksum", ErrCorrupt)
	}
	if !bytes.Equal(encoded[0:8], segmentMagic[:]) {
		return 0, 0, fmt.Errorf("%w: segment magic", ErrCorrupt)
	}
	if version := binary.LittleEndian.Uint16(encoded[8:10]); version != formatVersion {
		return 0, 0, fmt.Errorf("%w: got %d", ErrUnsupportedVersion, version)
	}
	if binary.LittleEndian.Uint16(encoded[10:12]) != segmentHeaderSize ||
		!allZero(encoded[12:16]) ||
		!allZero(encoded[32:60]) {
		return 0, 0, fmt.Errorf("%w: segment header fields", ErrCorrupt)
	}
	return binary.LittleEndian.Uint64(encoded[16:24]),
		binary.LittleEndian.Uint64(encoded[24:32]), nil
}

func encodeRecord(value Record) []byte {
	encoded := make([]byte, recordHeaderSize+len(value.Payload))
	copy(encoded[0:4], recordMagic[:])
	binary.LittleEndian.PutUint16(encoded[4:6], formatVersion)
	binary.LittleEndian.PutUint16(encoded[6:8], uint16(value.Type))
	binary.LittleEndian.PutUint16(encoded[8:10], recordHeaderSize)
	binary.LittleEndian.PutUint32(encoded[12:16], uint32(len(encoded)))
	binary.LittleEndian.PutUint64(encoded[16:24], value.LSN)
	binary.LittleEndian.PutUint64(encoded[24:32], value.TransactionID)
	binary.LittleEndian.PutUint64(encoded[32:40], value.SpaceID)
	binary.LittleEndian.PutUint64(encoded[40:48], value.PageID)
	binary.LittleEndian.PutUint32(encoded[48:52], uint32(len(value.Payload)))
	copy(encoded[recordHeaderSize:], value.Payload)
	binary.LittleEndian.PutUint32(encoded[52:56], checksum(encoded, 52))
	return encoded
}

func decodeRecord(encoded []byte, expectedLSN uint64) (Record, error) {
	if len(encoded) < recordHeaderSize {
		return Record{}, fmt.Errorf("%w: partial Record header", ErrCorrupt)
	}
	if !bytes.Equal(encoded[0:4], recordMagic[:]) {
		return Record{}, fmt.Errorf("%w: Record magic", ErrCorrupt)
	}
	if version := binary.LittleEndian.Uint16(encoded[4:6]); version != formatVersion {
		return Record{}, fmt.Errorf("%w: got %d", ErrUnsupportedVersion, version)
	}
	recordType := Type(binary.LittleEndian.Uint16(encoded[6:8]))
	totalLength := binary.LittleEndian.Uint32(encoded[12:16])
	payloadLength := binary.LittleEndian.Uint32(encoded[48:52])
	if !validType(recordType) ||
		binary.LittleEndian.Uint16(encoded[8:10]) != recordHeaderSize ||
		binary.LittleEndian.Uint16(encoded[10:12]) != 0 ||
		totalLength != uint32(len(encoded)) ||
		uint64(totalLength) != uint64(recordHeaderSize)+uint64(payloadLength) ||
		payloadLength > maxPayloadSize {
		return Record{}, fmt.Errorf("%w: invalid Record fields", ErrCorrupt)
	}
	if binary.LittleEndian.Uint32(encoded[52:56]) != checksum(encoded, 52) {
		return Record{}, fmt.Errorf("%w: Record checksum", ErrCorrupt)
	}
	lsn := binary.LittleEndian.Uint64(encoded[16:24])
	if lsn != expectedLSN {
		return Record{}, fmt.Errorf("%w: Record LSN %d, want %d", ErrCorrupt, lsn, expectedLSN)
	}
	return Record{
		Type:          recordType,
		LSN:           lsn,
		TransactionID: binary.LittleEndian.Uint64(encoded[24:32]),
		SpaceID:       binary.LittleEndian.Uint64(encoded[32:40]),
		PageID:        binary.LittleEndian.Uint64(encoded[40:48]),
		Payload:       bytes.Clone(encoded[recordHeaderSize:]),
	}, nil
}

func scanRecords(file segmentFile, startLSN uint64, fileSize int64) ([]Record, uint64, error) {
	offset := int64(segmentHeaderSize)
	nextLSN := startLSN + segmentHeaderSize
	records := make([]Record, 0)
	for offset < fileSize {
		if fileSize-offset < recordHeaderSize {
			return records, nextLSN, fmt.Errorf("%w: partial Record header", ErrCorrupt)
		}
		header := make([]byte, recordHeaderSize)
		if err := readFullAt(file, header, offset); err != nil {
			return records, nextLSN, err
		}
		totalLength := binary.LittleEndian.Uint32(header[12:16])
		if totalLength < recordHeaderSize || totalLength > recordHeaderSize+maxPayloadSize ||
			int64(totalLength) > fileSize-offset {
			return records, nextLSN, fmt.Errorf("%w: invalid or partial Record length", ErrCorrupt)
		}
		encoded := make([]byte, totalLength)
		copy(encoded, header)
		if totalLength > recordHeaderSize {
			if err := readFullAt(file, encoded[recordHeaderSize:], offset+recordHeaderSize); err != nil {
				return records, nextLSN, err
			}
		}
		value, err := decodeRecord(encoded, nextLSN)
		if err != nil {
			return records, nextLSN, err
		}
		records = append(records, value)
		offset += int64(totalLength)
		nextLSN += uint64(totalLength)
	}
	return records, nextLSN, nil
}

func checksum(encoded []byte, offset int) uint32 {
	digest := crc32.New(crcTable)
	_, _ = digest.Write(encoded[:offset])
	_, _ = digest.Write([]byte{0, 0, 0, 0})
	_, _ = digest.Write(encoded[offset+4:])
	return digest.Sum32()
}

func validType(value Type) bool {
	return value >= TypePageInit && value <= TypeCheckpoint
}

func allZero(value []byte) bool {
	for _, item := range value {
		if item != 0 {
			return false
		}
	}
	return true
}

func readFullAt(file segmentFile, target []byte, offset int64) error {
	read, err := file.ReadAt(target, offset)
	if read != len(target) {
		return fmt.Errorf("%w: short read %d/%d", ErrCorrupt, read, len(target))
	}
	return err
}

func writeFullAt(file segmentFile, value []byte, offset int64) error {
	written, err := file.WriteAt(value, offset)
	if written != len(value) {
		if err != nil {
			return errors.Join(io.ErrShortWrite, err)
		}
		return io.ErrShortWrite
	}
	return err
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return directory.Sync()
}
