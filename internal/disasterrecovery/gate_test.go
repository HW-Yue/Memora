package disasterrecovery_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/binlog"
	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/disasterrecovery"
	"github.com/HW-Yue/Memora/internal/nativecatalog"
	"github.com/HW-Yue/Memora/internal/nativemigration"
	"github.com/HW-Yue/Memora/internal/nativerow"
	"github.com/HW-Yue/Memora/internal/nativesnapshot"
	"github.com/HW-Yue/Memora/internal/pagestoremigration"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/snapshot"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

// TestTheBackupAndBinlogRestoreALostDatabaseToItsLastCommit is E8 stage 0's
// hard gate (docs/storage/record-index-and-authority-v1.md §6).
//
// Clustered promotion makes the page files the Database and drops the record
// file out of the correctness path. The record file is what catches a broken
// tree today — v6→v8 rebuilt every tree by re-reading it — and promotion gives
// that up. What is left underneath is the disaster-recovery chain: a full
// backup plus the binlog that rolls it forward. That chain had never been run
// end to end, so it is verified here, before the step that makes it the only
// one.
//
// The disaster is total: the whole databases directory is removed, record file
// and page files together. Deleting only the page files would prove nothing —
// the record file would rebuild them, which is exactly the safety net being
// given up. Only the binlog survives, because the binlog is the one thing whose
// stated purpose is to live somewhere else (internal/nativemigration's
// BinlogDirname comment), and it is replayed from a copy, as an archived log
// would be.
//
// The assertion is the logical snapshot hash, byte for byte: what has to
// survive is what the Database means. The Database is then read through the
// rebuilt trees, because a hash that matches over a Database that will not
// serve a Row is not a restore.
func TestTheBackupAndBinlogRestoreALostDatabaseToItsLastCommit(t *testing.T) {
	ctx := context.Background()
	origin := t.TempDir()

	source := openInstance(t, ctx, origin)
	table := createFixtureTable(t, ctx, source)
	first := insertRow(t, ctx, source, table, "first")
	second := insertRow(t, ctx, source, table, "second")

	// The full backup. It is taken with the Database open and writing, which is
	// the only way a backup is ever taken in practice.
	backupRoot := filepath.Join(t.TempDir(), "backup")
	if _, err := disasterrecovery.Backup(ctx, source.file, source.binlog, backupRoot); err != nil {
		t.Fatalf("Backup() error = %v", err)
	}

	// Everything after the backup exists only in the log.
	updateRow(t, ctx, source, table, first, "first revised")
	third := insertRow(t, ctx, source, table, "third")
	updateRow(t, ctx, source, table, second, "second revised")
	addFixtureTable(t, ctx, source)

	want := logicalHash(t, source.file)
	closeInstance(t, source)

	// The disaster: record file and page files are gone together.
	if err := os.RemoveAll(filepath.Join(origin, "databases")); err != nil {
		t.Fatal(err)
	}

	archive := filepath.Join(t.TempDir(), "binlog-archive")
	copyTree(t, nativemigration.BinlogDirectory(origin), archive)

	target := t.TempDir()
	if err := disasterrecovery.Restore(ctx, target, backupRoot, archive); err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	// The record log and the commit hint beside it — the small file that lets
	// the next open find the end of the log without reading it. No page files:
	// those are derived, and building them is what the open below does.
	if entries, err := os.ReadDir(filepath.Join(target, "databases")); err != nil ||
		len(entries) != 2 {
		t.Fatalf("restore left %v, %v, want the record log and its hint", entries, err)
	}

	restored := openInstance(t, ctx, target)
	defer closeInstance(t, restored)
	if got := logicalHash(t, restored.file); got != want {
		t.Fatalf("restored logical hash = %s, want %s", got, want)
	}
	for id, title := range map[string]string{
		first: "first revised", second: "second revised", third: "third",
	} {
		value, err := restored.rows.Get(ctx, "work", "records", id)
		if err != nil || value.Values["title"] != title {
			t.Fatalf("restored Row %q = %#v, %v", id, value, err)
		}
	}
}

