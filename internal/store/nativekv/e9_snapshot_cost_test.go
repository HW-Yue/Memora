package nativekv

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/HW-Yue/Memora/internal/store"
)

// beginAllocationsFor reports the average bytes allocated per Begin on a store
// holding entries items.
func beginAllocationsFor(t *testing.T, entries int) float64 {
	t.Helper()
	ctx := context.Background()
	database, err := Open(filepath.Join(t.TempDir(), "auxiliary.memora"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()

	tx, err := database.Begin(ctx, store.ReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 256)
	for index := 0; index < entries; index++ {
		if err := tx.Put(ctx, "traces", fmt.Sprintf("key-%06d", index), payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	const rounds = 200
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for round := 0; round < rounds; round++ {
		readOnly, err := database.Begin(ctx, store.ReadOnly)
		if err != nil {
			t.Fatal(err)
		}
		if err := readOnly.Rollback(); err != nil {
			t.Fatal(err)
		}
	}
	runtime.ReadMemStats(&after)
	return float64(after.TotalAlloc-before.TotalAlloc) / rounds
}

// TestBeginningATransactionDoesNotCopyTheWholeStore is the gate for the second
// resident index in this engine.
//
// Begin used to build a private snapshot by deep-copying every entry, payload
// included, on every transaction — read-only transactions too. The record
// file's resident table is paid once per open; this one is paid once per
// transaction, and the two stores it backs (auxiliary.memora and
// security.memora) are what security, host input and route traces touch on
// every single call. Route traces grow without bound, so the per-call cost
// grows with how much the instance has ever been used.
//
// The isolation Begin buys is real and must survive: an open transaction may
// not see a commit that lands after it started. What must go is paying for it
// by copying. The test pins the property that makes the fix verifiable —
// Begin's cost must not scale with how much the store holds.
func TestBeginningATransactionDoesNotCopyTheWholeStore(t *testing.T) {
	small := beginAllocationsFor(t, 100)
	large := beginAllocationsFor(t, 4000)

	t.Logf("Begin 平均分配: 100 条 %.0f 字节, 4000 条 %.0f 字节", small, large)
	if large > small*4 {
		t.Fatalf(
			"Begin allocated %.0f bytes on a 4000-entry store against %.0f on a "+
				"100-entry one; it must not scale with the entry count",
			large, small,
		)
	}
}

// TestAnOpenTransactionDoesNotSeeALaterCommit pins the property the snapshot
// exists for, and the one the copy-on-write change could plausibly have broken.
//
// Begin no longer copies the store; it takes the published map by reference.
// That is only correct while nothing writes into a published map, so this reads
// through a transaction opened before another one commits — over a key that
// existed beforehand, a key created afterwards, and a key deleted afterwards.
func TestAnOpenTransactionDoesNotSeeALaterCommit(t *testing.T) {
	ctx := context.Background()
	database, err := Open(filepath.Join(t.TempDir(), "auxiliary.memora"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()

	seed, err := database.Begin(ctx, store.ReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Put(ctx, "traces", "existing", []byte("before")); err != nil {
		t.Fatal(err)
	}
	if err := seed.Put(ctx, "traces", "removed", []byte("doomed")); err != nil {
		t.Fatal(err)
	}
	if err := seed.Commit(); err != nil {
		t.Fatal(err)
	}

	reader, err := database.Begin(ctx, store.ReadOnly)
	if err != nil {
		t.Fatal(err)
	}

	writer, err := database.Begin(ctx, store.ReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Put(ctx, "traces", "existing", []byte("after")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Put(ctx, "traces", "added", []byte("new")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Delete(ctx, "traces", "removed"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Commit(); err != nil {
		t.Fatal(err)
	}

	payload, err := reader.Get(ctx, "traces", "existing")
	if err != nil || string(payload) != "before" {
		t.Fatalf(`Get("existing") = %q, %v; the reader must still see "before"`, payload, err)
	}
	if _, err := reader.Get(ctx, "traces", "added"); err != store.ErrNotFound {
		t.Fatalf(`Get("added") error = %v, want ErrNotFound`, err)
	}
	if payload, err := reader.Get(ctx, "traces", "removed"); err != nil || string(payload) != "doomed" {
		t.Fatalf(`Get("removed") = %q, %v; a later delete must not reach this reader`, payload, err)
	}

	entries, err := reader.Scan(ctx, "traces")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Key != "existing" || entries[1].Key != "removed" {
		t.Fatalf("Scan = %+v, want the two keys that existed when the reader started", entries)
	}

	// A transaction opened after the commit sees all of it.
	fresh, err := database.Begin(ctx, store.ReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	if payload, err := fresh.Get(ctx, "traces", "existing"); err != nil || string(payload) != "after" {
		t.Fatalf(`fresh Get("existing") = %q, %v, want "after"`, payload, err)
	}
	if _, err := fresh.Get(ctx, "traces", "removed"); err != store.ErrNotFound {
		t.Fatalf(`fresh Get("removed") error = %v, want ErrNotFound`, err)
	}
	entries, err = fresh.Scan(ctx, "traces")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Key != "added" || entries[1].Key != "existing" {
		t.Fatalf("fresh Scan = %+v, want added and existing", entries)
	}
}
