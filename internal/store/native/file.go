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
	"sync/atomic"
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
	ObjectKindSnapshotMeta    ObjectKind = 10
	ObjectKindConfiguration   ObjectKind = 11
	ObjectKindCommittedChange ObjectKind = 12

	// 9 and 13 were the two Membership objects — leaf-to-Row and Row-to-leaf.
	// A Route leaf now records the Row it holds in a field on the leaf itself,
	// so nothing writes or reads these any more. See
	// docs/storage/leaf-rowid-v1.md.
	//
	// The numbers stay retired rather than reclaimed: databases written before
	// the switch still carry these records, and reusing a number would make
	// those bytes decode as something else. New writes are refused; old records
	// are inert.
	objectKindRetiredRouteMembership    ObjectKind = 9
	objectKindRetiredRouteRowMembership ObjectKind = 13

	// ObjectKindMax is the largest persisted object kind. Discovery and
	// migration sweeps enumerate [ObjectKindDatabase, ObjectKindMax].
	//
	// It stays at 13 even though 13 is retired: an existing database may hold
	// records of that kind, and a sweep that stopped at 12 would report them as
	// an unsupported kind and refuse to migrate a database that is merely old.
	ObjectKindMax ObjectKind = objectKindRetiredRouteRowMembership
)

const (
	objectKindTransactionBegin  ObjectKind = 0xfffe
	objectKindTransactionCommit ObjectKind = 0xffff
)

