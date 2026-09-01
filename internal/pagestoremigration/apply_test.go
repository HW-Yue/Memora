package pagestoremigration_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/nativecatalog"
	"github.com/HW-Yue/Memora/internal/nativerow"
	"github.com/HW-Yue/Memora/internal/pagestoremigration"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/store/catalogindex"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

func TestApplierPublishesReopenableFourTreeGenerationWithoutChangingNativeBodies(t *testing.T) {
	directory := t.TempDir()
	sourcePath := filepath.Join(directory, "database.memora")
	file, first, second := migrationFixtureAt(t, sourcePath)
	reader, err := pagestoremigration.NewNativeReader(file)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := reader.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}

	applier, err := pagestoremigration.NewApplier(reader, directory)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := applier.Apply(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if !receipt.Published || receipt.Reused || receipt.PlanDigest != plan.Digest ||
		receipt.Directory != filepath.Join(directory, pagestoremigration.GenerationDirectory) {
		t.Fatalf("Apply() receipt = %+v", receipt)
	}
	after, err := os.ReadFile(sourcePath)
	if err != nil || !bytes.Equal(after, before) {
		t.Fatalf("Apply changed legacy source: %v", err)
	}

	generation, err := pagestoremigration.OpenGeneration(receipt.Directory)
	if err != nil {
		t.Fatal(err)
	}
	defer generation.Close()
	if generation.PlanDigest() != plan.Digest || generation.SourceFingerprint() != plan.SourceFingerprint {
		t.Fatalf("Generation binding = %q / %q", generation.PlanDigest(), generation.SourceFingerprint())
	}
	if generation.Fulltext() == nil {
		t.Fatal("Generation is missing Fulltext Tree")
	}
	database, err := generation.Catalog().DatabaseByID(plan.Catalog[0].ID)
	if err != nil || database.ID != plan.Catalog[0].ID {
		t.Fatalf("Catalog Database = %+v, %v", database, err)
	}
	current, err := generation.CurrentRowsFor(second.TableID).Lookup(second.ID)
	if err != nil || current.Revision != second.Revision {
		t.Fatalf("Current Row = %+v, %v", current, err)
	}
	version, err := generation.RowVersions().ByRevision(first.ID, first.Revision)
	if err != nil || version.Revision != first.Revision {
		t.Fatalf("Row version = %+v, %v", version, err)
	}
	highWater, err := generation.RowVersions().HighWater()
	if err != nil || highWater != second.CommitSequence {
		t.Fatalf("version high-water = %d, %v", highWater, err)
	}
	if countFor(plan.RecordCounts, nativestore.ObjectKindRow) != 2 {
		t.Fatal("fixture Plan lost Row revisions")
	}
}

