package btree

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/HW-Yue/Memora/internal/store/page"
)

func TestLeafGoldenAndRoundTrip(t *testing.T) {
	header := page.Header{Type: page.TypeBTreeLeaf, SpaceID: 7, PageID: 11, Generation: 3, LSN: 9}
	want := Node{Kind: KindLeaf, NextLeafPageID: 12, LeafEntries: []LeafEntry{
		{Key: []byte("a"), Value: []byte("one")},
		{Key: []byte("bb"), Value: []byte("two")},
	}}
	encoded, err := Encode(header, want)
	if err != nil {
		t.Fatal(err)
	}
	if encoded.Header != header || len(encoded.Payload) != page.Size-page.HeaderSize {
		t.Fatalf("encoded Page = %+v payload=%d", encoded.Header, len(encoded.Payload))
	}
	if got := encoded.Payload[:48]; !bytes.Equal(got, leafGoldenHeader()) {
		t.Fatalf("header = %x\nwant   = %x", got, leafGoldenHeader())
	}
	assertSlot(t, encoded.Payload, 0, 16316, 4, 1)
	assertSlot(t, encoded.Payload, 1, 16311, 5, 2)
	if got := string(encoded.Payload[16311:]); got != "bbtwoaone" {
		t.Fatalf("record tail = %q", got)
	}
	got, err := Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	assertNodeEqual(t, got, want)
	reencoded, err := Encode(header, got)
	if err != nil || !bytes.Equal(reencoded.Payload, encoded.Payload) {
		t.Fatalf("re-encode differs: %v", err)
	}
}

func TestInternalGoldenRoundTripAndDeepCopy(t *testing.T) {
	header := page.Header{Type: page.TypeBTreeInternal, SpaceID: 7, PageID: 20}
	want := Node{Kind: KindInternal, Level: 2, LeftmostChild: 4, InternalEntries: []InternalEntry{
		{Key: []byte("m"), RightChild: 8},
		{Key: []byte("z"), RightChild: 9},
	}}
	encoded, err := Encode(header, want)
	if err != nil {
		t.Fatal(err)
	}
	want.InternalEntries[0].Key[0] = 'x'
	got, err := Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != KindInternal || got.Level != 2 || got.LeftmostChild != 4 ||
		string(got.InternalEntries[0].Key) != "m" || got.InternalEntries[1].RightChild != 9 {
		t.Fatalf("decoded = %+v", got)
	}
	got.InternalEntries[0].Key[0] = 'q'
	again, err := Decode(encoded)
	if err != nil || string(again.InternalEntries[0].Key) != "m" {
		t.Fatalf("Decode alias: %+v %v", again, err)
	}
}

func TestEmptyLeafAndInternalZeroSeparator(t *testing.T) {
	for _, test := range []struct {
		header page.Header
		node   Node
	}{
		{page.Header{Type: page.TypeBTreeLeaf, PageID: 1}, Node{Kind: KindLeaf}},
		{page.Header{Type: page.TypeBTreeInternal, PageID: 2}, Node{Kind: KindInternal, Level: 1, LeftmostChild: 7}},
	} {
		encoded, err := Encode(test.header, test.node)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Decode(encoded)
		if err != nil {
			t.Fatal(err)
		}
		assertNodeEqual(t, got, test.node)
	}
}

