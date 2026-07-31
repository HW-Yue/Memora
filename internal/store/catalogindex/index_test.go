package catalogindex

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/store/btree"
	"github.com/HW-Yue/Memora/internal/store/page"
	"github.com/HW-Yue/Memora/internal/store/treecommit"
	"github.com/HW-Yue/Memora/internal/store/wal"
)

const testSpaceID = uint64(41)

func TestLookupResolvesIdentityNameAliasAndScope(t *testing.T) {
	_, _, runtime, index := newTestIndex(t)
	database := catalog.Database{
		ID: "db_memory", Name: "Memory", Aliases: []string{"记忆库"}, SchemaVersion: 3,
		Tables: []catalog.Table{{
			ID: "tbl_decisions", DatabaseID: "db_memory", Name: "Decisions",
			Aliases: []string{"决定"}, SchemaVersion: 5,
			Columns: []catalog.Column{{
				ID: "col_title", Name: "Title", Aliases: []string{"标题"}, SchemaVersion: 7,
			}},
		}},
	}
	if receipt, err := index.Replace(1, []catalog.Database{database}); err != nil ||
		!receipt.Changed || receipt.State.Revision != 1 {
		t.Fatalf("Replace() = %+v, %v", receipt, err)
	}

	assertLocator(t, Locator{
		Kind: KindDatabase, ID: "db_memory", SchemaRevision: 3,
	}, func() (Locator, error) { return index.DatabaseByID("db_memory") })
	assertLocator(t, Locator{
		Kind: KindDatabase, ID: "db_memory", SchemaRevision: 3,
	}, func() (Locator, error) { return index.DatabaseByName(" 记忆库 ") })
	assertLocator(t, Locator{
		Kind: KindTable, ID: "tbl_decisions", DatabaseID: "db_memory", SchemaRevision: 5,
	}, func() (Locator, error) { return index.TableByID("tbl_decisions") })
	assertLocator(t, Locator{
		Kind: KindTable, ID: "tbl_decisions", DatabaseID: "db_memory", SchemaRevision: 5,
	}, func() (Locator, error) { return index.TableByName("db_memory", "决定") })
	assertLocator(t, Locator{
		Kind: KindColumn, ID: "col_title", DatabaseID: "db_memory",
		TableID: "tbl_decisions", SchemaRevision: 7,
	}, func() (Locator, error) { return index.ColumnByID("col_title") })
	assertLocator(t, Locator{
		Kind: KindColumn, ID: "col_title", DatabaseID: "db_memory",
		TableID: "tbl_decisions", SchemaRevision: 7,
	}, func() (Locator, error) { return index.ColumnByName("tbl_decisions", "标题") })
	if _, err := index.TableByName("db_other", "Decisions"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("scoped Table lookup error = %v", err)
	}
	if _, err := index.DatabaseByName("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("not-found error = %v", err)
	}
	if runtime.State().RootPageID == 0 {
		t.Fatal("Replace did not publish a root")
	}
}

func TestReplaceRejectsConflictBeforeWALAndPreservesSnapshot(t *testing.T) {
	set, _, _, index := newTestIndex(t)
	initial := []catalog.Database{{
		ID: "db_one", Name: "One", Aliases: []string{"shared"}, SchemaVersion: 1,
	}}
	if _, err := index.Replace(1, initial); err != nil {
		t.Fatal(err)
	}
	before, err := set.ScanCommitted()
	if err != nil {
		t.Fatal(err)
	}
	conflict := append([]catalog.Database(nil), initial...)
	conflict = append(conflict, catalog.Database{
		ID: "db_two", Name: "Two", Aliases: []string{"SHARED"}, SchemaVersion: 1,
	})
	if _, err := index.Replace(2, conflict); !errors.Is(err, ErrConflict) {
		t.Fatalf("Replace(conflict) error = %v", err)
	}
	after, err := set.ScanCommitted()
	if err != nil || len(after) != len(before) {
		t.Fatalf("committed transactions = %d, %v; want %d", len(after), err, len(before))
	}
	assertLocator(t, Locator{
		Kind: KindDatabase, ID: "db_one", SchemaRevision: 1,
	}, func() (Locator, error) { return index.DatabaseByName("shared") })
}

