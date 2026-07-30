package nativemigration_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/nativemigration"
)

func TestOpenDefaultCreatesAndReopensNativeAuthority(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "instance")
	first, err := nativemigration.OpenDefault(context.Background(), root)
	if err != nil || !first.Created {
		t.Fatalf("first OpenDefault() = %#v, %v", first, err)
	}
	_ = first.File.Close()
	second, err := nativemigration.OpenDefault(context.Background(), root)
	if err != nil || second.Created {
		t.Fatalf("second OpenDefault() = %#v, %v", second, err)
	}
	_ = second.File.Close()
}

func TestOpenDefaultRefusesLegacyFallback(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "instance")
	directory := filepath.Join(root, "databases")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(directory, nativemigration.LegacyFilename)
	if err := os.WriteFile(legacy, []byte("legacy authority"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := nativemigration.OpenDefault(context.Background(), root); !errors.Is(err, nativemigration.ErrLegacyMigrationRequired) {
		t.Fatalf("OpenDefault() legacy error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory, nativemigration.NativeFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy refusal published native file: %v", err)
	}
}

func TestCancelledOpenPublishesNothing(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "instance")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := nativemigration.OpenDefault(ctx, root); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled OpenDefault() error = %v", err)
	}
}
