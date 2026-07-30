package native

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"sort"
	"unicode/utf8"
)

const (
	fileHeaderSize   = 32
	recordHeaderSize = 24
	formatVersion    = 0
	maxIDLength      = 4 << 10
	maxPayloadLength = 16 << 20
)

var (
	ErrClosed             = errors.New("native store file is closed")
	ErrCorrupt            = errors.New("native store file is corrupt")
	ErrDuplicateID        = errors.New("native store record ID already exists")
	ErrInvalidArgument    = errors.New("native store argument is invalid")
	ErrNotFound           = errors.New("native store record not found")
	ErrUnsupportedVersion = errors.New("native store format version is unsupported")
)

var fileMagic = [8]byte{'M', 'E', 'M', 'O', 'R', 'A', 0, 0}

type FileKind uint16

const (
	FileKindSystem   FileKind = 1
	FileKindDatabase FileKind = 2
)

type ObjectKind uint16

const (
	ObjectKindOpaque   ObjectKind = 1
	ObjectKindDatabase ObjectKind = 2
	ObjectKindTable    ObjectKind = 3
	ObjectKindColumn   ObjectKind = 4
	ObjectKindRow      ObjectKind = 5
	ObjectKindHistory  ObjectKind = 6
)

type File struct {
	file    *os.File
	kind    FileKind
	records map[recordKey]recordMeta
	closed  bool
}

type recordKey struct {
	kind ObjectKind
	id   string
}

type recordMeta struct {
	payloadOffset int64
	payloadLength uint32
	payloadCRC    uint32
}

type fileHeader struct {
	kind FileKind
	uuid [16]byte
}

type recordHeader struct {
	recordLength  uint32
	kind          ObjectKind
	schema        uint32
	idLength      uint32
	payloadLength uint32
	payloadCRC    uint32
}

func Create(path string, kind FileKind) (*File, error) {
	if path == "" || !validFileKind(kind) {
		return nil, fmt.Errorf("%w: path and file kind are required", ErrInvalidArgument)
	}

	var uuid [16]byte
	if _, err := rand.Read(uuid[:]); err != nil {
		return nil, fmt.Errorf("generate file UUID: %w", err)
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create native store file: %w", err)
	}
	removeOnError := true
	defer func() {
		if removeOnError {
			_ = file.Close()
			_ = os.Remove(path)
		}
	}()

	encoded := encodeFileHeader(fileHeader{kind: kind, uuid: uuid})
	if err := writeFull(file, encoded[:]); err != nil {
		return nil, fmt.Errorf("write native store header: %w", err)
	}

	removeOnError = false
	return &File{file: file, kind: kind, records: make(map[recordKey]recordMeta)}, nil
}

func Open(path string) (*File, error) {
	if path == "" {
		return nil, fmt.Errorf("%w: path is required", ErrInvalidArgument)
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open native store file: %w", err)
	}

	result, err := openFile(file)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return result, nil
}

func openFile(file *os.File) (*File, error) {
	stat, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat native store file: %w", err)
	}
	if stat.Size() < fileHeaderSize {
		return nil, fmt.Errorf("%w: file is shorter than header", ErrCorrupt)
	}

	var encodedHeader [fileHeaderSize]byte
	if _, err := file.ReadAt(encodedHeader[:], 0); err != nil {
		return nil, fmt.Errorf("read native store header: %w", err)
	}
	header, err := decodeFileHeader(encodedHeader[:])
	if err != nil {
		return nil, err
	}

	result := &File{file: file, kind: header.kind, records: make(map[recordKey]recordMeta)}
	if err := result.scan(stat.Size()); err != nil {
		return nil, err
	}
	return result, nil
}

