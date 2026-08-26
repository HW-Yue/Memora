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
	payloadSize     = 40
	formatVersion   = uint16(2)
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

func Encode(value State) (page.Page, error) {
	if err := validatePersistentState(value); err != nil {
		return page.Page{}, err
	}
	payload := make([]byte, payloadSize)
	copy(payload[:8], controlMagic[:])
	binary.LittleEndian.PutUint16(payload[8:10], formatVersion)
	binary.LittleEndian.PutUint16(payload[10:12], payloadSize)
	binary.LittleEndian.PutUint64(payload[16:24], value.Revision)
	binary.LittleEndian.PutUint64(payload[24:32], value.RootPageID)
	binary.LittleEndian.PutUint64(payload[32:40], value.NextPageID)
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

func Decode(value page.Page, expectedSpaceID uint64) (State, error) {
	if value.Header.Type != page.TypeTreeControl ||
		value.Header.Flags != 0 ||
		value.Header.SpaceID != expectedSpaceID ||
		value.Header.PageID != PageID {
		return State{}, fmt.Errorf("%w: identity", ErrCorrupt)
	}
	if len(value.Payload) != payloadSize ||
		!bytes.Equal(value.Payload[:8], controlMagic[:]) {
		return State{}, fmt.Errorf("%w: payload", ErrCorrupt)
	}
	if version := binary.LittleEndian.Uint16(value.Payload[8:10]); version != formatVersion {
		return State{}, fmt.Errorf("%w: got %d", ErrUnsupportedVersion, version)
	}
	if binary.LittleEndian.Uint16(value.Payload[10:12]) != payloadSize ||
		binary.LittleEndian.Uint32(value.Payload[12:16]) != 0 {
		return State{}, fmt.Errorf("%w: payload fields", ErrCorrupt)
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
