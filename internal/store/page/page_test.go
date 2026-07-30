package page

import (
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	t.Parallel()

	want := Page{
		Header: Header{
			Type:       TypeBTreeLeaf,
			SpaceID:    0x0102030405060708,
			PageID:     0x1112131415161718,
			Generation: 0x2122232425262728,
			LSN:        0x3132333435363738,
		},
		Payload: []byte("完整语义 Row locator"),
	}

	encoded, err := Encode(want)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	if len(encoded) != Size {
		t.Fatalf("len(Encode()) = %d, want %d", len(encoded), Size)
	}

	got, err := Decode(encoded)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if got.Header != want.Header || !bytes.Equal(got.Payload, want.Payload) {
		t.Fatalf("Decode() = %#v, want %#v", got, want)
	}

	encoded[HeaderSize] ^= 0xff
	if bytes.Equal(got.Payload, encoded[HeaderSize:HeaderSize+len(got.Payload)]) {
		t.Fatal("Decode() payload aliases encoded input")
	}
}

func TestEncodeGoldenPrefix(t *testing.T) {
	t.Parallel()

	encoded, err := Encode(Page{
		Header: Header{
			Type:       TypeData,
			SpaceID:    0x0102030405060708,
			PageID:     0x1112131415161718,
			Generation: 0x2122232425262728,
			LSN:        0x3132333435363738,
		},
		Payload: []byte("abc"),
	})
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}

	const wantPrefix = "4d454d4f50473031010040000100000008070605040302011817161514131211282726252423222138373635343332310300000000000000317a587500000000616263"
	if got := bytesToHex(encoded[:HeaderSize+3]); got != wantPrefix {
		t.Fatalf("golden prefix = %s, want %s", got, wantPrefix)
	}
	if !allZero(encoded[HeaderSize+3:]) {
		t.Fatal("Encode() did not zero unused Page bytes")
	}
}

func TestPageTypesRoundTrip(t *testing.T) {
	t.Parallel()

	for _, pageType := range []Type{
		TypeData,
		TypeBTreeInternal,
		TypeBTreeLeaf,
		TypeFree,
		TypeManifest,
		TypeOverflow,
	} {
		pageType := pageType
		t.Run(pageTypeName(pageType), func(t *testing.T) {
			t.Parallel()
			encoded, err := Encode(Page{Header: Header{Type: pageType}})
			if err != nil {
				t.Fatalf("Encode() error = %v", err)
			}
			got, err := Decode(encoded)
			if err != nil {
				t.Fatalf("Decode() error = %v", err)
			}
			if got.Header.Type != pageType {
				t.Fatalf("Decode().Header.Type = %d, want %d", got.Header.Type, pageType)
			}
		})
	}
}

func TestPayloadSizeBoundary(t *testing.T) {
	t.Parallel()

	want := bytes.Repeat([]byte{0xa5}, Size-HeaderSize)
	encoded, err := Encode(Page{
		Header:  Header{Type: TypeOverflow},
		Payload: want,
	})
	if err != nil {
		t.Fatalf("Encode(max payload) error = %v", err)
	}
	got, err := Decode(encoded)
	if err != nil {
		t.Fatalf("Decode(max payload) error = %v", err)
	}
	if !bytes.Equal(got.Payload, want) {
		t.Fatal("Decode(max payload) differs from input")
	}
}

func TestEncodeRejectsInvalidPage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value Page
	}{
		{name: "zero type", value: Page{}},
		{name: "unknown type", value: Page{Header: Header{Type: Type(99)}}},
		{name: "flags", value: Page{Header: Header{Type: TypeData, Flags: 1}}},
		{
			name: "payload too large",
			value: Page{
				Header:  Header{Type: TypeData},
				Payload: []byte(strings.Repeat("x", Size-HeaderSize+1)),
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := Encode(test.value); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Encode() error = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestDecodeRejectsInvalidLength(t *testing.T) {
	t.Parallel()

	for _, length := range []int{0, HeaderSize - 1, Size - 1, Size + 1} {
		if _, err := Decode(make([]byte, length)); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("Decode(%d bytes) error = %v, want ErrCorrupt", length, err)
		}
	}
}

func TestDecodeRejectsCorruption(t *testing.T) {
	t.Parallel()

	valid, err := Encode(Page{
		Header:  Header{Type: TypeManifest, SpaceID: 7, PageID: 9},
		Payload: []byte("manifest"),
	})
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}

	tests := []struct {
		name   string
		offset int
	}{
		{name: "magic", offset: 0},
		{name: "header size", offset: 10},
		{name: "type", offset: 12},
		{name: "flags", offset: 14},
		{name: "payload length", offset: 48},
		{name: "reserved one", offset: 52},
		{name: "checksum", offset: 56},
		{name: "reserved two", offset: 60},
		{name: "payload", offset: HeaderSize},
		{name: "unused tail", offset: Size - 1},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			corrupt := bytes.Clone(valid)
			corrupt[test.offset] ^= 0xff
			if _, err := Decode(corrupt); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("Decode() error = %v, want ErrCorrupt", err)
			}
		})
	}
}

