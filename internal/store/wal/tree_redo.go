package wal

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"slices"

	"github.com/HW-Yue/Memora/internal/store/treecontrol"
)

const (
	rootRedoSize        = 56
	allocatorRedoHeader = 48
	treeRedoVersion     = uint16(1)
)

var (
	rootRedoMagic      = [4]byte{'M', 'R', 'O', 'T'}
	allocatorRedoMagic = [4]byte{'M', 'A', 'L', 'L'}
)

type RootRedo struct {
	ExpectedGeneration uint64
	Generation         uint64
	ExpectedRootPageID uint64
	RootPageID         uint64
	ExpectedNextPageID uint64
}

type AllocatorRedo struct {
	ExpectedGeneration uint64
	ExpectedNextPageID uint64
	NextPageID         uint64
	RetiredPageIDs     []uint64
}

func EncodeRootRedo(value RootRedo) ([]byte, error) {
	if err := validateRootRedo(value, ErrInvalid); err != nil {
		return nil, err
	}
	encoded := make([]byte, rootRedoSize)
	copy(encoded[:4], rootRedoMagic[:])
	binary.LittleEndian.PutUint16(encoded[4:6], treeRedoVersion)
	binary.LittleEndian.PutUint16(encoded[6:8], rootRedoSize)
	binary.LittleEndian.PutUint64(encoded[16:24], value.ExpectedGeneration)
	binary.LittleEndian.PutUint64(encoded[24:32], value.Generation)
	binary.LittleEndian.PutUint64(encoded[32:40], value.ExpectedRootPageID)
	binary.LittleEndian.PutUint64(encoded[40:48], value.RootPageID)
	binary.LittleEndian.PutUint64(encoded[48:56], value.ExpectedNextPageID)
	return encoded, nil
}

func EncodeAllocatorRedo(value AllocatorRedo) ([]byte, error) {
	if err := validateAllocatorRedo(value, ErrInvalid); err != nil {
		return nil, err
	}
	if uint64(len(value.RetiredPageIDs)) > math.MaxUint32 ||
		len(value.RetiredPageIDs) > (math.MaxInt-allocatorRedoHeader)/8 {
		return nil, fmt.Errorf("%w: allocator retired Page count", ErrInvalid)
	}
	encoded := make([]byte, allocatorRedoHeader+len(value.RetiredPageIDs)*8)
	copy(encoded[:4], allocatorRedoMagic[:])
	binary.LittleEndian.PutUint16(encoded[4:6], treeRedoVersion)
	binary.LittleEndian.PutUint16(encoded[6:8], allocatorRedoHeader)
	binary.LittleEndian.PutUint64(encoded[16:24], value.ExpectedGeneration)
	binary.LittleEndian.PutUint64(encoded[24:32], value.ExpectedNextPageID)
	binary.LittleEndian.PutUint64(encoded[32:40], value.NextPageID)
	binary.LittleEndian.PutUint32(encoded[40:44], uint32(len(value.RetiredPageIDs)))
	for index, pageID := range value.RetiredPageIDs {
		binary.LittleEndian.PutUint64(encoded[allocatorRedoHeader+index*8:], pageID)
	}
	return encoded, nil
}

func decodeRootRedo(encoded []byte) (RootRedo, error) {
	if len(encoded) != rootRedoSize ||
		!bytes.Equal(encoded[:4], rootRedoMagic[:]) ||
		binary.LittleEndian.Uint16(encoded[6:8]) != rootRedoSize ||
		!allZero(encoded[8:16]) {
		return RootRedo{}, fmt.Errorf("%w: root redo payload", ErrCorrupt)
	}
	if version := binary.LittleEndian.Uint16(encoded[4:6]); version != treeRedoVersion {
		return RootRedo{}, fmt.Errorf("%w: root redo version %d", ErrUnsupportedRedo, version)
	}
	value := RootRedo{
		ExpectedGeneration: binary.LittleEndian.Uint64(encoded[16:24]),
		Generation:         binary.LittleEndian.Uint64(encoded[24:32]),
		ExpectedRootPageID: binary.LittleEndian.Uint64(encoded[32:40]),
		RootPageID:         binary.LittleEndian.Uint64(encoded[40:48]),
		ExpectedNextPageID: binary.LittleEndian.Uint64(encoded[48:56]),
	}
	if err := validateRootRedo(value, ErrCorrupt); err != nil {
		return RootRedo{}, err
	}
	return value, nil
}

