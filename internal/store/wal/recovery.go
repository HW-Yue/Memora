package wal

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/HW-Yue/Memora/internal/store/page"
	"github.com/HW-Yue/Memora/internal/store/treecontrol"
)

const (
	pageDeltaHeaderSize = 16
	pageDeltaVersion    = uint16(1)
)

var (
	ErrMissingSpace    = errors.New("WAL recovery Page Store is missing")
	ErrNeedsPageImage  = errors.New("WAL recovery requires a full Page image")
	ErrUnsupportedRedo = errors.New("WAL redo type is unsupported")
	pageDeltaMagic     = [4]byte{'M', 'D', 'E', 'L'}
)

type PageStore interface {
	Read(pageID uint64) (page.Page, error)
	Write(value page.Page) error
	Sync() error
}

type RecoveryReport struct {
	Transactions    uint64
	PageWrites      uint64
	ControlWrites   uint64
	BootstrapWrites uint64
	SkippedRecords  uint64
	LastCommitLSN   uint64
}

type recoveryPageKey struct {
	spaceID uint64
	pageID  uint64
}

type stagedPage struct {
	store     PageStore
	value     page.Page
	dirty     bool
	control   bool
	bootstrap bool
}

func EncodePageDelta(offset uint32, replacement []byte) ([]byte, error) {
	if len(replacement) == 0 || uint64(len(replacement)) > math.MaxUint32 ||
		uint64(offset)+uint64(len(replacement)) > math.MaxUint32 {
		return nil, fmt.Errorf("%w: Page delta range", ErrInvalid)
	}
	encoded := make([]byte, pageDeltaHeaderSize+len(replacement))
	copy(encoded[0:4], pageDeltaMagic[:])
	binary.LittleEndian.PutUint16(encoded[4:6], pageDeltaVersion)
	binary.LittleEndian.PutUint16(encoded[6:8], pageDeltaHeaderSize)
	binary.LittleEndian.PutUint32(encoded[8:12], offset)
	binary.LittleEndian.PutUint32(encoded[12:16], uint32(len(replacement)))
	copy(encoded[pageDeltaHeaderSize:], replacement)
	return encoded, nil
}

func Recover(
	segment *Segment,
	spaces map[uint64]PageStore,
) (RecoveryReport, error) {
	transactions, err := ScanCommitted(segment)
	if err != nil {
		return RecoveryReport{}, err
	}
	return recoverTransactions(transactions, spaces)
}

func recoverTransactions(
	transactions []CommittedTransaction,
	spaces map[uint64]PageStore,
) (RecoveryReport, error) {
	var report RecoveryReport
	for _, transaction := range transactions {
		staged, touched, skipped, err := stageTransaction(transaction, spaces)
		if err != nil {
			return report, err
		}
		report.SkippedRecords += skipped
		for _, key := range bootstrapPageKeys(staged) {
			state := staged[key]
			bootstrap, err := treecontrol.EncodeBootstrap(key.spaceID)
			if err != nil {
				return report, fmt.Errorf("encode bootstrap Tree control: %w", err)
			}
			if err := state.store.Write(bootstrap); err != nil {
				return report, fmt.Errorf(
					"bootstrap transaction %d Tree control %d: %w",
					transaction.Receipt.TransactionID,
					key.spaceID,
					err,
				)
			}
			report.BootstrapWrites++
			if err := state.store.Sync(); err != nil {
				return report, fmt.Errorf(
					"sync bootstrap transaction %d space %d: %w",
					transaction.Receipt.TransactionID,
					key.spaceID,
					err,
				)
			}
		}
		dirtyKeys := dirtyPageKeys(staged)
		controlIndex := len(dirtyKeys)
		for index, key := range dirtyKeys {
			if staged[key].control {
				controlIndex = index
				break
			}
			state := staged[key]
			if err := state.store.Write(state.value); err != nil {
				return report, fmt.Errorf(
					"recover transaction %d Page %d/%d: %w",
					transaction.Receipt.TransactionID,
					key.spaceID,
					key.pageID,
					err,
				)
			}
			report.PageWrites++
		}
		if controlIndex < len(dirtyKeys) {
			for _, spaceID := range sortedSpaceIDs(touched) {
				if err := touched[spaceID].Sync(); err != nil {
					return report, fmt.Errorf(
						"sync transaction %d Pages before Tree control in space %d: %w",
						transaction.Receipt.TransactionID,
						spaceID,
						err,
					)
				}
			}
		}
		for _, key := range dirtyKeys[controlIndex:] {
			state := staged[key]
			if err := state.store.Write(state.value); err != nil {
				return report, fmt.Errorf(
					"recover transaction %d Page %d/%d: %w",
					transaction.Receipt.TransactionID,
					key.spaceID,
					key.pageID,
					err,
				)
			}
			report.ControlWrites++
		}
		for _, spaceID := range sortedSpaceIDs(touched) {
			if err := touched[spaceID].Sync(); err != nil {
				return report, fmt.Errorf(
					"sync recovered transaction %d space %d: %w",
					transaction.Receipt.TransactionID,
					spaceID,
					err,
				)
			}
		}
		report.Transactions++
		report.LastCommitLSN = transaction.Receipt.CommitLSN
	}
	return report, nil
}

