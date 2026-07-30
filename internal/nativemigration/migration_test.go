package nativemigration_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/nativemigration"
	"github.com/HW-Yue/Memora/internal/nativesnapshot"
	"github.com/HW-Yue/Memora/internal/snapshot"
	sqlitestore "github.com/HW-Yue/Memora/internal/store/sqlite"
)

func TestOpenDefaultCreatesAndReopensNativeAuthority(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "instance")
	first, err := nativemigration.OpenDefault(context.Background(), root)
	if err != nil || !first.Created || first.Migrated {
		t.Fatalf("first OpenDefault() = %#v, %v", first, err)
	}
	if err := first.File.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := nativemigration.OpenDefault(context.Background(), root)
	if err != nil || second.Created || second.Migrated {
		t.Fatalf("second OpenDefault() = %#v, %v", second, err)
	}
	_ = second.File.Close()
}

func TestOpenDefaultBacksUpMigratesVerifiesAndIsIdempotent(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "instance")
	directory := filepath.Join(root, "databases")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(directory, nativemigration.LegacyFilename)
	legacy, err := sqlitestore.Open(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile(filepath.Join("..", "snapshot", "testdata", "logical-snapshot-v0.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshot.New(legacy).Import(context.Background(), fixture); err != nil {
		t.Fatal(err)
	}
	legacySnapshot, err := snapshot.New(legacy).Export(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(legacyPath)
	result, err := nativemigration.OpenDefault(context.Background(), root)
	if err != nil || !result.Migrated || result.BackupPath == "" {
		t.Fatalf("OpenDefault() = %#v, %v", result, err)
	}
	after, _ := os.ReadFile(legacyPath)
	backup, _ := os.ReadFile(result.BackupPath)
	if !bytes.Equal(before, after) || !bytes.Equal(before, backup) {
		t.Fatal("legacy authority or backup changed during migration")
	}
	exported, err := nativesnapshot.NewNative(result.File).Export()
	if err != nil {
		t.Fatal(err)
	}
	want, _ := snapshot.CanonicalHash(legacySnapshot)
	got, _ := snapshot.CanonicalHash(exported)
	if got != want {
		t.Fatalf("migrated hash = %s, want %s", got, want)
	}
	_ = result.File.Close()
	repeated, err := nativemigration.OpenDefault(context.Background(), root)
	if err != nil || repeated.Migrated {
		t.Fatalf("repeated OpenDefault() = %#v, %v", repeated, err)
	}
	_ = repeated.File.Close()
}

func TestCancelledMigrationPublishesNothing(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "instance")
	directory := filepath.Join(root, "databases")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy, err := sqlitestore.Open(filepath.Join(directory, nativemigration.LegacyFilename))
	if err != nil {
		t.Fatal(err)
	}
	_ = legacy.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := nativemigration.OpenDefault(ctx, root); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled OpenDefault() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory, nativemigration.NativeFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled migration published native authority: %v", err)
	}
}
