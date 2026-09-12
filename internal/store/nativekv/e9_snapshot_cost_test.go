package nativekv

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/HW-Yue/Memora/internal/store"
)

// TestBeginningATransactionDoesNotCopyTheStore pins the cost of starting one.
//
// Begin used to build a private snapshot by deep-copying every entry, payload
// included, on every transaction — read-only ones too. That was how it bought
// a stable view of a map that writers mutated in place. There is no map now and
// nothing to copy, so starting a transaction has to cost a constant.
func TestBeginningATransactionDoesNotCopyTheStore(t *testing.T) {
	ctx := context.Background()

	measure := func(entries int) float64 {
		path := filepath.Join(t.TempDir(), "auxiliary.memora")
		database := seedEntries(t, path, entries)
		defer func() { _ = database.Close() }()

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

	small := measure(200)
	large := measure(4000)
	t.Logf("Begin 平均分配: 200 条 %.0f 字节, 4000 条 %.0f 字节", small, large)
	if large > small*2 {
		t.Fatalf(
			"Begin allocated %.0f bytes on a 4000-entry Store against %.0f on a "+
				"200-entry one; it must not scale with what the Store holds",
			large, small,
		)
	}
}

// TestATransactionSeesAConsistentStore pins the isolation this Store promises
// and records the mechanism change underneath it.
//
// A transaction must not see writes that land after it starts. That used to be
// bought by copying every entry at Begin. A shadow-paged Tree has nothing to
// copy — a Page a commit retires is reusable immediately, because
// treecommit.Runtime keeps no reader table, so an old root is not a snapshot —
// so readers and writers are serialised instead: a transaction holds the Store
// lock for its life.
//
// The guarantee is therefore stronger than it was (a writer cannot interleave
// at all) and the concurrency narrower (a writer waits for open readers). That
// trade is only safe because every transaction against this Store is opened and
// finished inside one function; a caller that held one open across other work
// would block the Store for that long.
func TestATransactionSeesAConsistentStore(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "auxiliary.memora")
	database := seedEntries(t, path, 0)
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
	if payload, err := reader.Get(ctx, "traces", "existing"); err != nil || string(payload) != "before" {
		t.Fatalf(`Get("existing") = %q, %v`, payload, err)
	}
	entries, err := reader.Scan(ctx, "traces")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Key != "existing" || entries[1].Key != "removed" {
		t.Fatalf("Scan = %+v, want existing and removed", entries)
	}
	// A repeated read inside the same transaction answers the same thing.
	if payload, err := reader.Get(ctx, "traces", "existing"); err != nil || string(payload) != "before" {
		t.Fatalf(`repeated Get("existing") = %q, %v`, payload, err)
	}
	if err := reader.Rollback(); err != nil {
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
	// Its own uncommitted writes are visible to it, and a delete it has staged
	// hides the committed value from it too.
	if pending, err := writer.Get(ctx, "traces", "existing"); err != nil || string(pending) != "after" {
		t.Fatalf(`writer Get("existing") = %q, %v, want its own write`, pending, err)
	}
	if _, err := writer.Get(ctx, "traces", "removed"); err != store.ErrNotFound {
		t.Fatalf(`writer Get("removed") error = %v, want ErrNotFound`, err)
	}
	if staged, err := writer.Scan(ctx, "traces"); err != nil {
		t.Fatal(err)
	} else if len(staged) != 2 || staged[0].Key != "added" || staged[1].Key != "existing" {
		t.Fatalf("writer Scan = %+v, want its own staged view", staged)
	}
	if err := writer.Commit(); err != nil {
		t.Fatal(err)
	}

	fresh, err := database.Begin(ctx, store.ReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fresh.Rollback() }()
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

// TestAnIndexBehindItsLogCatchesUp pins the repair path.
//
// The log is the authority and commits first, so a crash between the log append
// and the Tree commit leaves the Tree short by exactly that batch. The Tree
// records the log Size it is current through and reopening replays only the
// records past it. Deleting the whole index is the extreme of the same case and
// must rebuild from the log rather than lose data.
func TestAnIndexBehindItsLogCatchesUp(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "auxiliary.memora")
	database := seedEntries(t, path, 0)

	tx, err := database.Begin(ctx, store.ReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 20; index++ {
		if err := tx.Put(ctx, "traces", fmt.Sprintf("kept-%02d", index), []byte("value")); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	if err := removeIndex(path); err != nil {
		t.Fatal(err)
	}

	rebuilt, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rebuilt.Close() }()
	reader, err := rebuilt.Begin(ctx, store.ReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Rollback() }()
	entries, err := reader.Scan(ctx, "traces")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 20 {
		t.Fatalf("Scan after rebuilding the index returned %d entries, want 20", len(entries))
	}
	if payload, err := reader.Get(ctx, "traces", "kept-07"); err != nil || string(payload) != "value" {
		t.Fatalf(`Get after rebuilding the index = %q, %v, want "value"`, payload, err)
	}
}

// TestAValueLargerThanOneLeafRecordSurvives pins a capability the move to a
// Tree would otherwise have taken away.
//
// A leaf record is capped at 8 KiB — objectindex refuses more, because one
// record shares a 16 KiB Page with its neighbours and overflow Pages do not
// exist yet. The map this replaced had no such limit, so a value that used to
// store fine would have started failing: the legacy Catalog keeps its whole
// snapshot under one key and crosses 8 KiB on an ordinary schema, and route
// traces have no bound at all.
//
// Values are therefore split across records of their own kind, which a walk of
// the entries never sees. This checks a value well past the limit, that
// rewriting it shrinks and grows correctly, that Scan joins the pieces too, and
// that it survives a reopen.
func TestAValueLargerThanOneLeafRecordSurvives(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "auxiliary.memora")
	database := seedEntries(t, path, 0)

	large := make([]byte, 40<<10)
	for index := range large {
		large[index] = byte(index % 251)
	}
	small := []byte("small")

	write := func(payload []byte) {
		t.Helper()
		tx, err := database.Begin(ctx, store.ReadWrite)
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Put(ctx, "traces", "big", payload); err != nil {
			t.Fatal(err)
		}
		if err := tx.Put(ctx, "traces", "neighbour", []byte("beside it")); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	read := func(stage string, want []byte) {
		t.Helper()
		tx, err := database.Begin(ctx, store.ReadOnly)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		got, err := tx.Get(ctx, "traces", "big")
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s: Get(big) returned %d bytes (err %v), want %d", stage, len(got), err, len(want))
		}
		entries, err := tx.Scan(ctx, "traces")
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 2 || entries[0].Key != "big" || !bytes.Equal(entries[0].Value, want) {
			t.Fatalf("%s: Scan returned %d entries, first %q of %d bytes",
				stage, len(entries), entries[0].Key, len(entries[0].Value))
		}
	}

	write(large)
	read("written", large)

	// Shrinking below the limit must drop back to a single record cleanly.
	write(small)
	read("shrunk", small)

	// And growing again must not read a stale piece of the first value.
	write(large)
	read("regrown", large)

	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	tx, err := reopened.Begin(ctx, store.ReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if got, err := tx.Get(ctx, "traces", "big"); err != nil || !bytes.Equal(got, large) {
		t.Fatalf("after reopen: Get(big) returned %d bytes (err %v), want %d", len(got), err, len(large))
	}
}
