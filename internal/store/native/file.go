package native

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"sort"
	"sync"
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
	ObjectKindOpaque          ObjectKind = 1
	ObjectKindDatabase        ObjectKind = 2
	ObjectKindTable           ObjectKind = 3
	ObjectKindColumn          ObjectKind = 4
	ObjectKindRow             ObjectKind = 5
	ObjectKindHistory         ObjectKind = 6
	ObjectKindRelation        ObjectKind = 7
	ObjectKindRoute           ObjectKind = 8
	ObjectKindRouteMembership ObjectKind = 9
	ObjectKindSnapshotMeta    ObjectKind = 10
	ObjectKindConfiguration   ObjectKind = 11
)

const (
	objectKindTransactionBegin  ObjectKind = 0xfffe
	objectKindTransactionCommit ObjectKind = 0xffff
)

type File struct {
	mu             sync.RWMutex
	file           *os.File
	kind           FileKind
	records        map[recordKey]recordMeta
	recoveryOffset int64
	closed         bool
}

type recordKey struct {
	kind ObjectKind
	id   string
}

type recordMeta struct {
	payloadOffset int64
	payloadLength uint32
	payloadCRC    uint32
	schemaVersion uint32
}

type RecordRef struct {
	Kind          ObjectKind
	SchemaVersion uint32
	ID            string
	PayloadLength uint32
}

type bufferedRecord struct {
	kind    ObjectKind
	schema  uint32
	id      string
	payload []byte
}

type Transaction struct {
	file    *File
	id      string
	records []bufferedRecord
	keys    map[recordKey]struct{}
	closed  bool
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
	if err := file.Sync(); err != nil {
		return nil, fmt.Errorf("sync native store header: %w", err)
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
	if result.recoveryOffset > 0 && result.recoveryOffset < stat.Size() {
		if err := file.Truncate(result.recoveryOffset); err != nil {
			return nil, fmt.Errorf("truncate native crash tail: %w", err)
		}
		if err := file.Sync(); err != nil {
			return nil, fmt.Errorf("sync native crash recovery: %w", err)
		}
		result.recoveryOffset = 0
	}
	return result, nil
}

func (f *File) Put(kind ObjectKind, schemaVersion uint32, id string, payload []byte) error {
	if f == nil {
		return ErrClosed
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return ErrClosed
	}
	if err := validateRecord(kind, schemaVersion, id, len(payload)); err != nil {
		return err
	}
	if kind == objectKindTransactionBegin || kind == objectKindTransactionCommit {
		return fmt.Errorf("%w: reserved transaction kind", ErrInvalidArgument)
	}
	key := recordKey{kind: kind, id: id}
	if _, exists := f.records[key]; exists {
		return ErrDuplicateID
	}

	meta, err := f.appendRecord(kind, schemaVersion, id, payload)
	if err != nil {
		return err
	}
	if err := f.file.Sync(); err != nil {
		return fmt.Errorf("sync native record: %w", err)
	}
	f.records[key] = meta
	return nil
}

func (f *File) Begin() (*Transaction, error) {
	if f == nil {
		return nil, ErrClosed
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.closed {
		return nil, ErrClosed
	}
	return &Transaction{file: f, keys: make(map[recordKey]struct{})}, nil
}

func (transaction *Transaction) Put(kind ObjectKind, schemaVersion uint32, id string, payload []byte) error {
	if transaction == nil || transaction.closed || transaction.file == nil {
		return ErrClosed
	}
	if kind == objectKindTransactionBegin || kind == objectKindTransactionCommit {
		return fmt.Errorf("%w: reserved transaction kind", ErrInvalidArgument)
	}
	if err := validateRecord(kind, schemaVersion, id, len(payload)); err != nil {
		return err
	}
	key := recordKey{kind: kind, id: id}
	transaction.file.mu.RLock()
	defer transaction.file.mu.RUnlock()
	if transaction.file.closed {
		return ErrClosed
	}
	if _, exists := transaction.file.records[key]; exists {
		return ErrDuplicateID
	}
	if _, exists := transaction.keys[key]; exists {
		return ErrDuplicateID
	}
	transaction.keys[key] = struct{}{}
	transaction.records = append(transaction.records, bufferedRecord{kind: kind, schema: schemaVersion, id: id, payload: append([]byte(nil), payload...)})
	return nil
}

func (transaction *Transaction) Commit() error {
	if transaction == nil || transaction.closed || transaction.file == nil {
		return ErrClosed
	}
	transaction.file.mu.Lock()
	defer transaction.file.mu.Unlock()
	if transaction.file.closed {
		return ErrClosed
	}
	transaction.closed = true
	for key := range transaction.keys {
		if _, exists := transaction.file.records[key]; exists {
			return ErrDuplicateID
		}
	}
	transaction.id = deterministicTransactionID(transaction.records)
	if _, err := transaction.file.appendRecord(objectKindTransactionBegin, 1, transaction.id, nil); err != nil {
		return err
	}
	digest := sha256.New()
	metas := make(map[recordKey]recordMeta, len(transaction.records))
	for _, record := range transaction.records {
		encoded := encodeRecord(record.kind, record.schema, record.id, record.payload)
		_, _ = digest.Write(encoded)
		meta, err := transaction.file.appendEncoded(
			encoded, record.schema, len(record.id), len(record.payload), crc32.ChecksumIEEE(record.payload),
		)
		if err != nil {
			return err
		}
		metas[recordKey{kind: record.kind, id: record.id}] = meta
	}
	if err := transaction.file.file.Sync(); err != nil {
		return fmt.Errorf("sync native transaction records: %w", err)
	}
	if _, err := transaction.file.appendRecord(objectKindTransactionCommit, 1, transaction.id, digest.Sum(nil)); err != nil {
		return err
	}
	if err := transaction.file.file.Sync(); err != nil {
		return fmt.Errorf("sync native transaction commit: %w", err)
	}
	for key, meta := range metas {
		transaction.file.records[key] = meta
	}
	return nil
}

func deterministicTransactionID(records []bufferedRecord) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("memora-native-transaction-v1\x00"))
	for _, record := range records {
		_, _ = hash.Write(encodeRecord(record.kind, record.schema, record.id, record.payload))
	}
	return hex.EncodeToString(hash.Sum(nil)[:16])
}

