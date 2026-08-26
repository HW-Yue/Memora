package objectlock

import (
	"bytes"
	"errors"
	"math/rand"
	"sort"
	"testing"
)

func TestKeyCanonicalCodecGoldenAndCollisionCorpus(t *testing.T) {
	row, _ := RowKey("db", "tbl", "row")
	tests := []struct {
		key  Key
		want []byte
	}{
		{row, []byte{0, 2, 'd', 'b', 0, 3, 't', 'b', 'l', 0, 3, 'r', 'o', 'w'}},
	}
	for _, test := range tests {
		if got := test.key.canonical(); !bytes.Equal(got, test.want) {
			t.Fatalf("canonical(%s) = %v, want %v", test.key, got, test.want)
		}
	}
	// Length-prefixed components, so a separator appearing inside one of them
	// cannot make two different keys encode the same.
	components := []string{"a", "aa", "a/b", "a\x00b", "数据库", "route:one"}
	seen := make(map[string]Key)
	for _, databaseID := range components {
		for _, tableID := range components {
			for _, rowID := range components {
				key, err := RowKey(databaseID, tableID, rowID)
				if err != nil {
					t.Fatal(err)
				}
				encoded := string(key.canonical())
				if prior, collision := seen[encoded]; collision && prior != key {
					t.Fatalf("canonical collision: %s and %s", prior, key)
				}
				seen[encoded] = key
			}
		}
	}
}

func TestManagerMatchesSeededBatchReferenceModel(t *testing.T) {
	const ownerCount = 12
	manager := New()
	guards := make([]*Guard, ownerCount)
	owned := make([]map[Key]struct{}, ownerCount)
	for owner := range guards {
		guards[owner], _ = manager.Begin(uint64(owner + 1))
		owned[owner] = make(map[Key]struct{})
	}
	keys := make([]Key, 0, 60)
	for number := 0; number < 20; number++ {
		for _, tableID := range []string{"table", "other", "third"} {
			key, _ := RowKey("db", tableID, string(rune('a'+number)))
			keys = append(keys, key)
		}
	}
	holders := make(map[Key]int)
	random := rand.New(rand.NewSource(104))
	for step := 0; step < 5000; step++ {
		owner := random.Intn(ownerCount)
		if step != 0 && step%97 == 0 {
			if err := guards[owner].Release(); err != nil {
				t.Fatal(err)
			}
			for key := range owned[owner] {
				delete(holders, key)
			}
			owned[owner] = make(map[Key]struct{})
			guards[owner], _ = manager.Begin(uint64(owner + 1))
			continue
		}
		batch := make([]Key, 1+random.Intn(5))
		for index := range batch {
			batch[index] = keys[random.Intn(len(keys))]
		}
		unique := make(map[Key]struct{})
		for _, key := range batch {
			unique[key] = struct{}{}
		}
		ordered := make([]Key, 0, len(unique))
		for key := range unique {
			ordered = append(ordered, key)
		}
		sort.Slice(ordered, func(left, right int) bool {
			return bytes.Compare(ordered[left].canonical(), ordered[right].canonical()) < 0
		})
		var blocked *Key
		for _, key := range ordered {
			if holder, exists := holders[key]; exists && holder != owner {
				candidate := key
				blocked = &candidate
				break
			}
		}
		err := guards[owner].TryAcquire(t.Context(), batch...)
		if blocked != nil {
			var conflict *ConflictError
			if !errors.As(err, &conflict) || conflict.Blocked != *blocked {
				t.Fatalf("step %d conflict = %#v, %v; want %s", step, conflict, err, *blocked)
			}
			continue
		}
		if err != nil {
			t.Fatalf("step %d TryAcquire() error = %v", step, err)
		}
		for _, key := range ordered {
			holders[key] = owner
			owned[owner][key] = struct{}{}
		}
	}
}