func TestReplaceIsIdempotentAndDeletesStaleKeys(t *testing.T) {
	set, _, _, index := newTestIndex(t)
	first := []catalog.Database{{
		ID: "db_one", Name: "One", Aliases: []string{"old"}, SchemaVersion: 1,
	}}
	if _, err := index.Replace(1, first); err != nil {
		t.Fatal(err)
	}
	receipt, err := index.Replace(2, first)
	if err != nil || receipt.Changed {
		t.Fatalf("idempotent Replace() = %+v, %v", receipt, err)
	}
	transactions, err := set.ScanCommitted()
	if err != nil || len(transactions) != 1 {
		t.Fatalf("idempotent WAL transactions = %d, %v", len(transactions), err)
	}
	second := []catalog.Database{{
		ID: "db_one", Name: "Two", Aliases: []string{"One"}, SchemaVersion: 2,
	}}
	if _, err := index.Replace(3, second); err != nil {
		t.Fatal(err)
	}
	if _, err := index.DatabaseByName("old"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted alias error = %v", err)
	}
	assertLocator(t, Locator{
		Kind: KindDatabase, ID: "db_one", SchemaRevision: 2,
	}, func() (Locator, error) { return index.DatabaseByName("two") })
}

func TestLargeCatalogMatchesReferenceModelAndSplits(t *testing.T) {
	_, _, runtime, index := newTestIndex(t)
	databases := make([]catalog.Database, 0, 320)
	for number := 0; number < 320; number++ {
		id := fmt.Sprintf("db_%04d", number)
		databases = append(databases, catalog.Database{
			ID: id, Name: fmt.Sprintf("Database %04d", number),
			Aliases: []string{fmt.Sprintf("数据库 %04d", number)}, SchemaVersion: uint64(number + 1),
		})
	}
	if _, err := index.Replace(1, databases); err != nil {
		t.Fatal(err)
	}
	root, err := runtime.Read(runtime.State().RootPageID)
	if err != nil {
		t.Fatal(err)
	}
	node, err := btree.Decode(root)
	if err != nil || node.Kind != btree.KindInternal {
		t.Fatalf("root after split = %+v, %v", node, err)
	}
	for number, database := range databases {
		want := Locator{Kind: KindDatabase, ID: database.ID, SchemaRevision: uint64(number + 1)}
		assertLocator(t, want, func() (Locator, error) { return index.DatabaseByID(database.ID) })
		assertLocator(t, want, func() (Locator, error) { return index.DatabaseByName(database.Aliases[0]) })
	}
}

func TestCrashBeforeFlushReopensCommittedLookupIndex(t *testing.T) {
	directory := t.TempDir()
	walPath := filepath.Join(directory, "wal")
	pagePath := filepath.Join(directory, "catalog.pages")
	set, manager, runtime, index := openTestIndex(t, walPath, pagePath, false)
	if _, err := index.Replace(1, []catalog.Database{{
		ID: "db_memory", Name: "Memory", Aliases: []string{"记忆"}, SchemaVersion: 9,
	}}); err != nil {
		t.Fatal(err)
	}
	if count, err := manager.PageCount(); err != nil || count != 1 {
		t.Fatalf("pre-crash Page count = %d, %v", count, err)
	}
	committed := runtime.State()
	if err := set.Close(); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}

	reopenedSet, reopenedManager, reopenedRuntime, reopened := openTestIndex(t, walPath, pagePath, true)
	defer func() { _ = reopenedSet.Close(); _ = reopenedManager.Close() }()
	if reopenedRuntime.State() != committed {
		t.Fatalf("reopened state = %+v, want %+v", reopenedRuntime.State(), committed)
	}
	assertLocator(t, Locator{
		Kind: KindDatabase, ID: "db_memory", SchemaRevision: 9,
	}, func() (Locator, error) { return reopened.DatabaseByName("记忆") })
}

