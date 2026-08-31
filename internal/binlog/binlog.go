// Package binlog is the logical log of what each committed transaction changed.
//
// It is the recovery basis: replaying it rebuilds the Database, including the
// history table, and nothing else has to be consulted to do so
// (docs/product/write-model.md §5). The record file remains the store that
// point reads go to; this is the sequential account of how it got that way.
//
// A record is one transaction. It carries the transaction's identity and the
// terminal state of every object the transaction wrote — the encoded records
// themselves, not the statements that produced them. The write model is
// explicit about that: the timestamp has to be the moment of the original
// write, which a replayed statement could not reproduce.
//
// See docs/storage/three-logs-v1.md.
package binlog

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sync"
)

var (
	ErrInvalid = errors.New("binlog input is invalid")
	ErrCorrupt = errors.New("binlog is corrupt")
	ErrClosed  = errors.New("binlog is closed")
)

const (
	// frameMagic opens every frame so a truncated tail is told apart from a
	// frame that starts where one is expected.
	frameMagic = uint32(0x4d454d42) // MEMB
	// maxFrameBytes bounds one transaction's frame. A frame that claims more
	// than this is corruption, not a large write: the record store caps a
	// single record well below it and a transaction is bounded by memory.
	maxFrameBytes = 64 << 20
	maxRecords    = 1 << 20
)

// Record is one object a transaction wrote, in the store's own encoding.
type Record struct {
	Kind          uint16
	SchemaVersion uint32
	ID            string
	Payload       []byte
}

// Entry is one committed transaction.
type Entry struct {
	TransactionID string
	Records       []Record
}

// Log is an append-only sequential log. One writer at a time; Append is
// serialized so a frame is never interleaved with another.
type Log struct {
	mu        sync.Mutex
	directory string
	options   Options
	// file is the active segment, the only one written to. The rolled ones are
	// read during replay and are the unit retention drops.
	file     *os.File
	sequence uint64
	written  int64
	closed   bool
}

// Open opens or creates the log in the given directory with the default
// rolling and retention.
func Open(directory string) (*Log, error) { return OpenWithOptions(directory, Options{}) }