func decodeAllocatorRedo(encoded []byte) (AllocatorRedo, error) {
	if len(encoded) < allocatorRedoHeader ||
		!bytes.Equal(encoded[:4], allocatorRedoMagic[:]) ||
		binary.LittleEndian.Uint16(encoded[6:8]) != allocatorRedoHeader ||
		!allZero(encoded[8:16]) ||
		binary.LittleEndian.Uint32(encoded[44:48]) != 0 {
		return AllocatorRedo{}, fmt.Errorf("%w: allocator redo payload", ErrCorrupt)
	}
	if version := binary.LittleEndian.Uint16(encoded[4:6]); version != treeRedoVersion {
		return AllocatorRedo{}, fmt.Errorf("%w: allocator redo version %d", ErrUnsupportedRedo, version)
	}
	count := binary.LittleEndian.Uint32(encoded[40:44])
	if uint64(allocatorRedoHeader)+uint64(count)*8 != uint64(len(encoded)) {
		return AllocatorRedo{}, fmt.Errorf("%w: allocator retired Page count", ErrCorrupt)
	}
	value := AllocatorRedo{
		ExpectedGeneration: binary.LittleEndian.Uint64(encoded[16:24]),
		ExpectedNextPageID: binary.LittleEndian.Uint64(encoded[24:32]),
		NextPageID:         binary.LittleEndian.Uint64(encoded[32:40]),
		RetiredPageIDs:     make([]uint64, count),
	}
	for index := range value.RetiredPageIDs {
		value.RetiredPageIDs[index] = binary.LittleEndian.Uint64(
			encoded[allocatorRedoHeader+index*8:],
		)
	}
	if err := validateAllocatorRedo(value, ErrCorrupt); err != nil {
		return AllocatorRedo{}, err
	}
	return value, nil
}

func validateRootRedo(value RootRedo, class error) error {
	if value.ExpectedGeneration == math.MaxUint64 ||
		value.Generation != value.ExpectedGeneration+1 ||
		value.RootPageID < treecontrol.FirstDataPageID ||
		value.ExpectedNextPageID < treecontrol.FirstDataPageID ||
		(value.ExpectedGeneration == 0 && value.ExpectedRootPageID != 0) ||
		(value.ExpectedGeneration != 0 &&
			(value.ExpectedRootPageID < treecontrol.FirstDataPageID ||
				value.ExpectedRootPageID >= value.ExpectedNextPageID)) {
		return fmt.Errorf("%w: root redo fields", class)
	}
	return nil
}

func validateAllocatorRedo(value AllocatorRedo, class error) error {
	if value.ExpectedGeneration == math.MaxUint64 ||
		value.ExpectedNextPageID < treecontrol.FirstDataPageID ||
		value.NextPageID < value.ExpectedNextPageID ||
		(value.NextPageID == value.ExpectedNextPageID &&
			len(value.RetiredPageIDs) == 0) {
		return fmt.Errorf("%w: allocator redo fields", class)
	}
	if !slices.IsSorted(value.RetiredPageIDs) {
		return fmt.Errorf("%w: allocator retired Page order", class)
	}
	var previous uint64
	for index, pageID := range value.RetiredPageIDs {
		if pageID < treecontrol.FirstDataPageID ||
			pageID >= value.ExpectedNextPageID ||
			(index > 0 && pageID == previous) {
			return fmt.Errorf("%w: allocator retired Page identity", class)
		}
		previous = pageID
	}
	return nil
}