type File struct {
	mu   sync.RWMutex
	file *os.File
	kind FileKind
	// path is the log's own name, which the commit hint sits beside.
	path string
	// committed is where the last committed transaction ends. It is what the
	// open establishes and what every append advances; see commit_hint.go for
	// why the open can establish it without reading the log.
	committed int64
	// hinted is the value last written to the hint file, so the hint is
	// rewritten on an interval rather than on every commit.
	hinted int64
	// recoveredRecords is how many record headers the open had to hop to find
	// the end of the log. It is what the open-cost gate measures: the number
	// must not be a function of how long the log is.
	recoveredRecords int
	closed           bool
	// enumerations counts full-file sweeps (IDs, Records). Every one of them is
	// O(all records ever written), so a read path that takes one does not scale
	// with the data. Tests assert this stays at zero for point reads.
	enumerations atomic.Uint64
	// binlog receives one frame per committed transaction, or is nil when this
	// file commits without one. See AttachBinlog.
	binlog BinlogSink
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

// Location is a record payload's physical address in the file. It is durable —
// records are append-only and never move — so a Location may be persisted
// inside another record to chain revisions together, and resolving one costs a
// single read with no index in the way.
//
// A Location is self-verifying: ReadAtLocation checks the payload against the
// CRC recorded here, so an address recovered from an older record cannot
// silently return the wrong bytes.
type Location struct {
	Offset uint64
	Length uint32
	CRC    uint32
}

// Valid reports whether a Location addresses anything. The zero Location is the
// end of a chain, never a readable address: offset 0 is inside the file header.
func (location Location) Valid() bool { return location.Offset > 0 }

func (meta recordMeta) location() Location {
	return Location{
		Offset: uint64(meta.payloadOffset),
		Length: meta.payloadLength,
		CRC:    meta.payloadCRC,
	}
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
	// locations is filled by Complete. A writer that needs to chain a later
	// revision to one it just wrote reads the address back from here.
	locations map[recordKey]Location
	// prepared, digest and metas carry Prepare's work over to Complete. They
	// are the transaction's state between the two halves, which is the window
	// another store commits in.
	prepared bool
	digest   []byte
	metas    map[recordKey]recordMeta
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
	result := &File{file: file, kind: kind, path: path, committed: int64(fileHeaderSize)}
	_ = writeCommitHint(path, result.committed)
	result.hinted = result.committed
	return result, nil
}

func Open(path string) (*File, error) {
	if path == "" {
		return nil, fmt.Errorf("%w: path is required", ErrInvalidArgument)
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open native store file: %w", err)
	}

	result, err := openFile(file, path)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return result, nil
}

func openFile(file *os.File, path string) (*File, error) {
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

	// The header and nothing else. E8 stage 3 removed the scan that used to run
	// here: it read every record ever written, verified each one, and built a
	// map of where they all live — time and memory proportional to how many
	// times the Database has been written to, paid on every open, and the
	// single largest thing clustered promotion exists to delete
	// (docs/storage/record-index-and-authority-v1.md §1).
	//
	// Two things went with it, both deliberately:
	//
	// Corruption is found when a record is read rather than when the file is
	// opened. That is what every other store here already does — page.Open
	// reads one page — and a log whose records are individually checksummed
	// does not need to be read end to end to be opened. Verify still does it on
	// request.
	//
	// A torn tail is left in place rather than truncated. Finding it costs the
	// same scan, and it is inert: the walk that reads this file stops at the
	// last commit mark, and the next write appends past it. What it costs is
	// bytes, on a file that already never reclaims any.
	result := &File{file: file, kind: header.kind, path: path}
	if err := result.recoverCommitted(path, stat.Size()); err != nil {
		return nil, err
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
	if _, err := f.appendRecord(kind, schemaVersion, id, payload); err != nil {
		return err
	}
	if err := f.file.Sync(); err != nil {
		return fmt.Errorf("sync native record: %w", err)
	}
	f.advanceCommittedLocked()
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
	if _, exists := transaction.keys[key]; exists {
		return ErrDuplicateID
	}
	transaction.keys[key] = struct{}{}
	transaction.records = append(transaction.records, bufferedRecord{kind: kind, schema: schemaVersion, id: id, payload: append([]byte(nil), payload...)})
	return nil
}

func (transaction *Transaction) Commit() error {
	if err := transaction.Prepare(); err != nil {
		return err
	}
	return transaction.Complete()
}

// Prepare writes the transaction's records and reports them to the binlog,
// stopping one step short of the commit mark.
//
// This is the prepare of the write model's prepare → binlog → commit
// (docs/product/write-model.md §3). After it returns the records are on disk
// and the log holds the frame, and nothing yet claims the transaction
// committed: a crash here leaves a tail the next open discards.
//
// It is separate from Complete so another store can be the one that decides.
// The page Trees commit between the two, and the mark this file writes
// afterwards is an archive's record of what the Trees committed, not the
// decision itself.
func (transaction *Transaction) Prepare() error {
	if transaction == nil || transaction.closed || transaction.file == nil {
		return ErrClosed
	}
	if transaction.prepared {
		return fmt.Errorf("%w: transaction is already prepared", ErrInvalidArgument)
	}
	transaction.file.mu.Lock()
	defer transaction.file.mu.Unlock()
	if transaction.file.closed {
		return ErrClosed
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
	// The binlog goes in before the commit record, which is the point this
	// transaction becomes committed. A crash between them leaves a transaction
	// that the binlog can replay but that nothing claims was committed — the
	// safe side. The reverse order would allow the opposite: committed, and
	// missing from the log that is supposed to be able to rebuild it.
	// See docs/storage/three-logs-v1.md §2.
	if err := transaction.file.appendBinlog(transaction); err != nil {
		return err
	}
	transaction.digest = digest.Sum(nil)
	transaction.metas = metas
	transaction.prepared = true
	return nil
}

// Complete writes the commit mark for a prepared transaction and publishes its
// records.
//
// Until it returns the records are present but invisible: a reopen discards
// them. A transaction that is prepared and never completed is therefore not a
// loss to repair but an outcome — the write did not happen.
func (transaction *Transaction) Complete() error {
	if transaction == nil || transaction.closed || transaction.file == nil {
		return ErrClosed
	}
	if !transaction.prepared {
		return fmt.Errorf("%w: transaction is not prepared", ErrInvalidArgument)
	}
	transaction.file.mu.Lock()
	defer transaction.file.mu.Unlock()
	if transaction.file.closed {
		return ErrClosed
	}
	transaction.closed = true
	if _, err := transaction.file.appendRecord(
		objectKindTransactionCommit, 1, transaction.id, transaction.digest,
	); err != nil {
		return err
	}
	if err := transaction.file.file.Sync(); err != nil {
		return fmt.Errorf("sync native transaction commit: %w", err)
	}
	transaction.file.advanceCommittedLocked()
	transaction.locations = make(map[recordKey]Location, len(transaction.metas))
	for key, meta := range transaction.metas {
		transaction.locations[key] = meta.location()
	}
	return nil
}

// Location returns the physical address Commit assigned to a record this
// transaction wrote. It reports false before the commit succeeds: an uncommitted
// record has no durable address to point at.
func (transaction *Transaction) Location(kind ObjectKind, id string) (Location, bool) {
	if transaction == nil || transaction.locations == nil {
		return Location{}, false
	}
	location, ok := transaction.locations[recordKey{kind: kind, id: id}]
	return location, ok
}

// BinlogSink receives one frame per committed transaction. It is an interface
// so the record store does not depend on the log's package: the store owns the
// commit point, and the log is what that point is reported to.
type BinlogSink interface {
	Append(transactionID string, records []BinlogRecord) error
}

// BinlogRecord is one object a transaction wrote, in this store's encoding.
type BinlogRecord struct {
	Kind          uint16
	SchemaVersion uint32
	ID            string
	Payload       []byte
}

// AttachBinlog directs this file's commits at a log. A file with no log
// attached commits exactly as it always did — which is what every caller that
// has not opted in still gets.
func (f *File) AttachBinlog(sink BinlogSink) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.binlog = sink
}

// appendBinlog reports one transaction to the log. The caller holds the file
// lock and has already synced the records.
func (f *File) appendBinlog(transaction *Transaction) error {
	if f.binlog == nil {
		return nil
	}
	records := make([]BinlogRecord, 0, len(transaction.records))
	for _, record := range transaction.records {
		records = append(records, BinlogRecord{
			Kind: uint16(record.kind), SchemaVersion: record.schema,
			ID: record.id, Payload: record.payload,
		})
	}
	if err := f.binlog.Append(transaction.id, records); err != nil {
		return fmt.Errorf("append transaction to binlog: %w", err)
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
	return f.findRecordLocked(kind, id)
}

// Enumerations reports how many full-file sweeps this handle has served. A read
// path that scales with the data takes none: the count is the regression guard
// against a point read quietly going back to enumerating every record.
func (f *File) Enumerations() uint64 {
	if f == nil {
		return 0
	}
	return f.enumerations.Load()
}

// Location returns a record's physical address so a writer can point a new
// revision at an existing one.
//
// It resolves the name by walking the log, which is a pass over the file and is
// counted as one. There is no index to resolve it through any more — see the
// File doc comment — so a caller on a hot path must not use this.
func (f *File) Location(kind ObjectKind, id string) (Location, error) {
	if f == nil {
		return Location{}, ErrClosed
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.closed {
		return Location{}, ErrClosed
	}
	meta, err := f.findMetaLocked(kind, id)
	if err != nil {
		return Location{}, err
	}
	return meta.location(), nil
}

// ReadAtLocation reads a payload by physical address alone. This is the read
// that a version chain walks: it never consults the record map, so a revision
// reachable only through a pointer stored in a newer revision still resolves in
// one read.
func (f *File) ReadAtLocation(location Location) ([]byte, error) {
	if f == nil {
		return nil, ErrClosed
	}
	if !location.Valid() {
		return nil, fmt.Errorf("%w: location does not address a payload", ErrInvalidArgument)
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.closed {
		return nil, ErrClosed
	}
	payload := make([]byte, location.Length)
	if _, err := f.file.ReadAt(payload, int64(location.Offset)); err != nil {
		return nil, fmt.Errorf("read native record payload at %d: %w", location.Offset, err)
	}
	if crc32.ChecksumIEEE(payload) != location.CRC {
		return nil, fmt.Errorf("%w: payload CRC mismatch at %d", ErrCorrupt, location.Offset)
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
	f.enumerations.Add(1)
	ids := make([]string, 0)
	if err := f.walkCommitted(func(recordKind ObjectKind, id string, _ recordMeta) error {
		if recordKind == kind {
			ids = append(ids, id)
		}
		return nil
	}); err != nil {
		return nil, err
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
	f.enumerations.Add(1)
	result := make([]RecordRef, 0)
	if err := f.walkCommitted(func(kind ObjectKind, id string, meta recordMeta) error {
		result = append(result, RecordRef{
			Kind: kind, SchemaVersion: meta.schemaVersion, ID: id,
			PayloadLength: meta.payloadLength,
		})
		return nil
	}); err != nil {
		return nil, err
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
	// A clean close leaves an exact hint, so the next open walks nothing.
	if f.path != "" && f.committed != f.hinted {
		if writeCommitHint(f.path, f.committed) == nil {
			f.hinted = f.committed
		}
	}
	return f.file.Close()
}

// Verify reads the whole log and reports the first thing wrong with it.
//
// This is what opening used to do on every open. E8 stage 3 took it off that
// path — the cost was proportional to how many times the Database had ever been
// written to — and left it here, for a caller that actually wants to know: a
// doctor command, a restore checking what it just wrote, a test.
//
// It reports corruption. It does not repair, and it does not truncate a torn
// tail: a transaction with no commit mark is not damage, it is a write that did
// not happen, and the readers already skip it.
func (f *File) Verify() error {
	if f == nil {
		return ErrClosed
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.closed {
		return ErrClosed
	}
	f.enumerations.Add(1)
	stat, err := f.file.Stat()
	if err != nil {
		return fmt.Errorf("stat native store file: %w", err)
	}
	return f.verifyLocked(stat.Size())
}

func (f *File) verifyLocked(fileSize int64) error {
	type pendingTransaction struct {
		id      string
		start   int64
		records map[recordKey]recordMeta
		digest  []byte
	}
	// Duplicate IDs are still refused across every kind: the set lives only for
	// the length of this scan, so detecting them costs nothing after Open
	// returns. What is not kept is where the record is, for the kinds nothing
	// point-reads any more.
	seen := make(map[recordKey]struct{})
	var pending *pendingTransaction
	for offset := int64(fileHeaderSize); offset < fileSize; {
		if fileSize-offset < recordHeaderSize {
			// A record that runs past the end of the file is where a crash
			// landed mid-append. Nothing after it is readable and nothing
			// claims it committed, so the log ends here.
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
			// A record that runs past the end of the file is where a crash
			// landed mid-append. Nothing after it is readable and nothing
			// claims it committed, so the log ends here.
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
		if _, exists := seen[key]; exists {
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
			if len(payload) != 0 {
				return fmt.Errorf("%w: invalid transaction BEGIN", ErrCorrupt)
			}
			// A BEGIN while one is already open means that one never reached
			// its COMMIT. It used to be corruption, because the only way to
			// write a BEGIN without a COMMIT was to die between them, and a
			// dead process writes nothing after. Prepare/Complete made it an
			// ordinary outcome: the Trees refused the write, so the prepared
			// records stay where they are, unclaimed, and the next write lands
			// after them. Unclaimed is exactly what "not committed" means, so
			// the span is dropped and the scan carries on.
			pending = &pendingTransaction{id: string(id), start: offset, records: make(map[recordKey]recordMeta)}
		case objectKindTransactionCommit:
			if pending == nil || pending.id != string(id) || len(payload) != sha256.Size {
				return fmt.Errorf("%w: invalid transaction COMMIT", ErrCorrupt)
			}
			want := sha256.Sum256(pending.digest)
			if !bytes.Equal(payload, want[:]) {
				return fmt.Errorf("%w: transaction digest mismatch", ErrCorrupt)
			}
			for key := range pending.records {
				seen[key] = struct{}{}
			}
			pending = nil
		default:
			if pending == nil {
				seen[key] = struct{}{}
			} else {
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
	if kind == objectKindRetiredRouteMembership || kind == objectKindRetiredRouteRowMembership {
		return fmt.Errorf("%w: object kind %d is retired", ErrInvalidArgument, kind)
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

// walkCommitted visits every committed logical record in the log, in physical
// order, skipping transaction markers and any incomplete crash tail.
//
// It exists so enumeration does not need a process-resident index. IDs and
// Records used to read f.records, which is why that map had to hold an entry
// for every record the file had ever seen; walking on demand is what lets the
// map go away (architecture principle four, criterion 3).
//
// It reads record headers and IDs but never payloads, and does not re-verify
// payload CRCs: Open already checked every one, and both callers of this walk
// (the migration Plan build and the snapshot export) read the payloads they
// care about through Get, which checks the CRC again at that point. Reading
// every payload here would make enumeration cost the same as a full open for
// no additional guarantee.
//
// The caller must hold at least a read lock on f.mu.
func (f *File) walkCommitted(visit func(kind ObjectKind, id string, meta recordMeta) error) error {
	return f.walkCommittedFrom(int64(fileHeaderSize), visit)
}

// walkCommittedFrom is walkCommitted starting at a record boundary rather than
// at the first record. A derived index that already holds everything up to some
// Size uses it to read only the tail.
func (f *File) walkCommittedFrom(
	start int64,
	visit func(kind ObjectKind, id string, meta recordMeta) error,
) error {
	if start < int64(fileHeaderSize) {
		start = int64(fileHeaderSize)
	}
	stat, err := f.file.Stat()
	if err != nil {
		return fmt.Errorf("stat native store file: %w", err)
	}
	fileSize := stat.Size()

	type pendingRecord struct {
		kind ObjectKind
		id   string
		meta recordMeta
	}
	var pending []pendingRecord
	inTransaction := false

	for offset := start; offset < fileSize; {
		if fileSize-offset < recordHeaderSize {
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
			return nil
		}
		id := make([]byte, header.idLength)
		if _, err := f.file.ReadAt(id, offset+recordHeaderSize); err != nil {
			return fmt.Errorf("read native record ID at offset %d: %w", offset, err)
		}
		meta := recordMeta{
			payloadOffset: offset + recordHeaderSize + int64(header.idLength),
			payloadLength: header.payloadLength,
			payloadCRC:    header.payloadCRC,
			schemaVersion: header.schema,
		}
		switch header.kind {
		case objectKindTransactionBegin:
			inTransaction, pending = true, pending[:0]
		case objectKindTransactionCommit:
			for _, record := range pending {
				if err := visit(record.kind, record.id, record.meta); err != nil {
					return err
				}
			}
			inTransaction, pending = false, pending[:0]
		default:
			if inTransaction {
				pending = append(pending, pendingRecord{kind: header.kind, id: string(id), meta: meta})
			} else if err := visit(header.kind, string(id), meta); err != nil {
				return err
			}
		}
		offset += int64(header.recordLength)
	}
	// An unterminated transaction at the tail is not committed; its buffered
	// records are dropped rather than visited.
	return nil
}

// Size reports the record log's current length in bytes.
//
// It is a durable position, not a statistic: a length taken after a commit is a
// record boundary, so a derived index can store it and later ask only for the
// records written past it. RecordsSince is that ask.
// advanceCommittedLocked records that everything written so far is committed,
// and refreshes the hint when enough has been written since the last one.
//
// The caller holds the write lock and has already synced. The hint is written
// without a sync of its own and a failure is swallowed: a stale or missing hint
// costs a longer walk after the next crash, and nothing else — see
// commit_hint.go.
func (f *File) advanceCommittedLocked() {
	offset, err := f.file.Seek(0, io.SeekEnd)
	if err != nil {
		return
	}
	f.committed = offset
	if f.path == "" || f.committed-f.hinted < hintInterval {
		return
	}
	if writeCommitHint(f.path, f.committed) == nil {
		f.hinted = f.committed
	}
}

// Path is where this Database's record log lives on disk.
//
// A backup copies the file rather than reading it record by record, so it needs
// the name, and taking it from the handle keeps the caller from having to carry
// the path alongside the File it already holds.
func (f *File) Path() string {
	if f == nil || f.file == nil {
		return ""
	}
	return f.file.Name()
}

func (f *File) Size() (int64, error) {
	if f == nil {
		return 0, ErrClosed
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.closed {
		return 0, ErrClosed
	}
	return f.committed, nil
}

// Record is one committed record with its payload.
type Record struct {
	Kind          ObjectKind
	SchemaVersion uint32
	ID            string
	Payload       []byte
}

// RecordsSince returns every committed record written at or after offset, in
// physical order, with its payload.
//
// A derived index catches up with this: it stores the Size it last indexed
// through and asks for the tail, so a restart costs the records written since
// rather than every record the file has ever held. Offset zero asks for all of
// them, which is what a rebuild wants.
//
// The offset must be a record boundary — a Size taken after a commit always is.
func (f *File) RecordsSince(offset int64) ([]Record, error) {
	if f == nil {
		return nil, ErrClosed
	}
	if offset < 0 {
		return nil, fmt.Errorf("%w: negative offset", ErrInvalidArgument)
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.closed {
		return nil, ErrClosed
	}
	// Enumerations counts passes over the whole file, which is what the read-path
	// gates assert never happens. A read that starts from an offset a caller has
	// durably recorded is bounded by what was written since, so it is not one —
	// and counting it would make those gates fire on a bounded tail read while
	// saying nothing new about a real sweep.
	if offset <= int64(fileHeaderSize) {
		f.enumerations.Add(1)
	}
	result := make([]Record, 0)
	err := f.walkCommittedFrom(offset, func(kind ObjectKind, id string, meta recordMeta) error {
		payload := make([]byte, meta.payloadLength)
		if _, err := f.file.ReadAt(payload, meta.payloadOffset); err != nil {
			return fmt.Errorf("read native record payload at %d: %w", meta.payloadOffset, err)
		}
		if crc32.ChecksumIEEE(payload) != meta.payloadCRC {
			return fmt.Errorf("%w: payload CRC mismatch at %d", ErrCorrupt, meta.payloadOffset)
		}
		result = append(result, Record{
			Kind: kind, SchemaVersion: meta.schemaVersion, ID: id, Payload: payload,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// FindRecord returns one committed record's payload by walking the log, without
// consulting the resident index.
//
// It is for the paths that read a record but must not be a reason for that
// index to exist — a snapshot export checking whether its cached source is
// still current, for instance. The cost is a pass over the log, so it is only
// correct to use where a pass is already being made or the read is rare; a live
// read path wants a Tree, not this.
func (f *File) FindRecord(kind ObjectKind, id string) ([]byte, error) {
	if f == nil {
		return nil, ErrClosed
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.closed {
		return nil, ErrClosed
	}
	return f.findRecordLocked(kind, id)
}

// findRecordLocked reads one record's payload by walking the log. The caller
// holds at least a read lock.
func (f *File) findRecordLocked(kind ObjectKind, id string) ([]byte, error) {
	found, err := f.findMetaLocked(kind, id)
	if err != nil {
		return nil, err
	}
	payload := make([]byte, found.payloadLength)
	if _, err := f.file.ReadAt(payload, found.payloadOffset); err != nil {
		return nil, fmt.Errorf("read native record payload at %d: %w", found.payloadOffset, err)
	}
	if crc32.ChecksumIEEE(payload) != found.payloadCRC {
		return nil, fmt.Errorf("%w: payload CRC mismatch at %d", ErrCorrupt, found.payloadOffset)
	}
	return payload, nil
}

func (f *File) findMetaLocked(kind ObjectKind, id string) (recordMeta, error) {
	f.enumerations.Add(1)
	var found *recordMeta
	err := f.walkCommittedFrom(int64(fileHeaderSize),
		func(recordKind ObjectKind, recordIdentity string, meta recordMeta) error {
			if recordKind == kind && recordIdentity == id {
				value := meta
				found = &value
			}
			return nil
		})
	if err != nil {
		return recordMeta{}, err
	}
	if found == nil {
		return recordMeta{}, fmt.Errorf("%w: kind %d id %q", ErrNotFound, kind, id)
	}
	return *found, nil
}

// RecordsOfKind returns every committed record of one kind, with its payload,
// in ascending ID order.
//
// It replaces asking for the IDs of a kind and then looking each one up. That
// shape cost one pass plus a lookup per record, and the lookup went through the
// record log's resident index — so removing that index would have turned every
// one of those readers quadratic. One pass carries everything they need.
//
// These are the readers that run without a generation: the migration that
// builds one from the log, and anything opened on a file with no Authority. A
// reader that has a Tree should use it rather than this.
// RecordsMatching returns, in one pass, the committed records of a kind whose
// ID the predicate keeps, in ascending ID order.
//
// It is RecordsOfKind with a filter applied while walking rather than after, so
// a caller after one object's revisions does not first materialise every record
// of that kind. One pass either way — there is no index over this log — but the
// memory is what the caller asked for instead of what the Database holds.
func (f *File) RecordsMatching(kind ObjectKind, keep func(id string) bool) ([]Record, error) {
	if f == nil {
		return nil, ErrClosed
	}
	if keep == nil {
		return f.RecordsOfKind(kind)
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.closed {
		return nil, ErrClosed
	}
	f.enumerations.Add(1)
	result := make([]Record, 0)
	err := f.walkCommittedFrom(int64(fileHeaderSize),
		func(recordKind ObjectKind, id string, meta recordMeta) error {
			if recordKind != kind || !keep(id) {
				return nil
			}
			payload := make([]byte, meta.payloadLength)
			if _, err := f.file.ReadAt(payload, meta.payloadOffset); err != nil {
				return fmt.Errorf("read native record payload at %d: %w", meta.payloadOffset, err)
			}
			if crc32.ChecksumIEEE(payload) != meta.payloadCRC {
				return fmt.Errorf("%w: payload CRC mismatch at %d", ErrCorrupt, meta.payloadOffset)
			}
			result = append(result, Record{
				Kind: recordKind, SchemaVersion: meta.schemaVersion, ID: id, Payload: payload,
			})
			return nil
		})
	if err != nil {
		return nil, err
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result, nil
}

func (f *File) RecordsOfKind(kind ObjectKind) ([]Record, error) {
	records, err := f.RecordsSince(0)
	if err != nil {
		return nil, err
	}
	result := make([]Record, 0)
	for _, record := range records {
		if record.Kind == kind {
			result = append(result, record)
		}
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result, nil
}
