package nativemigration_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/binlog"
	"github.com/HW-Yue/Memora/internal/nativemigration"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

// TestProductionOpenWritesABinlogThatRebuildsTheDatabase is E6 stage 4's gate.
//
// The binlog was implemented and proven able to rebuild a Database, but nothing
// in production attached it: a database opened by the daemon wrote no log at
// all. A log that exists only in tests recovers nothing.
//
// It stayed unattached deliberately while PITR, replication and backup were out
// of scope — the log costs roughly its own size again on disk (measured: 293,869
// bytes of log against a 327,623 byte record file) and bought nothing, because
// the record file was already a complete recovery source. Those three are now in
// scope, and what they need is exactly what the record file cannot give them: a
// separable, complete sequence of changes. So the log is attached.
//
// The property is the separability itself. Not "a log file appeared" — that is
// satisfied by a log that is missing half the writes. The record file is deleted
// and the Database rebuilt from the log alone, then compared record for record.
//
// See docs/storage/three-logs-v1.md §5.4.
func TestProductionOpenWritesABinlogThatRebuildsTheDatabase(t *testing.T) {
	dataDir := t.TempDir()
	opened, err := nativemigration.OpenDefault(context.Background(), dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if opened.Binlog == nil {
		t.Fatal("a production open attached no binlog")
	}
	for index := 0; index < 20; index++ {
		transaction, err := opened.File.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := transaction.Put(
			nativestore.ObjectKindOpaque, 1,
			fmt.Sprintf("obj_%03d", index),
			[]byte(fmt.Sprintf("body %03d", index)),
		); err != nil {
			t.Fatal(err)
		}
		if err := transaction.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	original, err := opened.File.Records()
	if err != nil {
		t.Fatal(err)
	}
	// Captured before the handle closes. Reading them afterwards would always
	// fail, and a comparison that skips on failure asserts nothing.
	payloads := make(map[string][]byte, len(original))
	for _, record := range original {
		payload, err := opened.File.Get(record.Kind, record.ID)
		if err != nil {
			t.Fatal(err)
		}
		payloads[record.ID] = payload
	}
	if err := opened.File.Close(); err != nil {
		t.Fatal(err)
	}
	if err := opened.Binlog.Close(); err != nil {
		t.Fatal(err)
	}

	// The record file is deleted outright. Anything the rebuild produces comes
	// from the log and nowhere else.
	recordPath := filepath.Join(dataDir, "databases", nativemigration.NativeFilename)
	if err := os.Remove(recordPath); err != nil {
		t.Fatal(err)
	}
	log, err := binlog.Open(nativemigration.BinlogDirectory(dataDir))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	rebuiltPath := filepath.Join(t.TempDir(), "rebuilt.memora")
	rebuilt, err := nativestore.Create(rebuiltPath, nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	defer rebuilt.Close()
	if err := binlog.ReplayInto(log, rebuilt); err != nil {
		t.Fatalf("rebuild from the binlog alone: %v", err)
	}
	restored, err := rebuilt.Records()
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != len(original) {
		t.Fatalf("rebuilt holds %d records, original held %d", len(restored), len(original))
	}
	for index := range original {
		if restored[index].Kind != original[index].Kind ||
			restored[index].ID != original[index].ID ||
			restored[index].SchemaVersion != original[index].SchemaVersion ||
			restored[index].PayloadLength != original[index].PayloadLength {
			t.Fatalf("record %d differs:\n original = %#v\n rebuilt  = %#v",
				index, original[index], restored[index])
		}
		// Identity matching is not enough: two records can agree on kind, ID
		// and length and still hold different bytes.
		got, err := rebuilt.Get(restored[index].Kind, restored[index].ID)
		if err != nil {
			t.Fatalf("rebuilt Get(%q) = %v", restored[index].ID, err)
		}
		if want := payloads[original[index].ID]; string(got) != string(want) {
			t.Fatalf("record %q payload differs: %q vs %q", original[index].ID, want, got)
		}
	}
}

// TestReopeningKeepsAppendingToTheSameBinlog pins that the log is one sequence
// across restarts. A log that restarted with the Database would describe only
// the latest run, which is no use to anything that wants to replay history.
func TestReopeningKeepsAppendingToTheSameBinlog(t *testing.T) {
	dataDir := t.TempDir()
	write := func(id string) {
		t.Helper()
		opened, err := nativemigration.OpenDefault(context.Background(), dataDir)
		if err != nil {
			t.Fatal(err)
		}
		transaction, err := opened.File.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := transaction.Put(nativestore.ObjectKindOpaque, 1, id, []byte(id)); err != nil {
			t.Fatal(err)
		}
		if err := transaction.Commit(); err != nil {
			t.Fatal(err)
		}
		if err := opened.File.Close(); err != nil {
			t.Fatal(err)
		}
		if err := opened.Binlog.Close(); err != nil {
			t.Fatal(err)
		}
	}
	write("obj_first")
	write("obj_second")

	log, err := binlog.Open(nativemigration.BinlogDirectory(dataDir))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	seen := make([]string, 0, 2)
	if err := log.Replay(func(entry binlog.Entry) error {
		for _, record := range entry.Records {
			seen = append(seen, record.ID)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != "obj_first" || seen[1] != "obj_second" {
		t.Fatalf("binlog after two runs = %v, want both writes in order", seen)
	}
}