func TestLookupRejectsCorruptTreeWithoutFallback(t *testing.T) {
	directory := t.TempDir()
	walPath := filepath.Join(directory, "wal")
	pagePath := filepath.Join(directory, "catalog.pages")
	set, manager, runtime, index := openTestIndex(t, walPath, pagePath, false)
	if _, err := index.Replace(1, []catalog.Database{{
		ID: "db_memory", Name: "Memory", SchemaVersion: 1,
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.FlushDirty(16); err != nil {
		t.Fatal(err)
	}
	rootID := runtime.State().RootPageID
	root, err := manager.Read(rootID)
	if err != nil {
		t.Fatal(err)
	}
	root.Payload[0] ^= 0xff
	if err := manager.Write(root); err != nil {
		t.Fatal(err)
	}
	if err := manager.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := set.Close(); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}

	reopenedSet, reopenedManager, _, reopened := openTestIndex(t, walPath, pagePath, true)
	defer func() { _ = reopenedSet.Close(); _ = reopenedManager.Close() }()
	if _, err := reopened.DatabaseByName("Memory"); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("corrupt lookup error = %v", err)
	}
}

func TestLocatorCodecRejectsCorruptionCorpus(t *testing.T) {
	want := Locator{
		Kind: KindColumn, ID: "col_title", DatabaseID: "db_memory",
		TableID: "tbl_decisions", SchemaRevision: 7,
	}
	encoded, err := encodeLocator(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeLocator(encoded)
	if err != nil || got != want {
		t.Fatalf("locator round trip = %+v, %v", got, err)
	}
	corpus := [][]byte{
		encoded[:len(encoded)-1],
		append(append([]byte(nil), encoded...), 0),
		append([]byte(nil), encoded...),
		append([]byte(nil), encoded...),
		append([]byte(nil), encoded...),
	}
	corpus[2][8] = 99
	corpus[3][10] = 99
	corpus[4][12] = 1
	for number, value := range corpus {
		if _, err := decodeLocator(value); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("corpus[%d] error = %v", number, err)
		}
	}
}

func TestStoredKeyCodecRejectsMalformedOrNonCanonicalKeys(t *testing.T) {
	valid, err := nameKey(keyTableName, "db_memory", "  Decisions ")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateStoredKey(valid); err != nil {
		t.Fatalf("valid key error = %v", err)
	}
	corpus := [][]byte{
		valid[:len(valid)-1],
		append(append([]byte(nil), valid...), 0),
		append([]byte(nil), valid...),
		append([]byte(nil), valid...),
	}
	corpus[2][0] = 99
	corpus[3][1] = 99
	for number, value := range corpus {
		if err := validateStoredKey(value); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("key corpus[%d] error = %v", number, err)
		}
	}
}

func TestReplaceRejectsDuplicateStableIdentity(t *testing.T) {
	set, _, _, index := newTestIndex(t)
	duplicate := []catalog.Database{
		{ID: "db_same", Name: "One", SchemaVersion: 1},
		{ID: "db_same", Name: "Two", SchemaVersion: 1},
	}
	if _, err := index.Replace(1, duplicate); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate stable ID error = %v", err)
	}
	transactions, err := set.ScanCommitted()
	if err != nil || len(transactions) != 0 {
		t.Fatalf("transactions after duplicate ID = %d, %v", len(transactions), err)
	}
}

func newTestIndex(t *testing.T) (*wal.SegmentSet, *page.Manager, *treecommit.Runtime, *Index) {
	t.Helper()
	directory := t.TempDir()
	set, manager, runtime, index := openTestIndex(
		t, filepath.Join(directory, "wal"), filepath.Join(directory, "catalog.pages"), false,
	)
	t.Cleanup(func() { _ = set.Close(); _ = manager.Close() })
	return set, manager, runtime, index
}

func openTestIndex(t *testing.T, walPath, pagePath string, reopen bool) (*wal.SegmentSet, *page.Manager, *treecommit.Runtime, *Index) {
	t.Helper()
	var (
		set     *wal.SegmentSet
		manager *page.Manager
		err     error
	)
	if reopen {
		set, err = wal.OpenSegmentSet(walPath, 0)
	} else {
		set, err = wal.CreateSegmentSet(walPath, 0)
	}
	if err != nil {
		t.Fatal(err)
	}
	if reopen {
		manager, err = page.Open(pagePath, testSpaceID)
	} else {
		manager, err = page.Create(pagePath, testSpaceID)
	}
	if err != nil {
		_ = set.Close()
		t.Fatal(err)
	}
	runtime, _, err := treecommit.OpenRuntime(set, manager, treecommit.RuntimeConfig{
		SpaceID: testSpaceID, Capacity: 128, OldFrames: 64,
	})
	if err != nil {
		_ = set.Close()
		_ = manager.Close()
		t.Fatal(err)
	}
	index, err := Open(runtime)
	if err != nil {
		_ = set.Close()
		_ = manager.Close()
		t.Fatal(err)
	}
	return set, manager, runtime, index
}

func assertLocator(t *testing.T, want Locator, lookup func() (Locator, error)) {
	t.Helper()
	got, err := lookup()
	if err != nil || got != want {
		t.Fatalf("lookup = %+v, %v; want %+v", got, err, want)
	}
}