func (transaction *Transaction) Rollback() error {
	if transaction == nil || transaction.closed {
		return ErrClosed
	}
	transaction.closed = true
	transaction.records = nil
	return nil
}

func (f *File) appendRecord(kind ObjectKind, schema uint32, id string, payload []byte) (recordMeta, error) {
	encoded := encodeRecord(kind, schema, id, payload)
	return f.appendEncoded(encoded, schema, len(id), len(payload), crc32.ChecksumIEEE(payload))
}

func (f *File) appendEncoded(
	encoded []byte,
	schemaVersion uint32,
	idLength, payloadLength int,
	payloadCRC uint32,
) (recordMeta, error) {
	offset, err := f.file.Seek(0, io.SeekEnd)
	if err != nil {
		return recordMeta{}, fmt.Errorf("seek native store file: %w", err)
	}
	if err := writeFull(f.file, encoded); err != nil {
		return recordMeta{}, fmt.Errorf("write native record: %w", err)
	}
	return recordMeta{
		payloadOffset: offset + recordHeaderSize + int64(idLength),
		payloadLength: uint32(payloadLength), payloadCRC: payloadCRC,
		schemaVersion: schemaVersion,
	}, nil
}

func encodeRecord(kind ObjectKind, schema uint32, id string, payload []byte) []byte {
	header := encodeRecordHeader(recordHeader{recordLength: uint32(recordHeaderSize + len(id) + len(payload)), kind: kind, schema: schema, idLength: uint32(len(id)), payloadLength: uint32(len(payload)), payloadCRC: crc32.ChecksumIEEE(payload)})
	encoded := make([]byte, 0, len(header)+len(id)+len(payload))
	encoded = append(encoded, header[:]...)
	encoded = append(encoded, id...)
	return append(encoded, payload...)
}

