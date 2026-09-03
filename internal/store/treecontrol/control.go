package treecontrol

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/HW-Yue/Memora/internal/store/page"
)

const (
	PageID          = uint64(1)
	FirstDataPageID = uint64(2)
	headerSize      = 40
	formatVersion   = uint16(3)
	legacyVersion   = uint16(2)

	// MaxFreePageIDs is how many reusable Page IDs the control Page can carry.
	//
	// The set is Tree state, so it is written here rather than rediscovered by
	// reading every Page at open. What does not fit is not lost: the count is
	// written as freePagesUnrecorded and the next open rebuilds the set by
	// scanning, exactly as every open used to.
	MaxFreePageIDs = (page.Size - page.HeaderSize - headerSize) / 8

	freePagesUnrecorded = ^uint32(0)
)

var (
	ErrCorrupt            = errors.New("Tree control Page is corrupt")
	ErrInvalid            = errors.New("Tree control state is invalid")
	ErrUnsupportedVersion = errors.New("Tree control version is unsupported")
	controlMagic          = [8]byte{'M', 'E', 'M', 'T', 'R', 'C', '0', '2'}
)

type State struct {
	SpaceID    uint64
	Generation uint64
	Revision   uint64
	RootPageID uint64
	NextPageID uint64
	LSN        uint64
}

func Bootstrap(spaceID uint64) State {
	return State{
		SpaceID: spaceID, Generation: 1, NextPageID: FirstDataPageID,
	}
}

// EncodeBootstrap encodes the control page of a brand-new Tree.
//
// It returns an error rather than panicking on one. The encoding of a
// Bootstrap state cannot fail today — that is why this used to panic — but a
// production panic is not a claim a caller can check, and "no panics outside
// tests" is only an invariant if it holds without exceptions to remember.
func EncodeBootstrap(spaceID uint64) (page.Page, error) {
	return Encode(Bootstrap(spaceID))
}

// Encode writes a control Page whose reusable-Page set is empty and recorded.
//
// It is for the states that have no reusable Pages by construction — a
// bootstrap, and the validation call sites that only ask whether a state can be
// encoded at all. A commit uses EncodeWithFreePages.
func Encode(value State) (page.Page, error) {
	return EncodeWithFreePages(value, nil)
}

// EncodeWithFreePages writes a control Page carrying the Tree's reusable Page
// IDs, which must be sorted ascending and free of duplicates.
//
// The set used to live only in memory, rebuilt at open by reading every Page in
// the space and asking each one whether it was free. Writing it here is what
// lets a Tree open by reading its control Page and stopping.
//
// A set too large for the Page is recorded as absent rather than truncated:
// dropping IDs would leak Pages that nothing would ever hand back, so the
// fallback is the old scan, which is slow but never loses one.
func EncodeWithFreePages(value State, freePageIDs []uint64) (page.Page, error) {
	if err := validatePersistentState(value); err != nil {
		return page.Page{}, err
	}
	if err := validateFreePageIDs(value, freePageIDs); err != nil {
		return page.Page{}, err
	}
	recorded := len(freePageIDs) <= MaxFreePageIDs
	count := len(freePageIDs)
	if !recorded {
		count = 0
	}
	payload := make([]byte, headerSize+count*8)
	copy(payload[:8], controlMagic[:])
	binary.LittleEndian.PutUint16(payload[8:10], formatVersion)
	binary.LittleEndian.PutUint16(payload[10:12], headerSize)
	if recorded {
		binary.LittleEndian.PutUint32(payload[12:16], uint32(count))
	} else {
		binary.LittleEndian.PutUint32(payload[12:16], freePagesUnrecorded)
	}
	binary.LittleEndian.PutUint64(payload[16:24], value.Revision)
	binary.LittleEndian.PutUint64(payload[24:32], value.RootPageID)
	binary.LittleEndian.PutUint64(payload[32:40], value.NextPageID)
	for index := 0; index < count; index++ {
		offset := headerSize + index*8
		binary.LittleEndian.PutUint64(payload[offset:offset+8], freePageIDs[index])
	}
	return page.Page{
		Header: page.Header{
			Type:       page.TypeTreeControl,
			SpaceID:    value.SpaceID,
			PageID:     PageID,
			Generation: value.Generation,
			LSN:        value.LSN,
		},
		Payload: payload,
	}, nil
}

