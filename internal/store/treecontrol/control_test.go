package treecontrol

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math/rand"
	"testing"

	"github.com/HW-Yue/Memora/internal/store/page"
)

func TestControlCodecGoldenAndRoundTrip(t *testing.T) {
	state := State{
		SpaceID: 7, Generation: 3, RootPageID: 9, NextPageID: 12, LSN: 44,
	}
	value, err := Encode(state)
	if err != nil {
		t.Fatal(err)
	}
	if value.Header != (page.Header{
		Type: page.TypeTreeControl, SpaceID: 7, PageID: 1, Generation: 3, LSN: 44,
	}) {
		t.Fatalf("Header = %#v", value.Header)
	}
	wantPayload := make([]byte, payloadSize)
	copy(wantPayload[:8], "MEMTRC01")
	binary.LittleEndian.PutUint16(wantPayload[8:10], 1)
	binary.LittleEndian.PutUint16(wantPayload[10:12], payloadSize)
	binary.LittleEndian.PutUint64(wantPayload[16:24], 9)
	binary.LittleEndian.PutUint64(wantPayload[24:32], 12)
	if !bytes.Equal(value.Payload, wantPayload) {
		t.Fatalf("Payload = %x", value.Payload)
	}
	decoded, err := Decode(value, 7)
	if err != nil || decoded != state {
		t.Fatalf("Decode() = %#v, %v", decoded, err)
	}
	value.Payload[0] ^= 0xff
	if decoded.RootPageID != 9 {
		t.Fatal("Decode result aliases input")
	}
}

func TestBootstrapControlRoundTrip(t *testing.T) {
	value := EncodeBootstrap(7)
	decoded, err := Decode(value, 7)
	if err != nil || decoded != Bootstrap(7) {
		t.Fatalf("Decode(bootstrap) = %#v, %v", decoded, err)
	}
}

func TestControlCodecRejectsInvalidStateAndCorruption(t *testing.T) {
	for name, state := range map[string]State{
		"zero generation": {SpaceID: 7, RootPageID: 2, NextPageID: 3, LSN: 1},
		"zero root":       {SpaceID: 7, Generation: 1, NextPageID: 3, LSN: 1},
		"root at next":    {SpaceID: 7, Generation: 1, RootPageID: 3, NextPageID: 3, LSN: 1},
		"zero LSN":        {SpaceID: 7, Generation: 1, RootPageID: 2, NextPageID: 3},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Encode(state); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Encode() error = %v", err)
			}
		})
	}

	valid, err := Encode(State{
		SpaceID: 7, Generation: 1, RootPageID: 2, NextPageID: 3, LSN: 9,
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*page.Page)
		want   error
	}{
		{"type", func(value *page.Page) { value.Header.Type = page.TypeManifest }, ErrCorrupt},
		{"space", func(value *page.Page) { value.Header.SpaceID++ }, ErrCorrupt},
		{"Page", func(value *page.Page) { value.Header.PageID++ }, ErrCorrupt},
		{"flags", func(value *page.Page) { value.Header.Flags = 1 }, ErrCorrupt},
		{"generation", func(value *page.Page) { value.Header.Generation = 0 }, ErrCorrupt},
		{"LSN", func(value *page.Page) { value.Header.LSN = 0 }, ErrCorrupt},
		{"magic", func(value *page.Page) { value.Payload[0] ^= 1 }, ErrCorrupt},
		{"version", func(value *page.Page) {
			binary.LittleEndian.PutUint16(value.Payload[8:10], 2)
		}, ErrUnsupportedVersion},
		{"size", func(value *page.Page) {
			binary.LittleEndian.PutUint16(value.Payload[10:12], 31)
		}, ErrCorrupt},
		{"reserved", func(value *page.Page) { value.Payload[12] = 1 }, ErrCorrupt},
		{"root", func(value *page.Page) {
			binary.LittleEndian.PutUint64(value.Payload[16:24], 1)
		}, ErrCorrupt},
		{"high-water", func(value *page.Page) {
			binary.LittleEndian.PutUint64(value.Payload[24:32], 2)
		}, ErrCorrupt},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := valid
			value.Payload = bytes.Clone(valid.Payload)
			test.mutate(&value)
			if _, err := Decode(value, 7); !errors.Is(err, test.want) {
				t.Fatalf("Decode() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestControlCodecCorruptionSeed(t *testing.T) {
	valid, err := Encode(State{
		SpaceID: 7, Generation: 1, RootPageID: 2, NextPageID: 3, LSN: 9,
	})
	if err != nil {
		t.Fatal(err)
	}
	const seed = int64(9703)
	random := rand.New(rand.NewSource(seed))
	offsets := []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	for trial := 0; trial < 128; trial++ {
		value := valid
		value.Payload = bytes.Clone(valid.Payload)
		offset := offsets[random.Intn(len(offsets))]
		value.Payload[offset] ^= byte(1 + random.Intn(255))
		if _, err := Decode(value, 7); err == nil {
			t.Fatalf("seed=%d trial=%d offset=%d accepted corruption", seed, trial, offset)
		}
	}
}
