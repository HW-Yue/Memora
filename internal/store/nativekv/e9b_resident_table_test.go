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

// TestOneHugeTransactionCommits pins that a transaction's size is the caller's
// business, not the Tree's.
//
// One Tree commit's changed Pages all have to be dirty at once, and the buffer
// Pool has a fixed number of frames, so a commit that dirties more Pages than
// there are frames fails with no evictable frame. The map this replaced had no
// such coupling: a caller could stage as much as it liked. A logical snapshot
// import writes ten thousand keys in one transaction and started failing on
// exactly this, so the Tree side commits in batches and writes its catch-up
// marker last — a crash between batches leaves the marker behind and the next
// open replays the same tail, which converges because re-applying an entry the
// Tree already holds is skipped.
func TestOneHugeTransactionCommits(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "auxiliary.memora")
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	const entries = 10000
	tx, err := database.Begin(ctx, store.ReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 300)
	for index := 0; index < entries; index++ {
		if err := tx.Put(ctx, "rows", fmt.Sprintf("row-%08d", index), payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("committing %d writes in one transaction: %v", entries, err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	// All of it has to be there after a reopen, marker and entries agreeing.
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	reader, err := reopened.Begin(ctx, store.ReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Rollback() }()
	found, err := reader.Scan(ctx, "rows")
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != entries {
		t.Fatalf("Scan after reopen returned %d entries, want %d", len(found), entries)
	}
}