func TestDecodeRejectsStructuralCorruptionWithValidChecksum(t *testing.T) {
	t.Parallel()

	valid, err := Encode(Page{
		Header:  Header{Type: TypeManifest, SpaceID: 7, PageID: 9},
		Payload: []byte("manifest"),
	})
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func([]byte)
	}{
		{
			name: "magic",
			mutate: func(encoded []byte) {
				encoded[0] ^= 0xff
			},
		},
		{
			name: "header size",
			mutate: func(encoded []byte) {
				binary.LittleEndian.PutUint16(encoded[10:12], HeaderSize-1)
			},
		},
		{
			name: "unknown type",
			mutate: func(encoded []byte) {
				binary.LittleEndian.PutUint16(encoded[12:14], 99)
			},
		},
		{
			name: "flags",
			mutate: func(encoded []byte) {
				binary.LittleEndian.PutUint16(encoded[14:16], 1)
			},
		},
		{
			name: "payload length",
			mutate: func(encoded []byte) {
				binary.LittleEndian.PutUint32(encoded[48:52], Size)
			},
		},
		{
			name: "reserved one",
			mutate: func(encoded []byte) {
				binary.LittleEndian.PutUint32(encoded[52:56], 1)
			},
		},
		{
			name: "reserved two",
			mutate: func(encoded []byte) {
				binary.LittleEndian.PutUint32(encoded[60:64], 1)
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			corrupt := bytes.Clone(valid)
			test.mutate(corrupt)
			rewriteChecksum(corrupt)
			if _, err := Decode(corrupt); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("Decode() error = %v, want ErrCorrupt", err)
			}
		})
	}
}

func TestDecodeRejectsUnsupportedVersion(t *testing.T) {
	t.Parallel()

	valid, err := Encode(Page{Header: Header{Type: TypeData}})
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	valid[8] = 2
	rewriteChecksum(valid)
	if _, err := Decode(valid); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("Decode() error = %v, want ErrUnsupportedVersion", err)
	}
}

func FuzzDecodeNeverPanics(f *testing.F) {
	valid, err := Encode(Page{Header: Header{Type: TypeData}, Payload: []byte("seed")})
	if err == nil {
		f.Add(valid)
	}
	f.Add([]byte{})
	f.Add(make([]byte, Size))

	f.Fuzz(func(t *testing.T, encoded []byte) {
		_, _ = Decode(encoded)
	})
}

func FuzzRoundTrip(f *testing.F) {
	f.Add(uint8(TypeData), []byte{})
	f.Add(uint8(TypeBTreeLeaf), []byte("semantic locator"))
	f.Add(uint8(TypeOverflow), bytes.Repeat([]byte{0xa5}, 1024))

	f.Fuzz(func(t *testing.T, encodedType uint8, payload []byte) {
		if len(payload) > Size-HeaderSize {
			t.Skip()
		}
		pageType := Type(encodedType%uint8(TypeOverflow)) + TypeData
		want := Page{
			Header: Header{
				Type:       pageType,
				SpaceID:    1,
				PageID:     2,
				Generation: 3,
				LSN:        4,
			},
			Payload: payload,
		}
		encoded, err := Encode(want)
		if err != nil {
			t.Fatalf("Encode() error = %v", err)
		}
		got, err := Decode(encoded)
		if err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		if got.Header != want.Header || !bytes.Equal(got.Payload, want.Payload) {
			t.Fatalf("round-trip got %#v, want %#v", got, want)
		}
	})
}

func pageTypeName(value Type) string {
	switch value {
	case TypeData:
		return "data"
	case TypeBTreeInternal:
		return "btree_internal"
	case TypeBTreeLeaf:
		return "btree_leaf"
	case TypeFree:
		return "free"
	case TypeManifest:
		return "manifest"
	case TypeOverflow:
		return "overflow"
	default:
		return "unknown"
	}
}

func bytesToHex(value []byte) string {
	const digits = "0123456789abcdef"
	encoded := make([]byte, len(value)*2)
	for index, item := range value {
		encoded[index*2] = digits[item>>4]
		encoded[index*2+1] = digits[item&0x0f]
	}
	return string(encoded)
}

func allZero(value []byte) bool {
	for _, item := range value {
		if item != 0 {
			return false
		}
	}
	return true
}