// OpenWithOptions opens or creates the log and applies retention once, on the
// way in.
//
// Retention runs at open rather than on every append: dropping a file is a
// filesystem operation on the path a write is about to take, and doing it while
// appending would put it in the way of the write it is meant to make room for.
// A log that rolls on size and prunes on open converges just as well, because
// a Database that is being written to is also being opened.
func OpenWithOptions(directory string, options Options) (*Log, error) {
	if directory == "" {
		return nil, fmt.Errorf("%w: binlog directory", ErrInvalid)
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create binlog directory: %w", err)
	}
	sequences, err := segments(directory)
	if err != nil {
		return nil, err
	}
	active := uint64(1)
	if len(sequences) != 0 {
		active = sequences[len(sequences)-1]
	}
	if err := prune(directory, sequences, active, options.retention()); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(
		filepath.Join(directory, segmentName(active)), os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600,
	)
	if err != nil {
		return nil, fmt.Errorf("open binlog: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("stat binlog: %w", err)
	}
	return &Log{
		directory: directory, options: options, file: file,
		sequence: active, written: info.Size(),
	}, nil
}

// rollLocked closes the active segment and starts the next one.
//
// A frame is never split across segments: the roll happens between frames, so
// every file holds whole transactions and replaying a file is meaningful on its
// own. That is what makes a segment shippable and what makes it safe to drop.
func (log *Log) rollLocked() error {
	if err := log.file.Sync(); err != nil {
		return fmt.Errorf("sync binlog before roll: %w", err)
	}
	if err := log.file.Close(); err != nil {
		return fmt.Errorf("close binlog before roll: %w", err)
	}
	next := log.sequence + 1
	file, err := os.OpenFile(
		filepath.Join(log.directory, segmentName(next)), os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600,
	)
	if err != nil {
		return fmt.Errorf("open next binlog segment: %w", err)
	}
	log.file, log.sequence, log.written = file, next, 0
	return nil
}

// Append writes one transaction's frame and syncs it.
//
// It syncs before returning because the caller's next step is to mark the
// transaction committed: the write model orders the binlog write ahead of that
// mark so that a crash between them leaves a transaction that is recoverable
// rather than one that is claimed committed and cannot be replayed.
func (log *Log) Append(entry Entry) error {
	if log == nil {
		return fmt.Errorf("%w: binlog", ErrInvalid)
	}
	if entry.TransactionID == "" {
		return fmt.Errorf("%w: transaction ID is required", ErrInvalid)
	}
	if len(entry.Records) > maxRecords {
		return fmt.Errorf("%w: transaction holds %d records", ErrInvalid, len(entry.Records))
	}
	frame, err := encodeEntry(entry)
	if err != nil {
		return err
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	if log.closed {
		return ErrClosed
	}
	// Rolled before the write, not after, so a frame never straddles two files.
	if log.written > 0 && log.written+int64(len(frame)) > log.options.segmentBytes() {
		if err := log.rollLocked(); err != nil {
			return err
		}
	}
	if _, err := log.file.Write(frame); err != nil {
		return fmt.Errorf("append binlog frame: %w", err)
	}
	if err := log.file.Sync(); err != nil {
		return fmt.Errorf("sync binlog: %w", err)
	}
	log.written += int64(len(frame))
	return nil
}

// Replay reads every complete frame in order.
//
// A partial frame at the end is where a crash landed mid-append. It stops the
// replay without reporting an error: that transaction was never marked
// committed, so it is not part of the Database's history. A frame that is
// complete but does not check out is corruption and is reported.
func (log *Log) Replay(visit func(Entry) error) error {
	if log == nil || visit == nil {
		return fmt.Errorf("%w: binlog replay", ErrInvalid)
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	if log.closed {
		return ErrClosed
	}
	sequences, err := segments(log.directory)
	if err != nil {
		return err
	}
	for _, sequence := range sequences {
		if err := log.replaySegment(sequence, visit); err != nil {
			return err
		}
	}
	return nil
}

// replaySegment reads one file. The active segment is read through its own
// handle so an in-flight append is not missed; the rest are opened read-only.
func (log *Log) replaySegment(sequence uint64, visit func(Entry) error) error {
	var reader *bufio.Reader
	if sequence == log.sequence {
		if _, err := log.file.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("rewind binlog: %w", err)
		}
		reader = bufio.NewReader(log.file)
	} else {
		file, err := os.Open(filepath.Join(log.directory, segmentName(sequence)))
		if err != nil {
			return fmt.Errorf("open binlog segment: %w", err)
		}
		defer func() { _ = file.Close() }()
		reader = bufio.NewReader(file)
	}
	for {
		entry, err := decodeEntry(reader)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := visit(entry); err != nil {
			return err
		}
	}
}

func (log *Log) Close() error {
	if log == nil {
		return nil
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	if log.closed {
		return nil
	}
	log.closed = true
	return log.file.Close()
}

func encodeEntry(entry Entry) ([]byte, error) {
	body := make([]byte, 0, 64)
	body = binary.LittleEndian.AppendUint32(body, uint32(len(entry.TransactionID)))
	body = append(body, entry.TransactionID...)
	body = binary.LittleEndian.AppendUint32(body, uint32(len(entry.Records)))
	for _, record := range entry.Records {
		body = binary.LittleEndian.AppendUint16(body, record.Kind)
		body = binary.LittleEndian.AppendUint32(body, record.SchemaVersion)
		body = binary.LittleEndian.AppendUint32(body, uint32(len(record.ID)))
		body = append(body, record.ID...)
		body = binary.LittleEndian.AppendUint32(body, uint32(len(record.Payload)))
		body = append(body, record.Payload...)
	}
	if len(body) > maxFrameBytes {
		return nil, fmt.Errorf("%w: transaction frame is %d bytes", ErrInvalid, len(body))
	}
	frame := make([]byte, 0, 12+len(body))
	frame = binary.LittleEndian.AppendUint32(frame, frameMagic)
	frame = binary.LittleEndian.AppendUint32(frame, uint32(len(body)))
	frame = binary.LittleEndian.AppendUint32(frame, crc32.ChecksumIEEE(body))
	return append(frame, body...), nil
}

func decodeEntry(reader *bufio.Reader) (Entry, error) {
	header := make([]byte, 12)
	if _, err := io.ReadFull(reader, header); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			// A partial header is a torn append, which is a stopping point
			// rather than damage — see Replay.
			return Entry{}, io.EOF
		}
		return Entry{}, err
	}
	if binary.LittleEndian.Uint32(header[0:4]) != frameMagic {
		return Entry{}, fmt.Errorf("%w: frame magic", ErrCorrupt)
	}
	length := binary.LittleEndian.Uint32(header[4:8])
	if length > maxFrameBytes {
		return Entry{}, fmt.Errorf("%w: frame length %d", ErrCorrupt, length)
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(reader, body); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			return Entry{}, io.EOF
		}
		return Entry{}, err
	}
	if crc32.ChecksumIEEE(body) != binary.LittleEndian.Uint32(header[8:12]) {
		return Entry{}, fmt.Errorf("%w: frame checksum", ErrCorrupt)
	}
	return decodeBody(body)
}

