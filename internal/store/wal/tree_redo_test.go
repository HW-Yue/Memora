package wal

import (
	"encoding/binary"
	"errors"
	"reflect"
	"testing"
)

func TestTreeRedoCodecRoundTripAndValidation(t *testing.T) {
	root := RootRedo{
		ExpectedRevision: 2, Revision: 3,
		ExpectedRootPageID: 4, RootPageID: 6, ExpectedNextPageID: 5,
	}
	encodedRoot, err := EncodeRootRedo(root)
	if err != nil {
		t.Fatal(err)
	}
	decodedRoot, err := decodeRootRedo(encodedRoot)
	if err != nil || decodedRoot != root {
		t.Fatalf("root round trip = %#v, %v", decodedRoot, err)
	}
	allocator := AllocatorRedo{
		ExpectedRevision: 2, ExpectedNextPageID: 5, NextPageID: 7,
		RetiredPageIDs: []uint64{2, 4},
	}
	encodedAllocator, err := EncodeAllocatorRedo(allocator)
	if err != nil {
		t.Fatal(err)
	}
	decodedAllocator, err := decodeAllocatorRedo(encodedAllocator)
	if err != nil || !reflect.DeepEqual(decodedAllocator, allocator) {
		t.Fatalf("allocator round trip = %#v, %v", decodedAllocator, err)
	}
	encodedAllocator[48] ^= 0xff
	if decodedAllocator.RetiredPageIDs[0] != 2 {
		t.Fatal("decoded allocator aliases encoded payload")
	}

	invalidRoots := []RootRedo{
		{},
		{ExpectedRevision: ^uint64(0), Revision: 0, RootPageID: 2, ExpectedNextPageID: 3},
		{ExpectedRevision: 1, Revision: 3, ExpectedRootPageID: 2, RootPageID: 2, ExpectedNextPageID: 3},
		{ExpectedRevision: 1, Revision: 2, ExpectedRootPageID: 3, RootPageID: 2, ExpectedNextPageID: 3},
	}
	for index, value := range invalidRoots {
		if _, err := EncodeRootRedo(value); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid root %d error = %v", index, err)
		}
	}
	invalidAllocators := []AllocatorRedo{
		{},
		{ExpectedRevision: ^uint64(0), ExpectedNextPageID: 2, NextPageID: 3},
		{ExpectedNextPageID: 2, NextPageID: 2},
		{ExpectedNextPageID: 4, NextPageID: 3},
		{ExpectedNextPageID: 5, NextPageID: 5, RetiredPageIDs: []uint64{4, 3}},
		{ExpectedNextPageID: 5, NextPageID: 5, RetiredPageIDs: []uint64{2, 2}},
		{ExpectedNextPageID: 5, NextPageID: 5, RetiredPageIDs: []uint64{5}},
	}
	for index, value := range invalidAllocators {
		if _, err := EncodeAllocatorRedo(value); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid allocator %d error = %v", index, err)
		}
	}

	unknownRoot := cloneRedoBytes(encodedRoot)
	binary.LittleEndian.PutUint16(unknownRoot[4:6], treeRedoVersion+1)
	if _, err := decodeRootRedo(unknownRoot); !errors.Is(err, ErrUnsupportedRedo) {
		t.Fatalf("unknown root version error = %v", err)
	}
	badAllocator := cloneRedoBytes(encodedAllocator)
	binary.LittleEndian.PutUint32(badAllocator[40:44], 99)
	if _, err := decodeAllocatorRedo(badAllocator); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("bad allocator count error = %v", err)
	}
}

func TestTreeRedoCodecRejectsCorruptPayload(t *testing.T) {
	root, err := EncodeRootRedo(RootRedo{
		ExpectedRevision: 1, Revision: 2,
		ExpectedRootPageID: 2, RootPageID: 3, ExpectedNextPageID: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	allocator, err := EncodeAllocatorRedo(AllocatorRedo{
		ExpectedRevision: 1, ExpectedNextPageID: 3, NextPageID: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		value  []byte
		mutate func([]byte) []byte
		decode func([]byte) error
		want   error
	}{
		{
			name: "root magic", value: root, want: ErrCorrupt,
			mutate: func(value []byte) []byte { value[0] ^= 1; return value },
			decode: func(value []byte) error { _, err := decodeRootRedo(value); return err },
		},
		{
			name: "root header size", value: root, want: ErrCorrupt,
			mutate: func(value []byte) []byte {
				binary.LittleEndian.PutUint16(value[6:8], rootRedoSize-1)
				return value
			},
			decode: func(value []byte) error { _, err := decodeRootRedo(value); return err },
		},
		{
			name: "root reserved", value: root, want: ErrCorrupt,
			mutate: func(value []byte) []byte { value[8] = 1; return value },
			decode: func(value []byte) error { _, err := decodeRootRedo(value); return err },
		},
		{
			name: "root truncated", value: root, want: ErrCorrupt,
			mutate: func(value []byte) []byte { return value[:len(value)-1] },
			decode: func(value []byte) error { _, err := decodeRootRedo(value); return err },
		},
		{
			name: "allocator magic", value: allocator, want: ErrCorrupt,
			mutate: func(value []byte) []byte { value[0] ^= 1; return value },
			decode: func(value []byte) error { _, err := decodeAllocatorRedo(value); return err },
		},
		{
			name: "allocator header size", value: allocator, want: ErrCorrupt,
			mutate: func(value []byte) []byte {
				binary.LittleEndian.PutUint16(value[6:8], allocatorRedoHeader-1)
				return value
			},
			decode: func(value []byte) error { _, err := decodeAllocatorRedo(value); return err },
		},
		{
			name: "allocator version", value: allocator, want: ErrUnsupportedRedo,
			mutate: func(value []byte) []byte {
				binary.LittleEndian.PutUint16(value[4:6], treeRedoVersion+1)
				return value
			},
			decode: func(value []byte) error { _, err := decodeAllocatorRedo(value); return err },
		},
		{
			name: "allocator reserved", value: allocator, want: ErrCorrupt,
			mutate: func(value []byte) []byte { value[44] = 1; return value },
			decode: func(value []byte) error { _, err := decodeAllocatorRedo(value); return err },
		},
		{
			name: "allocator count", value: allocator, want: ErrCorrupt,
			mutate: func(value []byte) []byte {
				binary.LittleEndian.PutUint32(value[40:44], 1)
				return value
			},
			decode: func(value []byte) error { _, err := decodeAllocatorRedo(value); return err },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := test.mutate(cloneRedoBytes(test.value))
			if err := test.decode(value); !errors.Is(err, test.want) {
				t.Fatalf("decode error = %v, want %v", err, test.want)
			}
		})
	}
}

func cloneRedoBytes(value []byte) []byte {
	return append([]byte(nil), value...)
}