// TestABackupTakenUnderConcurrentWritesOverlapsRatherThanSkips pins the one
// ordering decision Backup makes.
//
// The binlog position is read before the record log's length, so a transaction
// that commits between them is in both halves. The restore has to recognise
// that and apply it once. The other order would leave the transaction in
// neither, and no later check would notice: the restored Database would simply
// be missing a commit from its middle.
func TestABackupTakenUnderConcurrentWritesOverlapsRatherThanSkips(t *testing.T) {
	ctx := context.Background()
	origin := t.TempDir()
	source := openInstance(t, ctx, origin)
	table := createFixtureTable(t, ctx, source)
	kept := insertRow(t, ctx, source, table, "kept")

	// Stand in for the writer that commits while the copy is running: the
	// position is taken, the transaction commits, and only then is the length
	// read. Backup does this internally; here the two halves are pulled apart
	// so the overlap is the case under test rather than a race to hope for.
	position, err := source.binlog.Position()
	if err != nil {
		t.Fatal(err)
	}
	overlapped := insertRow(t, ctx, source, table, "overlapped")
	backupRoot := filepath.Join(t.TempDir(), "backup")
	receipt, err := disasterrecovery.Backup(ctx, source.file, source.binlog, backupRoot)
	if err != nil {
		t.Fatal(err)
	}
	// Rewrite the receipt to the earlier position, which is what Backup's own
	// ordering produces when a write lands inside it.
	writeReceipt(t, backupRoot, disasterrecovery.Receipt{
		RecordBytes: receipt.RecordBytes, Binlog: position,
	})

	insertRow(t, ctx, source, table, "after")
	want := logicalHash(t, source.file)
	closeInstance(t, source)
	if err := os.RemoveAll(filepath.Join(origin, "databases")); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "binlog-archive")
	copyTree(t, nativemigration.BinlogDirectory(origin), archive)

	target := t.TempDir()
	if err := disasterrecovery.Restore(ctx, target, backupRoot, archive); err != nil {
		t.Fatalf("Restore() over an overlapping tail = %v", err)
	}
	restored := openInstance(t, ctx, target)
	defer closeInstance(t, restored)
	if got := logicalHash(t, restored.file); got != want {
		t.Fatalf("restored logical hash = %s, want %s", got, want)
	}
	for _, id := range []string{kept, overlapped} {
		if _, err := restored.rows.Get(ctx, "work", "records", id); err != nil {
			t.Fatalf("Row %q after an overlapping restore: %v", id, err)
		}
	}
}

// TestABinlogThatNoLongerReachesTheBackupIsReportedNotPartlyApplied pins what
// happens when retention has outrun the base.
//
// A backup older than the log's oldest segment cannot be rolled forward: the
// transactions in between are gone. Replaying what is left would produce a
// Database missing a middle, which is worse than no restore, so it is refused.
func TestABinlogThatNoLongerReachesTheBackupIsReportedNotPartlyApplied(t *testing.T) {
	ctx := context.Background()
	origin := t.TempDir()
	source := openInstance(t, ctx, origin)
	table := createFixtureTable(t, ctx, source)
	insertRow(t, ctx, source, table, "first")
	backupRoot := filepath.Join(t.TempDir(), "backup")
	if _, err := disasterrecovery.Backup(
		ctx, source.file, source.binlog, backupRoot,
	); err != nil {
		t.Fatal(err)
	}
	insertRow(t, ctx, source, table, "second")
	closeInstance(t, source)

	// The archive begins after the base: retention dropped the segments that
	// joined them, so the surviving log starts further along than the backup
	// stops. Renaming the segment is how that state is reached without waiting
	// out a retention window.
	archive := filepath.Join(t.TempDir(), "binlog-archive")
	copyTree(t, nativemigration.BinlogDirectory(origin), archive)
	dropEarlySegments(t, archive)

	target := t.TempDir()
	if err := disasterrecovery.Restore(
		ctx, target, backupRoot, archive,
	); !errors.Is(err, binlog.ErrGap) {
		t.Fatalf("Restore() past the log's reach = %v, want ErrGap", err)
	}
	// Nothing was left half-restored: a refusal that had already written a
	// record log would leave the operator a Database to mistake for a restore.
	if entries, err := os.ReadDir(filepath.Join(target, "databases")); err == nil &&
		len(entries) != 0 {
		t.Fatalf("refused restore left %v behind", entries)
	}
}