func (f *File) Get(kind ObjectKind, id string) ([]byte, error) {
	if f == nil {
		return nil, ErrClosed
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.closed {
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

func (f *File) Kind() (FileKind, error) {
	if f == nil {
		return 0, ErrClosed
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.closed {
		return 0, ErrClosed
	}
	return f.kind, nil
}

func (f *File) IDs(kind ObjectKind) ([]string, error) {
	if f == nil {
		return nil, ErrClosed
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.closed {
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

// Records returns every committed logical record in deterministic physical-kind/key order.
// Transaction markers and an incomplete crash tail are never included.
func (f *File) Records() ([]RecordRef, error) {
	if f == nil {
		return nil, ErrClosed
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.closed {
		return nil, ErrClosed
	}
	result := make([]RecordRef, 0, len(f.records))
	for key, meta := range f.records {
		result = append(result, RecordRef{
			Kind: key.kind, SchemaVersion: meta.schemaVersion, ID: key.id,
			PayloadLength: meta.payloadLength,
		})
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].Kind != result[right].Kind {
			return result[left].Kind < result[right].Kind
		}
		return result[left].ID < result[right].ID
	})
	return result, nil
}

func (f *File) Close() error {
	if f == nil {
		return ErrClosed
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return ErrClosed
	}
	f.closed = true
	return f.file.Close()
}

func (f *File) scan(fileSize int64) error {
	type pendingTransaction struct {
		id      string
		start   int64
		records map[recordKey]recordMeta
		digest  []byte
	}
	var pending *pendingTransaction
	safeOffset := int64(fileHeaderSize)
	for offset := int64(fileHeaderSize); offset < fileSize; {
		if fileSize-offset < recordHeaderSize {
			f.recoveryOffset = safeOffset
			if pending != nil {
				f.recoveryOffset = pending.start
			}
			return nil
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
			f.recoveryOffset = safeOffset
			if pending != nil {
				f.recoveryOffset = pending.start
			}
			return nil
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
		meta := recordMeta{
			payloadOffset: payloadOffset,
			payloadLength: header.payloadLength,
			payloadCRC:    header.payloadCRC,
			schemaVersion: header.schema,
		}
		switch header.kind {
		case objectKindTransactionBegin:
			if pending != nil || len(payload) != 0 {
				return fmt.Errorf("%w: invalid transaction BEGIN", ErrCorrupt)
			}
			pending = &pendingTransaction{id: string(id), start: offset, records: make(map[recordKey]recordMeta)}
		case objectKindTransactionCommit:
			if pending == nil || pending.id != string(id) || len(payload) != sha256.Size {
				return fmt.Errorf("%w: invalid transaction COMMIT", ErrCorrupt)
			}
			want := sha256.Sum256(pending.digest)
			if !bytes.Equal(payload, want[:]) {
				return fmt.Errorf("%w: transaction digest mismatch", ErrCorrupt)
			}
			for recordKey, recordMeta := range pending.records {
				f.records[recordKey] = recordMeta
			}
			pending = nil
			safeOffset = offset + int64(header.recordLength)
		default:
			if pending == nil {
				if _, exists := f.records[key]; exists {
					return fmt.Errorf("%w: duplicate record ID %q", ErrCorrupt, string(id))
				}
				f.records[key] = meta
				safeOffset = offset + int64(header.recordLength)
			} else {
				if _, exists := f.records[key]; exists {
					return fmt.Errorf("%w: duplicate record ID %q", ErrCorrupt, string(id))
				}
				if _, exists := pending.records[key]; exists {
					return fmt.Errorf("%w: duplicate transaction record ID %q", ErrCorrupt, string(id))
				}
				pending.records[key] = meta
				pending.digest = append(pending.digest, encoded[:]...)
				pending.digest = append(pending.digest, id...)
				pending.digest = append(pending.digest, payload...)
			}
		}
		offset += int64(header.recordLength)
	}
	if pending != nil {
		f.recoveryOffset = pending.start
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
