package nativekv

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/HW-Yue/Memora/internal/store"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

func seedEntries(t *testing.T, path string, entries int) store.Store {
	t.Helper()
	ctx := context.Background()
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 256)
	for base := 0; base < entries; base += 200 {
		tx, err := database.Begin(ctx, store.ReadWrite)
		if err != nil {
			t.Fatal(err)
		}
		for index := base; index < base+200 && index < entries; index++ {
			if err := tx.Put(ctx, "traces", fmt.Sprintf("key-%06d", index), payload); err != nil {
				t.Fatal(err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	return database
}

// TestOpeningAStoreDoesNotLoadEveryValue is the gate for this Store's resident
// index.
//
// Open decoded every record in the file into a map holding every key and every
// payload — the whole Store, in the process, for its whole life, with no
// capacity and no eviction. The two Stores it backs (auxiliary.memora and
// security.memora) are written on every call and route traces grow without
// bound, so the map grew with how much the instance had ever recorded.
//
// The committed state now lives in a B+ Tree beside the log and is read on
// demand, so this Store's own cost of opening must not scale with what it
// holds.
//
// What it measures is the difference between opening the Store and opening the
// record log underneath it, because the log still builds a resident index of
// its own (nativestore.File.records — one entry per record ever written). At
// 6000 entries that index is about 1.0 MiB and this Store's own share is about
// 46 KiB. Charging the log's index to this package would make the gate fail for
// something this package does not own and cannot fix; that is E9 stages 2-4,
// tracked separately. Subtracting it is what makes the assertion honest.
func TestOpeningAStoreDoesNotLoadEveryValue(t *testing.T) {
	measure := func(entries int) float64 {
		path := filepath.Join(t.TempDir(), "auxiliary.memora")
		seeded := seedEntries(t, path, entries)
		if err := seeded.Close(); err != nil {
			t.Fatal(err)
		}

		log := retained(t, func() func() error {
			opened, err := nativestore.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			return opened.Close
		})
		whole := retained(t, func() func() error {
			opened, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			return opened.Close
		})
		return whole - log
	}

	small := measure(200)
	large := measure(6000)
	t.Logf("本包自己的开库常驻: 200 条 %.0f 字节, 6000 条 %.0f 字节", small, large)

	// Thirty times the entries. A fixed cost is fine — the Tree has a buffer
	// pool and a control Page — but it must not grow with the contents.
	if large > small*2 {
		t.Fatalf(
			"opening a 6000-entry Store retained %.0f bytes of its own against %.0f "+
				"for a 200-entry one; Open must not load what the Store holds",
			large, small,
		)
	}
}

// retained reports the live heap an open leaves behind, with the opened handle
// closed again so the measurement does not leak into the next one.
func retained(t *testing.T, open func() func() error) float64 {
	t.Helper()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	closer := open()
	runtime.GC()
	runtime.ReadMemStats(&after)
	result := float64(int64(after.HeapAlloc) - int64(before.HeapAlloc))
	if err := closer(); err != nil {
		t.Fatal(err)
	}
	return result
}