func TestEncodeRejectsInvalidNodesAndCapacity(t *testing.T) {
	leaf := page.Header{Type: page.TypeBTreeLeaf, PageID: 1}
	internal := page.Header{Type: page.TypeBTreeInternal, PageID: 2}
	for name, test := range map[string]struct {
		header page.Header
		node   Node
		want   error
	}{
		"type mismatch":  {internal, Node{Kind: KindLeaf}, ErrInvalid},
		"leaf level":     {leaf, Node{Kind: KindLeaf, Level: 1}, ErrInvalid},
		"leaf child":     {leaf, Node{Kind: KindLeaf, LeftmostChild: 1}, ErrInvalid},
		"internal level": {internal, Node{Kind: KindInternal, LeftmostChild: 1}, ErrInvalid},
		"internal next":  {internal, Node{Kind: KindInternal, Level: 1, LeftmostChild: 1, NextLeafPageID: 2}, ErrInvalid},
		"zero child":     {internal, Node{Kind: KindInternal, Level: 1}, ErrInvalid},
		"empty key":      {leaf, Node{Kind: KindLeaf, LeafEntries: []LeafEntry{{Value: []byte("v")}}}, ErrInvalid},
		"empty value":    {leaf, Node{Kind: KindLeaf, LeafEntries: []LeafEntry{{Key: []byte("k")}}}, ErrInvalid},
		"duplicate":      {leaf, leafNode("a", "a"), ErrInvalid},
		"unordered":      {leaf, leafNode("b", "a"), ErrInvalid},
		"mixed entries":  {leaf, Node{Kind: KindLeaf, InternalEntries: []InternalEntry{{Key: []byte("a"), RightChild: 1}}}, ErrInvalid},
		"too large":      {leaf, Node{Kind: KindLeaf, LeafEntries: []LeafEntry{{Key: []byte("k"), Value: make([]byte, page.Size)}}}, ErrNoSpace},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Encode(test.header, test.node); !errors.Is(err, test.want) {
				t.Fatalf("Encode() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestLeafCapacityBoundary(t *testing.T) {
	header := page.Header{Type: page.TypeBTreeLeaf, PageID: 1}
	value := make([]byte, 16263)
	if _, err := Encode(header, Node{Kind: KindLeaf, LeafEntries: []LeafEntry{{Key: []byte("k"), Value: value}}}); err != nil {
		t.Fatalf("exact capacity Encode() error = %v", err)
	}
	value = append(value, 0)
	if _, err := Encode(header, Node{Kind: KindLeaf, LeafEntries: []LeafEntry{{Key: []byte("k"), Value: value}}}); !errors.Is(err, ErrNoSpace) {
		t.Fatalf("over capacity Encode() error = %v, want ErrNoSpace", err)
	}
}

func TestDecodeRejectsCorruptionCorpus(t *testing.T) {
	valid, err := Encode(page.Header{Type: page.TypeBTreeLeaf, PageID: 1}, leafNode("a", "b"))
	if err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func([]byte){
		"magic":           func(value []byte) { value[0] ^= 1 },
		"version":         func(value []byte) { binary.LittleEndian.PutUint16(value[8:10], 2) },
		"kind":            func(value []byte) { binary.LittleEndian.PutUint16(value[10:12], 2) },
		"flags":           func(value []byte) { value[14] = 1 },
		"count":           func(value []byte) { binary.LittleEndian.PutUint32(value[16:20], 9999) },
		"slot reserved":   func(value []byte) { value[54] = 1 },
		"header reserved": func(value []byte) { value[44] = 1 },
		"offset":          func(value []byte) { binary.LittleEndian.PutUint16(value[48:50], 47) },
		"length":          func(value []byte) { binary.LittleEndian.PutUint16(value[50:52], 0) },
		"key length":      func(value []byte) { binary.LittleEndian.PutUint16(value[52:54], 99) },
		"free byte":       func(value []byte) { value[100] = 1 },
		"record gap":      func(value []byte) { binary.LittleEndian.PutUint16(value[56:58], 16315) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			corrupt := clonePage(valid)
			mutate(corrupt.Payload)
			if _, err := Decode(corrupt); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("Decode() error = %v, want ErrCorrupt", err)
			}
		})
	}
	raw, err := page.Encode(valid)
	if err != nil {
		t.Fatal(err)
	}
	for _, offset := range []int{0, 7, 8, 15, 20, 40, 48, 55, 1000, len(raw) - 1} {
		corrupt := bytes.Clone(raw)
		corrupt[offset] ^= 0x80
		if _, err := page.Decode(corrupt); !errors.Is(err, page.ErrCorrupt) {
			t.Fatalf("raw bit flip at %d error = %v, want page.ErrCorrupt", offset, err)
		}
	}
}

func leafGoldenHeader() []byte {
	result := make([]byte, 48)
	copy(result, []byte("MEMBTN01"))
	binary.LittleEndian.PutUint16(result[8:10], 1)
	binary.LittleEndian.PutUint16(result[10:12], uint16(KindLeaf))
	binary.LittleEndian.PutUint32(result[16:20], 2)
	binary.LittleEndian.PutUint16(result[20:22], 8)
	binary.LittleEndian.PutUint16(result[22:24], 48)
	binary.LittleEndian.PutUint64(result[32:40], 12)
	binary.LittleEndian.PutUint16(result[40:42], 64)
	binary.LittleEndian.PutUint16(result[42:44], 16311)
	return result
}

func leafNode(keys ...string) Node {
	result := Node{Kind: KindLeaf}
	for _, key := range keys {
		result.LeafEntries = append(result.LeafEntries, LeafEntry{Key: []byte(key), Value: []byte("v")})
	}
	return result
}

func assertSlot(t *testing.T, payload []byte, index, offset, length, keyLength int) {
	t.Helper()
	start := 48 + index*8
	if int(binary.LittleEndian.Uint16(payload[start:start+2])) != offset ||
		int(binary.LittleEndian.Uint16(payload[start+2:start+4])) != length ||
		int(binary.LittleEndian.Uint16(payload[start+4:start+6])) != keyLength ||
		binary.LittleEndian.Uint16(payload[start+6:start+8]) != 0 {
		t.Fatalf("slot %d = %x", index, payload[start:start+8])
	}
}

func assertNodeEqual(t *testing.T, got, want Node) {
	t.Helper()
	if got.Kind != want.Kind || got.Level != want.Level || got.NextLeafPageID != want.NextLeafPageID ||
		got.LeftmostChild != want.LeftmostChild || len(got.LeafEntries) != len(want.LeafEntries) ||
		len(got.InternalEntries) != len(want.InternalEntries) {
		t.Fatalf("Node = %+v, want %+v", got, want)
	}
	for index := range got.LeafEntries {
		if !bytes.Equal(got.LeafEntries[index].Key, want.LeafEntries[index].Key) ||
			!bytes.Equal(got.LeafEntries[index].Value, want.LeafEntries[index].Value) {
			t.Fatalf("leaf entry %d differs", index)
		}
	}
	for index := range got.InternalEntries {
		if !bytes.Equal(got.InternalEntries[index].Key, want.InternalEntries[index].Key) ||
			got.InternalEntries[index].RightChild != want.InternalEntries[index].RightChild {
			t.Fatalf("internal entry %d differs", index)
		}
	}
}

func clonePage(value page.Page) page.Page {
	value.Payload = bytes.Clone(value.Payload)
	return value
}