func stageTransaction(
	transaction CommittedTransaction,
	spaces map[uint64]PageStore,
) (map[recoveryPageKey]stagedPage, map[uint64]PageStore, uint64, error) {
	staged := make(map[recoveryPageKey]stagedPage)
	touched := make(map[uint64]PageStore)
	initialized := make(map[recoveryPageKey]page.Type)
	freed := make(map[recoveryPageKey]struct{})
	metadataPlans, err := parseTreeMetadata(transaction.Records)
	if err != nil {
		return nil, nil, 0, err
	}
	var skipped uint64
	for _, record := range transaction.Records {
		if record.Type == TypeRoot || record.Type == TypeAllocator {
			continue
		}
		if record.Type != TypePageInit &&
			record.Type != TypeFullPageImage &&
			record.Type != TypePageDelta {
			return nil, nil, 0, fmt.Errorf("%w: type %d", ErrCorrupt, record.Type)
		}
		if record.PageID == 0 {
			return nil, nil, 0, fmt.Errorf("%w: zero Page ID", ErrCorrupt)
		}
		store, exists := spaces[record.SpaceID]
		if !exists || store == nil {
			return nil, nil, 0, fmt.Errorf("%w: space %d", ErrMissingSpace, record.SpaceID)
		}
		var image page.Page
		var deltaOffset uint32
		var replacement []byte
		var err error
		switch record.Type {
		case TypePageInit, TypeFullPageImage:
			image, err = decodePageImage(record)
		case TypePageDelta:
			deltaOffset, replacement, err = decodePageDelta(record.Payload)
		}
		if err != nil {
			return nil, nil, 0, err
		}
		touched[record.SpaceID] = store
		key := recoveryPageKey{spaceID: record.SpaceID, pageID: record.PageID}
		if record.Type == TypePageInit {
			initialized[key] = image.Header.Type
		}
		if (record.Type == TypePageInit || record.Type == TypeFullPageImage) &&
			image.Header.Type == page.TypeFree {
			freed[key] = struct{}{}
		}
		if (record.Type == TypePageInit || record.Type == TypeFullPageImage) &&
			image.Header.Type == page.TypeTreeControl {
			return nil, nil, 0, fmt.Errorf("%w: ordinary Tree control Page redo", ErrCorrupt)
		}
		state, loaded := staged[key]
		var readErr error
		if !loaded {
			state.store = store
			state.value, readErr = store.Read(record.PageID)
			if readErr != nil &&
				!errors.Is(readErr, page.ErrNotFound) &&
				!errors.Is(readErr, page.ErrCorrupt) {
				return nil, nil, 0, fmt.Errorf(
					"read recovery Page %d/%d: %w",
					record.SpaceID,
					record.PageID,
					readErr,
				)
			}
			if readErr == nil &&
				(state.value.Header.SpaceID != record.SpaceID ||
					state.value.Header.PageID != record.PageID) {
				return nil, nil, 0, fmt.Errorf("%w: Page Store identity", ErrCorrupt)
			}
		}
		if readErr == nil && state.value.Header.LSN >= record.LSN {
			skipped++
			staged[key] = state
			continue
		}

		switch record.Type {
		case TypePageInit, TypeFullPageImage:
			state.value = image
			state.dirty = true
		case TypePageDelta:
			if readErr != nil {
				return nil, nil, 0, fmt.Errorf(
					"%w: space %d Page %d",
					ErrNeedsPageImage,
					record.SpaceID,
					record.PageID,
				)
			}
			end := uint64(deltaOffset) + uint64(len(replacement))
			if end > uint64(len(state.value.Payload)) {
				return nil, nil, 0, fmt.Errorf("%w: Page delta exceeds Payload", ErrCorrupt)
			}
			state.value.Payload = bytes.Clone(state.value.Payload)
			copy(state.value.Payload[int(deltaOffset):int(end)], replacement)
			state.value.Header.LSN = record.LSN
			state.dirty = true
		}
		staged[key] = state
	}
	// One plan per Tree in the transaction. They are staged after every Page
	// redo so each Tree's metadata sees the complete staged image.
	for _, plan := range metadataPlans {
		metadataSkipped, err := stageTreeMetadata(
			plan,
			spaces,
			staged,
			touched,
			initialized,
			freed,
		)
		if err != nil {
			return nil, nil, 0, err
		}
		skipped += metadataSkipped
	}
	return staged, touched, skipped, nil
}