func decodeBody(body []byte) (Entry, error) {
	cursor := &decoder{bytes: body}
	id, err := cursor.text()
	if err != nil {
		return Entry{}, err
	}
	count, err := cursor.u32()
	if err != nil {
		return Entry{}, err
	}
	if count > maxRecords {
		return Entry{}, fmt.Errorf("%w: record count %d", ErrCorrupt, count)
	}
	entry := Entry{TransactionID: id, Records: make([]Record, 0, count)}
	for index := uint32(0); index < count; index++ {
		kind, err := cursor.u16()
		if err != nil {
			return Entry{}, err
		}
		schema, err := cursor.u32()
		if err != nil {
			return Entry{}, err
		}
		recordID, err := cursor.text()
		if err != nil {
			return Entry{}, err
		}
		payload, err := cursor.blob()
		if err != nil {
			return Entry{}, err
		}
		entry.Records = append(entry.Records, Record{
			Kind: kind, SchemaVersion: schema, ID: recordID, Payload: payload,
		})
	}
	if cursor.offset != len(body) {
		return Entry{}, fmt.Errorf("%w: frame has %d trailing bytes", ErrCorrupt, len(body)-cursor.offset)
	}
	return entry, nil
}

type decoder struct {
	bytes  []byte
	offset int
}

func (d *decoder) u16() (uint16, error) {
	if d.offset+2 > len(d.bytes) {
		return 0, fmt.Errorf("%w: truncated frame", ErrCorrupt)
	}
	value := binary.LittleEndian.Uint16(d.bytes[d.offset:])
	d.offset += 2
	return value, nil
}

func (d *decoder) u32() (uint32, error) {
	if d.offset+4 > len(d.bytes) {
		return 0, fmt.Errorf("%w: truncated frame", ErrCorrupt)
	}
	value := binary.LittleEndian.Uint32(d.bytes[d.offset:])
	d.offset += 4
	return value, nil
}

func (d *decoder) blob() ([]byte, error) {
	length, err := d.u32()
	if err != nil {
		return nil, err
	}
	if d.offset+int(length) > len(d.bytes) {
		return nil, fmt.Errorf("%w: truncated frame", ErrCorrupt)
	}
	value := append([]byte(nil), d.bytes[d.offset:d.offset+int(length)]...)
	d.offset += int(length)
	return value, nil
}

func (d *decoder) text() (string, error) {
	value, err := d.blob()
	if err != nil {
		return "", err
	}
	return string(value), nil
}