func TestEmptyGenerationAndRepeatedApplyAreValidAndIdempotent(t *testing.T) {
	directory := t.TempDir()
	file, err := nativestore.Create(filepath.Join(directory, "database.memora"), nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader, _ := pagestoremigration.NewNativeReader(file)
	plan, err := reader.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	applier, _ := pagestoremigration.NewApplier(reader, directory)
	first, err := applier.Apply(context.Background(), plan)
	if err != nil || !first.Published {
		t.Fatalf("first empty Apply() = %+v, %v", first, err)
	}
	second, err := applier.Apply(context.Background(), plan)
	if err != nil || !second.Reused || second.Published {
		t.Fatalf("second empty Apply() = %+v, %v", second, err)
	}
	generation, err := pagestoremigration.OpenGeneration(first.Directory)
	if err != nil {
		t.Fatal(err)
	}
	defer generation.Close()
	if highWater, err := generation.RowVersions().HighWater(); err != nil || highWater != 0 {
		t.Fatalf("empty high-water = %d, %v", highWater, err)
	}
	if _, err := generation.Catalog().DatabaseByID("db_missing"); !errors.Is(err, catalogindex.ErrNotFound) {
		t.Fatalf("empty Catalog lookup error = %v", err)
	}
	// A Table with no Tree locates nothing rather than failing: that is the
	// state of a Catalog entry nothing has ever written to.
	if index := generation.CurrentRowsFor("tbl_missing"); index != nil {
		t.Fatalf("a Table with no Tree returned an Index: %#v", index)
	}
	if _, err := generation.CurrentRowsFor("tbl_missing").Lookup("row_missing"); err == nil {
		t.Fatal("looking a Row up in a Table with no Tree must fail")
	}
}

func TestLargeGenerationMatchesEveryPlannedLocatorAfterReopen(t *testing.T) {
	directory := t.TempDir()
	sourcePath := filepath.Join(directory, "database.memora")
	file, err := nativestore.Create(sourcePath, nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	databases, _, _ := migrationValues()
	if err := nativecatalog.New(file).Write(nil, databases); err != nil {
		t.Fatal(err)
	}
	repository := nativerow.New(file)
	transaction, err := file.Begin()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	table := databases[0].Tables[0]
	for number := 0; number < 1000; number++ {
		value := row.Row{
			ID: fmt.Sprintf("row_%04d", number), DatabaseID: databases[0].ID, TableID: table.ID,
			SchemaVersion: table.SchemaVersion, Revision: 1, CommitSequence: uint64(number + 1),
			State: row.StateLive, Values: map[string]any{"col_title": fmt.Sprintf("note-%d", number)},
			CreatedAt: now, UpdatedAt: now,
		}
		if err := repository.StageSnapshotRow(transaction, value, table); err != nil {
			t.Fatal(err)
		}
		if number%5 == 0 {
			value.Revision, value.CommitSequence = 2, uint64(10000+number)
			value.State, value.UpdatedAt = row.StateDeleted, now.Add(time.Minute)
			if err := repository.StageSnapshotRow(transaction, value, table); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(sourcePath)
	reader, _ := pagestoremigration.NewNativeReader(file)
	plan, err := reader.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	applier, _ := pagestoremigration.NewApplier(reader, directory)
	receipt, err := applier.Apply(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	generation, err := pagestoremigration.OpenGeneration(receipt.Directory)
	if err != nil {
		t.Fatal(err)
	}
	defer generation.Close()
	for _, want := range plan.CurrentRows {
		got, err := generation.CurrentRowsFor(want.TableID).Lookup(want.RowID)
		if err != nil || got != want {
			t.Fatalf("current %q = %+v, %v; want %+v", want.RowID, got, err, want)
		}
	}
	for _, want := range plan.RowVersions {
		got, err := generation.RowVersions().ByRevision(want.RowID, want.Revision)
		if err != nil || got != want {
			t.Fatalf("version %q/%d = %+v, %v; want %+v", want.RowID, want.Revision, got, err, want)
		}
	}
	after, _ := os.ReadFile(sourcePath)
	if !bytes.Equal(after, before) {
		t.Fatal("large migration changed legacy source")
	}
}

func TestExistingGenerationConflictsWithANewSourceBoundPlan(t *testing.T) {
	directory := t.TempDir()
	file, _, _ := migrationFixtureAt(t, filepath.Join(directory, "database.memora"))
	reader, _ := pagestoremigration.NewNativeReader(file)
	first, _ := reader.Build(context.Background())
	applier, _ := pagestoremigration.NewApplier(reader, directory)
	if _, err := applier.Apply(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := file.Put(nativestore.ObjectKindConfiguration, 1, "config", []byte("new")); err != nil {
		t.Fatal(err)
	}
	second, err := reader.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := applier.Apply(context.Background(), second); !errors.Is(err, pagestoremigration.ErrConflict) {
		t.Fatalf("different Plan Apply() error = %v", err)
	}
}

func TestOpenGenerationRejectsManifestPageWALAndDirectoryCorruption(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{"manifest", func(t *testing.T, directory string) {
			file, err := os.OpenFile(filepath.Join(directory, "manifest.json"), os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = file.WriteString("{}")
			_ = file.Close()
		}},
		{"Page", func(t *testing.T, directory string) {
			flipLastByte(t, filepath.Join(directory, "catalog.pages"))
		}},
		{"Fulltext Page", func(t *testing.T, directory string) {
			flipLastByte(t, filepath.Join(directory, "fulltext.pages"))
		}},
		// One case, not one per Tree: the whole generation now commits into a
		// single redo log, so there is a single set of Segment files to corrupt.
		{"redo log", func(t *testing.T, directory string) {
			matches, err := filepath.Glob(filepath.Join(directory, "redo.wal", "segment-*.wal"))
			if err != nil || len(matches) != 1 {
				t.Fatalf("redo log files = %v, %v", matches, err)
			}
			flipLastByte(t, matches[0])
		}},
		{"missing redo log", func(t *testing.T, directory string) {
			if err := os.RemoveAll(filepath.Join(directory, "redo.wal")); err != nil {
				t.Fatal(err)
			}
		}},
		{"extra entry", func(t *testing.T, directory string) {
			if err := os.WriteFile(filepath.Join(directory, "unexpected"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			file, _, _ := migrationFixtureAt(t, filepath.Join(directory, "database.memora"))
			reader, _ := pagestoremigration.NewNativeReader(file)
			plan, _ := reader.Build(context.Background())
			applier, _ := pagestoremigration.NewApplier(reader, directory)
			receipt, err := applier.Apply(context.Background(), plan)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(t, receipt.Directory)
			if _, err := pagestoremigration.OpenGeneration(receipt.Directory); !errors.Is(err, pagestoremigration.ErrTargetCorrupt) {
				t.Fatalf("OpenGeneration(corrupt) error = %v", err)
			}
		})
	}
}

func flipLastByte(t *testing.T, path string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() == 0 {
		t.Fatalf("stat %q = %+v, %v", path, info, err)
	}
	var value [1]byte
	if _, err := file.ReadAt(value[:], info.Size()-1); err != nil {
		t.Fatal(err)
	}
	value[0] ^= 0xff
	if _, err := file.WriteAt(value[:], info.Size()-1); err != nil {
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
}
