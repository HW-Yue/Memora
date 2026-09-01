package binlog_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/binlog"
	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/nativecatalog"
	"github.com/HW-Yue/Memora/internal/nativerow"
	"github.com/HW-Yue/Memora/internal/row"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

// TestReplayingTheBinlogAloneRebuildsTheDatabase is E6 stage 1's gate.
//
// The write model makes the binlog the one thing recovery consults: replaying
// it rebuilds the Database, the history table included, and nothing else has to
// be read to do it (docs/product/write-model.md §5).
//
// So the gate replays into an *empty* Database and compares the two record for
// record. Comparing what a query returns would not settle it — a rebuild can
// answer the same queries while holding different bytes, and this log is
// supposed to reproduce the Database, not something equivalent to it.
func TestReplayingTheBinlogAloneRebuildsTheDatabase(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	sourcePath := filepath.Join(directory, "source.memora")
	source, err := nativestore.Create(sourcePath, nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()

	log, err := binlog.Open(filepath.Join(directory, "binlog"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	source.AttachBinlog(binlog.NewSink(log))

	table := writeFixture(t, source)

	// Replay into a Database that has never seen a write.
	targetPath := filepath.Join(directory, "target.memora")
	target, err := nativestore.Create(targetPath, nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	if err := binlog.ReplayInto(log, target); err != nil {
		t.Fatalf("ReplayInto() = %v", err)
	}

	assertSameRecords(t, source, target)

	// And the rebuilt Database answers as the original does, including the
	// history the write model requires the binlog to carry.
	original, err := nativerow.New(source).AllRows()
	if err != nil {
		t.Fatal(err)
	}
	if len(original) == 0 {
		t.Fatal("fixture wrote no Rows")
	}
	for _, value := range original {
		records, _, err := nativerow.New(target).History(value.DatabaseID, value.TableID, value.ID, 100)
		if err != nil {
			t.Fatalf("rebuilt history for %q: %v", value.ID, err)
		}
		if len(records) != int(value.Revision) {
			t.Fatalf("rebuilt Row %q has %d history records, want %d",
				value.ID, len(records), value.Revision)
		}
	}
	_ = table
}

// TestATornFinalFrameStopsTheReplayWithoutFailing pins where a crash mid-append
// leaves the log.
//
// The frame is written before the record store marks the transaction committed,
// so a half-written frame belongs to a transaction that was never committed.
// Replay stops there. Reporting it as damage would make an ordinary crash look
// like a broken Database; skipping past it would be worse still, because the
// frames after a torn one are not there to skip to.
func TestATornFinalFrameStopsTheReplayWithoutFailing(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	logDirectory := filepath.Join(directory, "binlog")
	log, err := binlog.Open(logDirectory)
	if err != nil {
		t.Fatal(err)
	}
	for index := 1; index <= 3; index++ {
		if err := log.Append(binlog.Entry{
			TransactionID: string(rune('a' + index)),
			Records: []binlog.Record{{
				Kind: uint16(nativestore.ObjectKindOpaque), SchemaVersion: 1,
				ID: string(rune('a' + index)), Payload: []byte("payload"),
			}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	// Tear the tail, as a crash part-way through an append would.
	entries, err := os.ReadDir(logDirectory)
	if err != nil || len(entries) != 1 {
		t.Fatalf("binlog directory = %v, %v", entries, err)
	}
	path := filepath.Join(logDirectory, entries[0].Name())
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, info.Size()-4); err != nil {
		t.Fatal(err)
	}

	reopened, err := binlog.Open(logDirectory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	seen := 0
	if err := reopened.Replay(func(binlog.Entry) error { seen++; return nil }); err != nil {
		t.Fatalf("a torn final frame must stop the replay, not fail it: %v", err)
	}
	if seen != 2 {
		t.Fatalf("replayed %d frames, want the 2 that were written whole", seen)
	}
}

// TestACorruptFrameIsReportedRatherThanReplayed separates damage from a torn
// tail: a frame that is complete but does not check out is not something a
// crash produces, and replaying it would write bytes nobody committed.
func TestACorruptFrameIsReportedRatherThanReplayed(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	logDirectory := filepath.Join(directory, "binlog")
	log, err := binlog.Open(logDirectory)
	if err != nil {
		t.Fatal(err)
	}
	for index := 1; index <= 2; index++ {
		if err := log.Append(binlog.Entry{
			TransactionID: string(rune('a' + index)),
			Records: []binlog.Record{{
				Kind: uint16(nativestore.ObjectKindOpaque), SchemaVersion: 1,
				ID: string(rune('a' + index)), Payload: []byte("payload"),
			}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(logDirectory)
	path := filepath.Join(logDirectory, entries[0].Name())
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Flip a byte inside the first frame's body, leaving every length intact.
	content[20] ^= 0xff
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}

	reopened, err := binlog.Open(logDirectory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	err = reopened.Replay(func(binlog.Entry) error { return nil })
	if !errors.Is(err, binlog.ErrCorrupt) {
		t.Fatalf("Replay(corrupt) = %v, want ErrCorrupt", err)
	}
}

func writeFixture(t *testing.T, file *nativestore.File) catalog.Table {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	databases := []catalog.Database{{
		ID: "db_work", Name: "work", Purpose: "Work", Scope: "Projects", SchemaVersion: 1,
		CreatedAt: now, UpdatedAt: now,
		Tables: []catalog.Table{{
			ID: "tbl_notes", DatabaseID: "db_work", Name: "notes", Purpose: "Notes",
			RowSemantics: "One note", SchemaVersion: 1, CreatedAt: now, UpdatedAt: now,
			Columns: []catalog.Column{{
				ID: "col_title", Name: "title", Type: "TEXT", MaxCharacters: 100,
				Purpose: "Title", SchemaVersion: 1, CreatedAt: now, UpdatedAt: now,
			}},
		}},
	}}
	if err := nativecatalog.New(file).Write(nil, databases); err != nil {
		t.Fatal(err)
	}
	table := databases[0].Tables[0]
	rows := nativerow.New(file)
	dictionary := nativecatalog.NewService(nativecatalog.New(file), nativecatalog.ServiceOptions{})
	service := nativerow.NewService(rows, dictionary, nativerow.ServiceOptions{})
	inserted, err := service.Insert(ctx, "work", "notes", map[string]any{"title": "first"}, row.WriteOptions{
		ExpectedSchemaVersion: table.SchemaVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Update(ctx, "work", "notes", inserted.ID, map[string]any{"title": "second"}, row.WriteOptions{
		ExpectedSchemaVersion: table.SchemaVersion, ExpectedRevision: inserted.Revision,
	}); err != nil {
		t.Fatal(err)
	}
	return table
}

func assertSameRecords(t *testing.T, source, target *nativestore.File) {
	t.Helper()
	original, err := source.Records()
	if err != nil {
		t.Fatal(err)
	}
	rebuilt, err := target.Records()
	if err != nil {
		t.Fatal(err)
	}
	if len(original) != len(rebuilt) {
		t.Fatalf("rebuilt Database holds %d records, source holds %d", len(rebuilt), len(original))
	}
	for index := range original {
		if original[index].Kind != rebuilt[index].Kind || original[index].ID != rebuilt[index].ID {
			t.Fatalf("record %d = %v/%q, want %v/%q", index,
				rebuilt[index].Kind, rebuilt[index].ID, original[index].Kind, original[index].ID)
		}
		want, err := source.Get(original[index].Kind, original[index].ID)
		if err != nil {
			t.Fatal(err)
		}
		got, err := target.Get(rebuilt[index].Kind, rebuilt[index].ID)
		if err != nil {
			t.Fatalf("rebuilt Database is missing %q: %v", rebuilt[index].ID, err)
		}
		if string(got) != string(want) {
			t.Fatalf("record %q differs after replay", original[index].ID)
		}
	}
}