func decodePageImage(record Record) (page.Page, error) {
	image, err := page.Decode(record.Payload)
	if err != nil {
		return page.Page{}, fmt.Errorf("%w: invalid Page image: %v", ErrCorrupt, err)
	}
	if image.Header.SpaceID != record.SpaceID ||
		image.Header.PageID != record.PageID ||
		image.Header.LSN != 0 {
		return page.Page{}, fmt.Errorf("%w: Page image identity or LSN", ErrCorrupt)
	}
	image.Header.LSN = record.LSN
	return image, nil
}

func decodePageDelta(encoded []byte) (uint32, []byte, error) {
	if len(encoded) < pageDeltaHeaderSize ||
		!bytes.Equal(encoded[0:4], pageDeltaMagic[:]) ||
		binary.LittleEndian.Uint16(encoded[4:6]) != pageDeltaVersion ||
		binary.LittleEndian.Uint16(encoded[6:8]) != pageDeltaHeaderSize {
		return 0, nil, fmt.Errorf("%w: invalid Page delta header", ErrCorrupt)
	}
	length := binary.LittleEndian.Uint32(encoded[12:16])
	if length == 0 || uint64(pageDeltaHeaderSize)+uint64(length) != uint64(len(encoded)) {
		return 0, nil, fmt.Errorf("%w: invalid Page delta length", ErrCorrupt)
	}
	return binary.LittleEndian.Uint32(encoded[8:12]),
		bytes.Clone(encoded[pageDeltaHeaderSize:]),
		nil
}

func dirtyPageKeys(staged map[recoveryPageKey]stagedPage) []recoveryPageKey {
	keys := make([]recoveryPageKey, 0, len(staged))
	for key, state := range staged {
		if state.dirty {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(left, right int) bool {
		leftControl := staged[keys[left]].control
		rightControl := staged[keys[right]].control
		if leftControl != rightControl {
			return !leftControl
		}
		if keys[left].spaceID != keys[right].spaceID {
			return keys[left].spaceID < keys[right].spaceID
		}
		return keys[left].pageID < keys[right].pageID
	})
	return keys
}

func bootstrapPageKeys(staged map[recoveryPageKey]stagedPage) []recoveryPageKey {
	keys := make([]recoveryPageKey, 0)
	for key, state := range staged {
		if state.bootstrap {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(left, right int) bool {
		return keys[left].spaceID < keys[right].spaceID
	})
	return keys
}

func sortedSpaceIDs(spaces map[uint64]PageStore) []uint64 {
	spaceIDs := make([]uint64, 0, len(spaces))
	for spaceID := range spaces {
		spaceIDs = append(spaceIDs, spaceID)
	}
	sort.Slice(spaceIDs, func(left, right int) bool {
		return spaceIDs[left] < spaceIDs[right]
	})
	return spaceIDs
}
