package native

import (
	"bytes"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestPutCloseReopenGet(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "database.memora")
	file, err := Create(path, FileKindDatabase)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	want := []byte("原生 Row payload")
	if err := file.Put(ObjectKindOpaque, 1, "row_001", want); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })

	got, err := reopened.Get(ObjectKindOpaque, "row_001")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("Get() = %q, want %q", got, want)
	}
}

func TestMultipleRecordsAndDuplicateID(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "database.memora")
	file, err := Create(path, FileKindDatabase)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	t.Cleanup(func() { _ = file.Close() })

	if err := file.Put(ObjectKindOpaque, 1, "row_a", nil); err != nil {
		t.Fatalf("Put(row_a) error = %v", err)
	}
	if err := file.Put(ObjectKindOpaque, 1, "row_b", []byte("B")); err != nil {
		t.Fatalf("Put(row_b) error = %v", err)
	}
	if err := file.Put(ObjectKindOpaque, 1, "row_a", []byte("again")); !errors.Is(err, ErrDuplicateID) {
		t.Fatalf("Put(duplicate) error = %v, want ErrDuplicateID", err)
	}
}

func TestOpenRejectsCorruptPayload(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "database.memora")
	file, err := Create(path, FileKindDatabase)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := file.Put(ObjectKindOpaque, 1, "row_001", []byte("payload")); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	data[len(data)-1] ^= 0xff
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if _, err := Open(path); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Open(corrupt) error = %v, want ErrCorrupt", err)
	}
}

func TestHeaderEncodingGolden(t *testing.T) {
	t.Parallel()

	var uuid [16]byte
	for index := range uuid {
		uuid[index] = byte(index)
	}
	got := encodeFileHeader(fileHeader{kind: FileKindDatabase, uuid: uuid})
	want, err := hex.DecodeString(
		"4d454d4f524100000000200002000000000102030405060708090a0b0c0d0e0f",
	)
	if err != nil {
		t.Fatalf("DecodeString() error = %v", err)
	}
	if !bytes.Equal(got[:], want) {
		t.Fatalf("encodeFileHeader() = %x, want %x", got, want)
	}
}

func TestRecordHeaderEncodingGolden(t *testing.T) {
	t.Parallel()

	got := encodeRecordHeader(recordHeader{
		recordLength:  recordHeaderSize + 2,
		kind:          ObjectKindOpaque,
		schema:        1,
		idLength:      1,
		payloadLength: 1,
		payloadCRC:    0xd3d99e8b, // CRC-32 of "A".
	})
	want, err := hex.DecodeString("1a000000010000000100000001000000010000008b9ed9d3")
	if err != nil {
		t.Fatalf("DecodeString() error = %v", err)
	}
	if !bytes.Equal(got[:], want) {
		t.Fatalf("encodeRecordHeader() = %x, want %x", got, want)
	}
}

func TestOpenRejectsEveryTruncatedPrefix(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "source.memora")
	file, err := Create(path, FileKindDatabase)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := file.Put(ObjectKindOpaque, 1, "row_001", []byte("payload")); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	for length := 0; length < len(data); length++ {
		if length == fileHeaderSize {
			continue // A header-only file is a valid empty store.
		}
		length := length
		t.Run(strconv.Itoa(length), func(t *testing.T) {
			truncatedPath := filepath.Join(t.TempDir(), "truncated.memora")
			if err := os.WriteFile(truncatedPath, data[:length], 0o600); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
			opened, err := Open(truncatedPath)
			if opened != nil {
				_ = opened.Close()
			}
			if err == nil {
				t.Fatalf("Open(%d-byte prefix) unexpectedly succeeded", length)
			}
		})
	}
}

func TestOpenRejectsUnsupportedVersion(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "database.memora")
	file, err := Create(path, FileKindDatabase)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	data[8] = 1
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := Open(path); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("Open(unknown version) error = %v, want ErrUnsupportedVersion", err)
	}
}

func TestPutValidatesIdentityAndLimits(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "database.memora")
	file, err := Create(path, FileKindDatabase)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	t.Cleanup(func() { _ = file.Close() })

	tests := []struct {
		name    string
		kind    ObjectKind
		schema  uint32
		id      string
		payload []byte
	}{
		{name: "zero kind", schema: 1, id: "row"},
		{name: "zero schema", kind: ObjectKindOpaque, id: "row"},
		{name: "empty ID", kind: ObjectKindOpaque, schema: 1},
		{name: "invalid UTF-8", kind: ObjectKindOpaque, schema: 1, id: string([]byte{0xff})},
		{name: "long ID", kind: ObjectKindOpaque, schema: 1, id: string(bytes.Repeat([]byte{'a'}, maxIDLength+1))},
		{name: "large payload", kind: ObjectKindOpaque, schema: 1, id: "row", payload: make([]byte, maxPayloadLength+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := file.Put(test.kind, test.schema, test.id, test.payload); !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("Put() error = %v, want ErrInvalidArgument", err)
			}
		})
	}
}

func FuzzDecodeRecordHeader(f *testing.F) {
	valid := encodeRecordHeader(recordHeader{
		recordLength: recordHeaderSize + 1,
		kind:         ObjectKindOpaque,
		schema:       1,
		idLength:     1,
	})
	f.Add(valid[:])
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, encoded []byte) {
		_, _ = decodeRecordHeader(encoded)
	})
}