// dropEarlySegments renumbers the archive so its oldest segment is past where
// any existing backup stops, which is what a retention window does to a log.
func dropEarlySegments(t *testing.T, archive string) {
	t.Helper()
	entries, err := os.ReadDir(archive)
	if err != nil || len(entries) == 0 {
		t.Fatalf("binlog archive = %v, %v", entries, err)
	}
	for _, entry := range entries {
		name := entry.Name()
		renamed := strings.Replace(name, "00001", "00009", 1)
		if renamed == name {
			t.Fatalf("unexpected binlog segment name %q", name)
		}
		if err := os.Rename(
			filepath.Join(archive, name), filepath.Join(archive, renamed),
		); err != nil {
			t.Fatal(err)
		}
	}
}

// TestALogicalSnapshotIsNotARollforwardBase records why the full half of the
// chain is a byte copy and not nativesnapshot.Export.
//
// The plan (docs/storage/record-index-and-authority-v1.md §6) named the logical
// snapshot as the full half, on the strength of it already existing. Running
// the chain showed the two halves do not compose. A logical import replays
// history through the ordinary write path: it allocates change sequences from
// one, and it keeps only the current revision of each catalog object. The
// binlog carries the record IDs and sequences that were actually written. Land
// the log on the import and the numbering does not meet — here the base ends at
// change two and the tail resumes at five, and the change index refuses the
// hole.
//
// This is a property of the two formats, not a bug with a fix, so it is pinned
// rather than repaired: the next person to reach for Export as a restore base
// gets this test instead of a corrupt Database.
func TestALogicalSnapshotIsNotARollforwardBase(t *testing.T) {
	ctx := context.Background()
	origin := t.TempDir()
	source := openInstance(t, ctx, origin)
	table := createFixtureTable(t, ctx, source)
	first := insertRow(t, ctx, source, table, "first")
	insertRow(t, ctx, source, table, "second")

	base, err := nativesnapshot.NewNative(source.file).Export()
	if err != nil {
		t.Fatal(err)
	}
	from, err := source.binlog.Position()
	if err != nil {
		t.Fatal(err)
	}
	updateRow(t, ctx, source, table, first, "first revised")
	closeInstance(t, source)
	if err := os.RemoveAll(filepath.Join(origin, "databases")); err != nil {
		t.Fatal(err)
	}

	// Do by hand what a logical-base restore would do, so the divergence is
	// visible rather than hidden behind an API that refuses to offer it.
	target := t.TempDir()
	directory := filepath.Join(target, "databases")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := nativestore.Create(
		filepath.Join(directory, nativemigration.NativeFilename), nativestore.FileKindDatabase,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := nativesnapshot.NewNative(file).Import(base); err != nil {
		t.Fatal(err)
	}
	log, err := binlog.OpenWithOptions(
		nativemigration.BinlogDirectory(origin), binlog.Options{Retention: -1},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := log.ReplayFrom(from, func(entry binlog.Entry) error {
		transaction, beginErr := file.Begin()
		if beginErr != nil {
			return beginErr
		}
		for _, record := range entry.Records {
			if putErr := transaction.Put(
				nativestore.ObjectKind(record.Kind), record.SchemaVersion, record.ID, record.Payload,
			); putErr != nil {
				_ = transaction.Rollback()
				return putErr
			}
		}
		return transaction.Commit()
	}); err != nil {
		t.Fatal(err)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	// The record log takes it — nothing collides, because the sequences the
	// import allocated and the ones the log carries are simply different
	// numbers. The damage only surfaces where the change log's density is
	// checked, which is on the way in.
	sequences, err := file.IDs(nativestore.ObjectKindCommittedChange)
	if err != nil {
		t.Fatal(err)
	}
	if len(sequences) < 3 {
		t.Fatalf("change records = %v, want the import's and the log's together", sequences)
	}
	if _, err := pagestoremigration.OpenAuthority(ctx, file, directory); err == nil {
		t.Fatal("a logical base rolled forward by the binlog opened cleanly, want a refusal")
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

type instance struct {
	dataDir    string
	file       *nativestore.File
	binlog     *binlog.Log
	authority  *pagestoremigration.Authority
	dictionary *nativecatalog.Service
	rows       *nativerow.Service
}

func openInstance(t *testing.T, ctx context.Context, dataDir string) *instance {
	t.Helper()
	migration, err := nativemigration.OpenDefault(ctx, dataDir)
	if err != nil {
		t.Fatalf("OpenDefault() error = %v", err)
	}
	authority, err := pagestoremigration.OpenAuthority(
		ctx, migration.File, filepath.Join(dataDir, "databases"),
	)
	if err != nil {
		t.Fatalf("OpenAuthority() error = %v", err)
	}
	dictionary := nativecatalog.NewService(
		nativecatalog.New(migration.File), nativecatalog.ServiceOptions{Authority: authority},
	)
	return &instance{
		dataDir: dataDir, file: migration.File, binlog: migration.Binlog, authority: authority,
		dictionary: dictionary,
		rows: nativerow.NewService(
			nativerow.NewWithObjects(migration.File, authority), dictionary,
			nativerow.ServiceOptions{Authority: authority},
		),
	}
}

func closeInstance(t *testing.T, value *instance) {
	t.Helper()
	if err := value.authority.Close(); err != nil {
		t.Fatal(err)
	}
	if err := value.file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := value.binlog.Close(); err != nil {
		t.Fatal(err)
	}
}

func createFixtureTable(t *testing.T, ctx context.Context, value *instance) catalog.Table {
	t.Helper()
	if _, err := value.dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{
		Name: "work", Purpose: "Work", Scope: "Disaster recovery gate",
	}); err != nil {
		t.Fatal(err)
	}
	table, err := value.dictionary.CreateTable(ctx, "work", catalog.TableDefinition{
		Name: "records", Purpose: "Records", RowSemantics: "One claim",
		Columns: []catalog.ColumnDefinition{{Name: "title", Type: "TEXT(100)", Purpose: "Title"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return table
}

func insertRow(t *testing.T, ctx context.Context, value *instance, table catalog.Table, title string) string {
	t.Helper()
	inserted, err := value.rows.Insert(ctx, "work", "records", map[string]any{"title": title},
		row.WriteOptions{ExpectedSchemaVersion: table.SchemaVersion, Metadata: row.WriteMetadata{Actor: "gate", Source: "test", Reason: "fixture"}})
	if err != nil {
		t.Fatalf("Insert(%q) error = %v", title, err)
	}
	return inserted.ID
}

func updateRow(t *testing.T, ctx context.Context, value *instance, table catalog.Table, id, title string) {
	t.Helper()
	current, err := value.rows.Get(ctx, "work", "records", id)
	if err != nil {
		t.Fatalf("Get(%q) error = %v", id, err)
	}
	if _, err := value.rows.Update(ctx, "work", "records", id, map[string]any{"title": title},
		row.WriteOptions{
			ExpectedRevision: current.Revision, ExpectedSchemaVersion: table.SchemaVersion,
			Metadata: row.WriteMetadata{Actor: "gate", Source: "test", Reason: "fixture"},
		}); err != nil {
		t.Fatalf("Update(%q) error = %v", id, err)
	}
}

func logicalHash(t *testing.T, file *nativestore.File) string {
	t.Helper()
	exported, err := nativesnapshot.NewNative(file).Export()
	if err != nil {
		t.Fatalf("Export() error = %v", err)
	}
	hash, err := snapshot.CanonicalHash(exported)
	if err != nil {
		t.Fatalf("CanonicalHash() error = %v", err)
	}
	return hash
}

func copyTree(t *testing.T, source, target string) {
	t.Helper()
	entries, err := os.ReadDir(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		body, err := os.ReadFile(filepath.Join(source, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(target, entry.Name()), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// addFixtureTable puts a catalog write in the binlog tail. Creating a Table
// writes a new revision of the Database record too, and catalog objects are
// revision-chained, so a base that dropped the earlier links fails here rather
// than quietly.
func addFixtureTable(t *testing.T, ctx context.Context, value *instance) {
	t.Helper()
	if _, err := value.dictionary.CreateTable(ctx, "work", catalog.TableDefinition{
		Name: "notes", Purpose: "Notes", RowSemantics: "One note",
		Columns: []catalog.ColumnDefinition{{Name: "body", Type: "TEXT(100)", Purpose: "Body"}},
	}); err != nil {
		t.Fatalf("CreateTable() after the backup error = %v", err)
	}
}

func writeReceipt(t *testing.T, backupRoot string, receipt disasterrecovery.Receipt) {
	t.Helper()
	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(backupRoot, disasterrecovery.ReceiptFilename), encoded, 0o600,
	); err != nil {
		t.Fatal(err)
	}
}