func (f *File) Put(kind ObjectKind, schemaVersion uint32, id string, payload []byte) error {
	if f == nil || f.closed {
		return ErrClosed
	}
	if err := validateRecord(kind, schemaVersion, id, len(payload)); err != nil {
		return err
	}
	key := recordKey{kind: kind, id: id}
	if _, exists := f.records[key]; exists {
		return ErrDuplicateID
	}

	encodedHeader := encodeRecordHeader(recordHeader{
		recordLength:  uint32(recordHeaderSize + len(id) + len(payload)),
		kind:          kind,
		schema:        schemaVersion,
		idLength:      uint32(len(id)),
		payloadLength: uint32(len(payload)),
		payloadCRC:    crc32.ChecksumIEEE(payload),
	})
	offset, err := f.file.Seek(0, io.SeekEnd)
	if err != nil {
		return fmt.Errorf("seek native store file: %w", err)
	}
	if err := writeFull(f.file, encodedHeader[:]); err != nil {
		return fmt.Errorf("write native record header: %w", err)
	}
	if err := writeFull(f.file, []byte(id)); err != nil {
		return fmt.Errorf("write native record ID: %w", err)
	}
	if err := writeFull(f.file, payload); err != nil {
		return fmt.Errorf("write native record payload: %w", err)
	}
	f.records[key] = recordMeta{
		payloadOffset: offset + recordHeaderSize + int64(len(id)),
		payloadLength: uint32(len(payload)),
		payloadCRC:    crc32.ChecksumIEEE(payload),
	}
	return nil
}

func (f *File) Get(kind ObjectKind, id string) ([]byte, error) {
	if f == nil || f.closed {
		return nil, ErrClosed
	}
	meta, ok := f.records[recordKey{kind: kind, id: id}]
	if !ok {
		return nil, ErrNotFound
	}
	payload := make([]byte, meta.payloadLength)
	if _, err := f.file.ReadAt(payload, meta.payloadOffset); err != nil {
		return nil, fmt.Errorf("read native record payload: %w", err)
	}
	if crc32.ChecksumIEEE(payload) != meta.payloadCRC {
		return nil, fmt.Errorf("%w: payload CRC mismatch", ErrCorrupt)
	}
	return payload, nil
}

