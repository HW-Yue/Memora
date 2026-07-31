package btree

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand"
	"slices"
	"testing"

	"github.com/HW-Yue/Memora/internal/store/page"
)

func TestUpsertLeafInsertReplaceAndDeepCopy(t *testing.T) {
	header := page.Header{Type: page.TypeBTreeLeaf, SpaceID: 7, PageID: 1}
	node := Node{Kind: KindLeaf, NextLeafPageID: 9, LeafEntries: []LeafEntry{
		{Key: []byte("b"), Value: []byte("2")},
		{Key: []byte("d"), Value: []byte("4")},
	}}
	for _, test := range []struct {
		key, value string
		replaced   bool
		wantKeys   []string
	}{
		{"a", "1", false, []string{"a", "b", "d"}},
		{"c", "3", false, []string{"b", "c", "d"}},
		{"e", "5", false, []string{"b", "d", "e"}},
		{"b", "new", true, []string{"b", "d"}},
	} {
		t.Run(test.key, func(t *testing.T) {
			key, value := []byte(test.key), []byte(test.value)
			got, replaced, err := UpsertLeaf(header, node, key, value)
			if err != nil {
				t.Fatal(err)
			}
			if replaced != test.replaced || got.NextLeafPageID != 9 {
				t.Fatalf("replaced/next = %v/%d", replaced, got.NextLeafPageID)
			}
			assertLeafKeys(t, got, test.wantKeys)
			key[0], value[0] = 'X', 'X'
			if got.LeafEntries[0].Key[0] == 'X' {
				t.Fatal("result aliases input key")
			}
			got.LeafEntries[0].Key[0] = 'Y'
			if string(node.LeafEntries[0].Key) != "b" {
				t.Fatal("result aliases original Node")
			}
		})
	}
}

func TestUpsertInternalInsertReplaceAndMetadata(t *testing.T) {
	header := page.Header{Type: page.TypeBTreeInternal, SpaceID: 7, PageID: 2}
	node := Node{Kind: KindInternal, Level: 2, LeftmostChild: 3, InternalEntries: []InternalEntry{
		{Key: []byte("m"), RightChild: 8},
		{Key: []byte("z"), RightChild: 9},
	}}
	inserted, replaced, err := UpsertInternal(header, node, []byte("t"), 10)
	if err != nil || replaced {
		t.Fatalf("insert = %+v, %v/%v", inserted, replaced, err)
	}
	if inserted.Level != 2 || inserted.LeftmostChild != 3 ||
		string(inserted.InternalEntries[1].Key) != "t" || inserted.InternalEntries[1].RightChild != 10 {
		t.Fatalf("inserted = %+v", inserted)
	}
	updated, replaced, err := UpsertInternal(header, node, []byte("m"), 11)
	if err != nil || !replaced || updated.InternalEntries[0].RightChild != 11 {
		t.Fatalf("replace = %+v, %v/%v", updated, replaced, err)
	}
	if node.InternalEntries[0].RightChild != 8 {
		t.Fatal("original internal Node changed")
	}
}

func TestUpsertRejectsInvalidAndNoSpaceAtomically(t *testing.T) {
	leafHeader := page.Header{Type: page.TypeBTreeLeaf, PageID: 1}
	internalHeader := page.Header{Type: page.TypeBTreeInternal, PageID: 2}
	leaf := Node{Kind: KindLeaf, LeafEntries: []LeafEntry{{Key: []byte("a"), Value: []byte("1")}}}
	internal := Node{Kind: KindInternal, Level: 1, LeftmostChild: 3}
	for name, run := range map[string]func() error{
		"empty key":    func() error { _, _, err := UpsertLeaf(leafHeader, leaf, nil, []byte("v")); return err },
		"empty value":  func() error { _, _, err := UpsertLeaf(leafHeader, leaf, []byte("b"), nil); return err },
		"wrong kind":   func() error { _, _, err := UpsertLeaf(leafHeader, internal, []byte("b"), []byte("v")); return err },
		"wrong header": func() error { _, _, err := UpsertLeaf(internalHeader, leaf, []byte("b"), []byte("v")); return err },
		"zero child":   func() error { _, _, err := UpsertInternal(internalHeader, internal, []byte("b"), 0); return err },
	} {
		t.Run(name, func(t *testing.T) {
			if err := run(); !errors.Is(err, ErrInvalid) {
				t.Fatalf("error = %v, want ErrInvalid", err)
			}
		})
	}

	full := Node{Kind: KindLeaf, LeafEntries: []LeafEntry{{
		Key: []byte("a"), Value: make([]byte, 16263),
	}}}
	before, err := Encode(leafHeader, full)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := UpsertLeaf(leafHeader, full, []byte("b"), []byte("2")); !errors.Is(err, ErrNoSpace) {
		t.Fatalf("Upsert full error = %v, want ErrNoSpace", err)
	}
	after, err := Encode(leafHeader, full)
	if err != nil || !bytes.Equal(before.Payload, after.Payload) {
		t.Fatalf("full input changed after failure: %v", err)
	}
}

func TestUpsertLeafMatchesSeededReferenceModel(t *testing.T) {
	const seed = int64(9301)
	random := rand.New(rand.NewSource(seed))
	header := page.Header{Type: page.TypeBTreeLeaf, SpaceID: 7, PageID: 1}
	node := Node{Kind: KindLeaf}
	model := make(map[string]string)
	for step := 0; step < 500; step++ {
		key := fmt.Sprintf("k%02d", random.Intn(50))
		value := fmt.Sprintf("v%03d", random.Intn(1000))
		_, existed := model[key]
		next, replaced, err := UpsertLeaf(header, node, []byte(key), []byte(value))
		if err != nil {
			t.Fatalf("seed=%d step=%d: %v", seed, step, err)
		}
		if replaced != existed {
			t.Fatalf("seed=%d step=%d replaced=%v want %v", seed, step, replaced, existed)
		}
		model[key] = value
		node = next
		encoded, err := Encode(header, node)
		if err != nil {
			t.Fatalf("seed=%d step=%d encode: %v", seed, step, err)
		}
		node, err = Decode(encoded)
		if err != nil {
			t.Fatalf("seed=%d step=%d decode: %v", seed, step, err)
		}
		assertLeafModel(t, node, model, seed, step)
	}
}

func assertLeafKeys(t *testing.T, node Node, want []string) {
	t.Helper()
	got := make([]string, len(node.LeafEntries))
	for index := range node.LeafEntries {
		got[index] = string(node.LeafEntries[index].Key)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}
}

func assertLeafModel(t *testing.T, node Node, model map[string]string, seed int64, step int) {
	t.Helper()
	keys := make([]string, 0, len(model))
	for key := range model {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	if len(node.LeafEntries) != len(keys) {
		t.Fatalf("seed=%d step=%d entries=%d want %d", seed, step, len(node.LeafEntries), len(keys))
	}
	for index, key := range keys {
		if string(node.LeafEntries[index].Key) != key ||
			string(node.LeafEntries[index].Value) != model[key] {
			t.Fatalf("seed=%d step=%d entry %d mismatch", seed, step, index)
		}
	}
}