func validateFreePageIDs(value State, freePageIDs []uint64) error {
	var previous uint64
	for index, pageID := range freePageIDs {
		if pageID < FirstDataPageID || pageID >= value.NextPageID || pageID == value.RootPageID ||
			(index > 0 && pageID <= previous) {
			return fmt.Errorf("%w: reusable Page IDs", ErrInvalid)
		}
		previous = pageID
	}
	return nil
}

func Decode(value page.Page, expectedSpaceID uint64) (State, error) {
	if value.Header.Type != page.TypeTreeControl ||
		value.Header.Flags != 0 ||
		value.Header.SpaceID != expectedSpaceID ||
		value.Header.PageID != PageID {
		return State{}, fmt.Errorf("%w: identity", ErrCorrupt)
	}
	if len(value.Payload) < headerSize ||
		!bytes.Equal(value.Payload[:8], controlMagic[:]) {
		return State{}, fmt.Errorf("%w: payload", ErrCorrupt)
	}
	version := binary.LittleEndian.Uint16(value.Payload[8:10])
	if version != formatVersion && version != legacyVersion {
		return State{}, fmt.Errorf("%w: got %d", ErrUnsupportedVersion, version)
	}
	if binary.LittleEndian.Uint16(value.Payload[10:12]) != headerSize {
		return State{}, fmt.Errorf("%w: payload fields", ErrCorrupt)
	}
	if _, _, err := decodeFreePages(value.Payload, version); err != nil {
		return State{}, err
	}
	state := State{
		SpaceID:    expectedSpaceID,
		Generation: value.Header.Generation,
		Revision:   binary.LittleEndian.Uint64(value.Payload[16:24]),
		RootPageID: binary.LittleEndian.Uint64(value.Payload[24:32]),
		NextPageID: binary.LittleEndian.Uint64(value.Payload[32:40]),
		LSN:        value.Header.LSN,
	}
	if err := validatePersistentState(state); err != nil {
		return State{}, fmt.Errorf("%w: state", ErrCorrupt)
	}
	return state, nil
}

// DecodeFreePages reads the reusable Page IDs a control Page carries.
//
// recorded is false when the Page does not hold the set — a Tree written before
// the set was persisted, or one whose set was too large for the Page. The caller
// then has to rebuild it the old way, by reading every Page in the space.
func DecodeFreePages(value page.Page) (ids []uint64, recorded bool, err error) {
	if len(value.Payload) < headerSize || !bytes.Equal(value.Payload[:8], controlMagic[:]) {
		return nil, false, fmt.Errorf("%w: payload", ErrCorrupt)
	}
	return decodeFreePages(value.Payload, binary.LittleEndian.Uint16(value.Payload[8:10]))
}

func decodeFreePages(payload []byte, version uint16) (ids []uint64, recorded bool, err error) {
	if version == legacyVersion {
		// Version 2 has a reserved word here and no set to read.
		if binary.LittleEndian.Uint32(payload[12:16]) != 0 || len(payload) != headerSize {
			return nil, false, fmt.Errorf("%w: payload fields", ErrCorrupt)
		}
		return nil, false, nil
	}
	count := binary.LittleEndian.Uint32(payload[12:16])
	if count == freePagesUnrecorded {
		if len(payload) != headerSize {
			return nil, false, fmt.Errorf("%w: unrecorded reusable Page count", ErrCorrupt)
		}
		return nil, false, nil
	}
	if int(count) > MaxFreePageIDs || len(payload) != headerSize+int(count)*8 {
		return nil, false, fmt.Errorf("%w: reusable Page count", ErrCorrupt)
	}
	result := make([]uint64, 0, count)
	var previous uint64
	for index := 0; index < int(count); index++ {
		offset := headerSize + index*8
		pageID := binary.LittleEndian.Uint64(payload[offset : offset+8])
		if pageID < FirstDataPageID || (index > 0 && pageID <= previous) {
			return nil, false, fmt.Errorf("%w: reusable Page IDs", ErrCorrupt)
		}
		previous = pageID
		result = append(result, pageID)
	}
	return result, true, nil
}

func validatePersistentState(value State) error {
	if value.Generation == 1 &&
		value.Revision == 0 &&
		value.RootPageID == 0 &&
		value.NextPageID == FirstDataPageID &&
		value.LSN == 0 {
		return nil
	}
	if value.Generation == 0 ||
		value.Revision == 0 ||
		value.RootPageID < FirstDataPageID ||
		value.NextPageID <= value.RootPageID ||
		value.LSN == 0 {
		return fmt.Errorf("%w: fields", ErrInvalid)
	}
	return nil
}
