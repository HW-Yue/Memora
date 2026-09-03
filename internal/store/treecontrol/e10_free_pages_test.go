package treecontrol

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/HW-Yue/Memora/internal/store/page"
)

func freeState() State {
	return State{SpaceID: 7, Generation: 3, Revision: 7, RootPageID: 9, NextPageID: 200, LSN: 44}
}

// TestControlPageCarriesTheReusablePageSet pins what moved onto disk.
//
// The set used to be rebuilt at open by reading every Page in the space and
// asking each one whether it was free, which made opening a Tree cost the size
// of its Page file. It is Tree state, so it rides in the control Page with the
// rest of it and a round trip has to return it exactly.
func TestControlPageCarriesTheReusablePageSet(t *testing.T) {
	state := freeState()
	free := []uint64{5, 11, 12, 130}

	value, err := EncodeWithFreePages(state, free)
	if err != nil {
		t.Fatal(err)
	}
	if len(value.Payload) != headerSize+len(free)*8 {
		t.Fatalf("payload length = %d, want header plus one word per Page", len(value.Payload))
	}
	decoded, err := Decode(value, 7)
	if err != nil || decoded != state {
		t.Fatalf("Decode() = %#v, %v", decoded, err)
	}
	ids, recorded, err := DecodeFreePages(value)
	if err != nil || !recorded {
		t.Fatalf("DecodeFreePages() recorded = %v, err = %v", recorded, err)
	}
	if len(ids) != len(free) {
		t.Fatalf("DecodeFreePages() = %v, want %v", ids, free)
	}
	for index := range free {
		if ids[index] != free[index] {
			t.Fatalf("DecodeFreePages() = %v, want %v", ids, free)
		}
	}

	// Encode without a set is the empty set, recorded — not "unknown".
	empty, err := Encode(state)
	if err != nil {
		t.Fatal(err)
	}
	if ids, recorded, err := DecodeFreePages(empty); err != nil || !recorded || len(ids) != 0 {
		t.Fatalf("DecodeFreePages(empty) = %v, %v, %v", ids, recorded, err)
	}
}

// TestAnOversizedReusableSetIsRecordedAsAbsent pins the overflow rule.
//
// More reusable Pages than the control Page can hold must not be truncated:
// dropping IDs leaks Pages that nothing would ever hand back, and the file
// would grow forever with space it already owned. The set is marked absent
// instead, which sends the next open back to the scan — slow, but it cannot
// lose a Page.
func TestAnOversizedReusableSetIsRecordedAsAbsent(t *testing.T) {
	state := freeState()
	state.NextPageID = uint64(MaxFreePageIDs) + FirstDataPageID + 10
	state.RootPageID = state.NextPageID - 1

	free := make([]uint64, 0, MaxFreePageIDs+1)
	for pageID := FirstDataPageID; len(free) <= MaxFreePageIDs; pageID++ {
		free = append(free, pageID)
	}

	value, err := EncodeWithFreePages(state, free)
	if err != nil {
		t.Fatal(err)
	}
	if len(value.Payload) != headerSize {
		t.Fatalf("payload length = %d, want the bare header", len(value.Payload))
	}
	if _, recorded, err := DecodeFreePages(value); err != nil || recorded {
		t.Fatalf("DecodeFreePages() recorded = %v, err = %v, want an absent set", recorded, err)
	}
	if decoded, err := Decode(value, 7); err != nil || decoded != state {
		t.Fatalf("Decode() = %#v, %v", decoded, err)
	}
}

// TestAControlPageWrittenBeforeTheSetExistedStillOpens pins the upgrade path.
//
// Version 2 control Pages are on disk in every Database written before this,
// and they carry no reusable set. They must decode, and report the set as
// absent so the Runtime rebuilds it by scanning exactly once; the next commit
// writes version 3 and the scan is never paid again.
func TestAControlPageWrittenBeforeTheSetExistedStillOpens(t *testing.T) {
	state := freeState()
	legacy := make([]byte, headerSize)
	copy(legacy[:8], controlMagic[:])
	binary.LittleEndian.PutUint16(legacy[8:10], legacyVersion)
	binary.LittleEndian.PutUint16(legacy[10:12], headerSize)
	binary.LittleEndian.PutUint64(legacy[16:24], state.Revision)
	binary.LittleEndian.PutUint64(legacy[24:32], state.RootPageID)
	binary.LittleEndian.PutUint64(legacy[32:40], state.NextPageID)
	value := page.Page{
		Header: page.Header{
			Type: page.TypeTreeControl, SpaceID: state.SpaceID, PageID: PageID,
			Generation: state.Generation, LSN: state.LSN,
		},
		Payload: legacy,
	}

	decoded, err := Decode(value, 7)
	if err != nil || decoded != state {
		t.Fatalf("Decode(version 2) = %#v, %v", decoded, err)
	}
	ids, recorded, err := DecodeFreePages(value)
	if err != nil || recorded || ids != nil {
		t.Fatalf("DecodeFreePages(version 2) = %v, %v, %v; want an absent set", ids, recorded, err)
	}
}

// TestAReusableSetThatDisagreesWithTheStateIsRefused pins the checks that keep
// a corrupt Page from being read as a valid one.
func TestAReusableSetThatDisagreesWithTheStateIsRefused(t *testing.T) {
	state := freeState()
	for _, test := range []struct {
		name string
		free []uint64
	}{
		{"below the first data Page", []uint64{PageID}},
		{"the live root", []uint64{state.RootPageID}},
		{"beyond the high-water mark", []uint64{state.NextPageID}},
		{"out of order", []uint64{11, 5}},
		{"duplicated", []uint64{5, 5}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := EncodeWithFreePages(state, test.free); !errors.Is(err, ErrInvalid) {
				t.Fatalf("EncodeWithFreePages(%v) error = %v, want ErrInvalid", test.free, err)
			}
		})
	}

	valid, err := EncodeWithFreePages(state, []uint64{5, 11})
	if err != nil {
		t.Fatal(err)
	}
	truncated := page.Page{Header: valid.Header, Payload: bytes.Clone(valid.Payload[:headerSize+8])}
	if _, _, err := DecodeFreePages(truncated); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("DecodeFreePages(truncated) error = %v, want ErrCorrupt", err)
	}
}