func (f *File) IDs(kind ObjectKind) ([]string, error) {
	if f == nil || f.closed {
		return nil, ErrClosed
	}
	ids := make([]string, 0)
	for key := range f.records {
		if key.kind == kind {
			ids = append(ids, key.id)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func (f *File) Close() error {
	if f == nil || f.closed {
		return ErrClosed
	}
	f.closed = true
	return f.file.Close()
}

func (f *File) scan(fileSize int64) error {
	for offset := int64(fileHeaderSize); offset < fileSize; {
		if fileSize-offset < recordHeaderSize {
			return fmt.Errorf("%w: incomplete record header at offset %d", ErrCorrupt, offset)
		}
		var encoded [recordHeaderSize]byte
		if _, err := f.file.ReadAt(encoded[:], offset); err != nil {
			return fmt.Errorf("read native record header at offset %d: %w", offset, err)
		}
		header, err := decodeRecordHeader(encoded[:])
		if err != nil {
			return fmt.Errorf("record at offset %d: %w", offset, err)
		}
		if int64(header.recordLength) > fileSize-offset {
			return fmt.Errorf("%w: incomplete record at offset %d", ErrCorrupt, offset)
		}

		id := make([]byte, header.idLength)
		if _, err := f.file.ReadAt(id, offset+recordHeaderSize); err != nil {
			return fmt.Errorf("read native record ID at offset %d: %w", offset, err)
		}
		if !utf8.Valid(id) || len(id) == 0 {
			return fmt.Errorf("%w: invalid record ID at offset %d", ErrCorrupt, offset)
		}
		key := recordKey{kind: header.kind, id: string(id)}
		if _, exists := f.records[key]; exists {
			return fmt.Errorf("%w: duplicate record ID %q", ErrCorrupt, string(id))
		}

		payloadOffset := offset + recordHeaderSize + int64(header.idLength)
		payload := make([]byte, header.payloadLength)
		if _, err := f.file.ReadAt(payload, payloadOffset); err != nil {
			return fmt.Errorf("read native record payload at offset %d: %w", offset, err)
		}
		if crc32.ChecksumIEEE(payload) != header.payloadCRC {
			return fmt.Errorf("%w: payload CRC mismatch at offset %d", ErrCorrupt, offset)
		}
		f.records[key] = recordMeta{
			payloadOffset: payloadOffset,
			payloadLength: header.payloadLength,
			payloadCRC:    header.payloadCRC,
		}
		offset += int64(header.recordLength)
	}
	return nil
}

func encodeFileHeader(header fileHeader) [fileHeaderSize]byte {
	var encoded [fileHeaderSize]byte
	copy(encoded[0:8], fileMagic[:])
	binary.LittleEndian.PutUint16(encoded[8:10], formatVersion)
	binary.LittleEndian.PutUint16(encoded[10:12], fileHeaderSize)
	binary.LittleEndian.PutUint16(encoded[12:14], uint16(header.kind))
	copy(encoded[16:32], header.uuid[:])
	return encoded
}

func decodeFileHeader(encoded []byte) (fileHeader, error) {
	if len(encoded) != fileHeaderSize {
		return fileHeader{}, fmt.Errorf("%w: invalid file header length", ErrCorrupt)
	}
	if string(encoded[0:8]) != string(fileMagic[:]) {
		return fileHeader{}, fmt.Errorf("%w: invalid file magic", ErrCorrupt)
	}
	version := binary.LittleEndian.Uint16(encoded[8:10])
	if version != formatVersion {
		return fileHeader{}, fmt.Errorf("%w: got %d", ErrUnsupportedVersion, version)
	}
	if binary.LittleEndian.Uint16(encoded[10:12]) != fileHeaderSize {
		return fileHeader{}, fmt.Errorf("%w: invalid file header size", ErrCorrupt)
	}
	kind := FileKind(binary.LittleEndian.Uint16(encoded[12:14]))
	if !validFileKind(kind) || binary.LittleEndian.Uint16(encoded[14:16]) != 0 {
		return fileHeader{}, fmt.Errorf("%w: invalid file kind or flags", ErrCorrupt)
	}
	var uuid [16]byte
	copy(uuid[:], encoded[16:32])
	return fileHeader{kind: kind, uuid: uuid}, nil
}

func encodeRecordHeader(header recordHeader) [recordHeaderSize]byte {
	var encoded [recordHeaderSize]byte
	binary.LittleEndian.PutUint32(encoded[0:4], header.recordLength)
	binary.LittleEndian.PutUint16(encoded[4:6], uint16(header.kind))
	binary.LittleEndian.PutUint32(encoded[8:12], header.schema)
	binary.LittleEndian.PutUint32(encoded[12:16], header.idLength)
	binary.LittleEndian.PutUint32(encoded[16:20], header.payloadLength)
	binary.LittleEndian.PutUint32(encoded[20:24], header.payloadCRC)
	return encoded
}

func decodeRecordHeader(encoded []byte) (recordHeader, error) {
	if len(encoded) != recordHeaderSize {
		return recordHeader{}, fmt.Errorf("%w: invalid record header length", ErrCorrupt)
	}
	header := recordHeader{
		recordLength:  binary.LittleEndian.Uint32(encoded[0:4]),
		kind:          ObjectKind(binary.LittleEndian.Uint16(encoded[4:6])),
		schema:        binary.LittleEndian.Uint32(encoded[8:12]),
		idLength:      binary.LittleEndian.Uint32(encoded[12:16]),
		payloadLength: binary.LittleEndian.Uint32(encoded[16:20]),
		payloadCRC:    binary.LittleEndian.Uint32(encoded[20:24]),
	}
	if binary.LittleEndian.Uint16(encoded[6:8]) != 0 {
		return recordHeader{}, fmt.Errorf("%w: unsupported record flags", ErrCorrupt)
	}
	if header.kind == 0 || header.schema == 0 || header.idLength == 0 || header.idLength > maxIDLength {
		return recordHeader{}, fmt.Errorf("%w: invalid record identity", ErrCorrupt)
	}
	if header.payloadLength > maxPayloadLength {
		return recordHeader{}, fmt.Errorf("%w: payload exceeds limit", ErrCorrupt)
	}
	wantLength := uint64(recordHeaderSize) + uint64(header.idLength) + uint64(header.payloadLength)
	if wantLength != uint64(header.recordLength) {
		return recordHeader{}, fmt.Errorf("%w: inconsistent record length", ErrCorrupt)
	}
	return header, nil
}

func validateRecord(kind ObjectKind, schemaVersion uint32, id string, payloadLength int) error {
	if kind == 0 || schemaVersion == 0 || id == "" || !utf8.ValidString(id) {
		return fmt.Errorf("%w: kind, schema version, and UTF-8 ID are required", ErrInvalidArgument)
	}
	if len(id) > maxIDLength || payloadLength < 0 || payloadLength > maxPayloadLength {
		return fmt.Errorf("%w: record exceeds size limit", ErrInvalidArgument)
	}
	return nil
}

func validFileKind(kind FileKind) bool {
	return kind == FileKindSystem || kind == FileKindDatabase
}

func writeFull(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := writer.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}
