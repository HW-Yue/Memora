package instance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/testkit"
)

const testInstanceID = "018f2f7e-7b5d-7c31-8a29-53f27d8f93c1"

func TestDefaultLocations(t *testing.T) {
	t.Parallel()

	home := filepath.Join(string(filepath.Separator), "Users", "memora-test")
	locations, err := DefaultLocations(home, "default", "")
	if err != nil {
		t.Fatalf("DefaultLocations() error = %v", err)
	}
	if got, want := locations.DataDir, filepath.Join(home, "Library", "Application Support", "Memora", "instances", "default"); got != want {
		t.Fatalf("DataDir = %q, want %q", got, want)
	}
	if got, want := locations.CacheDir, filepath.Join(home, "Library", "Caches", "Memora"); got != want {
		t.Fatalf("CacheDir = %q, want %q", got, want)
	}
	if got, want := locations.LogDir, filepath.Join(home, "Library", "Logs", "Memora"); got != want {
		t.Fatalf("LogDir = %q, want %q", got, want)
	}
}

func TestLocationsRequireAbsolutePaths(t *testing.T) {
	t.Parallel()

	if _, err := DefaultLocations("relative-home", "default", ""); !errors.Is(err, ErrPathNotAbsolute) {
		t.Fatalf("relative home error = %v, want %v", err, ErrPathNotAbsolute)
	}
	if _, err := DefaultLocations("/Users/test", "default", "relative-data"); !errors.Is(err, ErrPathNotAbsolute) {
		t.Fatalf("relative override error = %v, want %v", err, ErrPathNotAbsolute)
	}
}

func TestInitializeCreatesInstanceAtomically(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "instance")
	createdAt := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	result, err := Initialize(context.Background(), root, Options{
		Clock:    testkit.NewFakeClock(createdAt),
		IDs:      testkit.NewFakeIDs(testInstanceID),
		PageSize: 16 * 1024,
	})
	if err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	if !result.Created {
		t.Fatal("Initialize() Created = false, want true")
	}
	if result.Metadata.InstanceID != testInstanceID {
		t.Fatalf("InstanceID = %q, want %q", result.Metadata.InstanceID, testInstanceID)
	}
	if !result.Metadata.CreatedAt.Equal(createdAt) {
		t.Fatalf("CreatedAt = %v, want %v", result.Metadata.CreatedAt, createdAt)
	}
	if result.Metadata.PageSize != 16*1024 {
		t.Fatalf("PageSize = %d, want %d", result.Metadata.PageSize, 16*1024)
	}

	for _, name := range []string{"system", "databases", "redo", "undo", "binlog", "tmp"} {
		assertMode(t, filepath.Join(root, name), os.ModeDir|0o700)
	}
	assertMode(t, root, os.ModeDir|0o700)
	assertMode(t, filepath.Join(root, metadataFilename), 0o600)

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	for _, entry := range entries {
		if entry.Name() != metadataFilename && entry.Name()[0] == '.' {
			t.Errorf("temporary initialization file remains: %q", entry.Name())
		}
	}
}

func TestInitializeIsIdempotent(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "instance")
	firstTime := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	first, err := Initialize(context.Background(), root, Options{
		Clock: testkit.NewFakeClock(firstTime),
		IDs:   testkit.NewFakeIDs(testInstanceID),
	})
	if err != nil {
		t.Fatalf("first Initialize() error = %v", err)
	}
	second, err := Initialize(context.Background(), root, Options{
		Clock: testkit.NewFakeClock(firstTime.Add(time.Hour)),
		IDs:   testkit.NewFakeIDs("01911111-1111-7111-8111-111111111111"),
	})
	if err != nil {
		t.Fatalf("second Initialize() error = %v", err)
	}
	if second.Created {
		t.Fatal("second Initialize() Created = true, want false")
	}
	if second.Metadata != first.Metadata {
		t.Fatalf("second metadata = %#v, want %#v", second.Metadata, first.Metadata)
	}
}

func TestInitializeRejectsCorruptMetadata(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "instance")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	path := filepath.Join(root, metadataFilename)
	want := []byte("not-valid-metadata")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	_, err := Initialize(context.Background(), root, Options{
		Clock: testkit.NewFakeClock(time.Now()),
		IDs:   testkit.NewFakeIDs(testInstanceID),
	})
	if !errors.Is(err, ErrCorruptMetadata) {
		t.Fatalf("Initialize() error = %v, want %v", err, ErrCorruptMetadata)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("ReadFile() error = %v", readErr)
	}
	if string(got) != string(want) {
		t.Fatalf("corrupt metadata was overwritten: got %q, want %q", got, want)
	}
}

func TestInitializeDoesNotCreateCacheOrLogs(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	locations, err := DefaultLocations(home, "default", "")
	if err != nil {
		t.Fatalf("DefaultLocations() error = %v", err)
	}
	_, err = Initialize(context.Background(), locations.DataDir, Options{
		Clock: testkit.NewFakeClock(time.Now()),
		IDs:   testkit.NewFakeIDs(testInstanceID),
	})
	if err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	for _, path := range []string{locations.CacheDir, locations.LogDir} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Stat(%q) error = %v, want not exist", path, err)
		}
	}
}

func TestInitializeRejectsChecksumMismatch(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "instance")
	_, err := Initialize(context.Background(), root, Options{
		Clock: testkit.NewFakeClock(time.Now()),
		IDs:   testkit.NewFakeIDs(testInstanceID),
	})
	if err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	path := filepath.Join(root, metadataFilename)
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	encoded[20] ^= 0xff
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	_, err = Initialize(context.Background(), root, Options{})
	if !errors.Is(err, ErrCorruptMetadata) {
		t.Fatalf("Initialize() checksum error = %v, want %v", err, ErrCorruptMetadata)
	}
}

func TestInitializePublishesSingleIdentityConcurrently(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "instance")
	start := make(chan struct{})
	results := make(chan Result, 2)
	errorsFound := make(chan error, 2)
	ids := []string{
		testInstanceID,
		"01911111-1111-7111-8111-111111111111",
	}
	var wait sync.WaitGroup
	for _, id := range ids {
		id := id
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			result, err := Initialize(context.Background(), root, Options{
				Clock: testkit.NewFakeClock(time.Now()),
				IDs:   testkit.NewFakeIDs(id),
			})
			if err != nil {
				errorsFound <- err
				return
			}
			results <- result
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	close(errorsFound)
	for err := range errorsFound {
		t.Fatalf("concurrent Initialize() error = %v", err)
	}

	var first Metadata
	created := 0
	count := 0
	for result := range results {
		count++
		if result.Created {
			created++
		}
		if count == 1 {
			first = result.Metadata
		} else if result.Metadata != first {
			t.Fatalf("concurrent metadata differs: %#v and %#v", first, result.Metadata)
		}
	}
	if count != 2 || created != 1 {
		t.Fatalf("results = %d, created = %d; want 2 and 1", count, created)
	}
}

func TestInitializeCanceledContextDoesNotWrite(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "instance")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Initialize(ctx, root, Options{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Initialize() error = %v, want %v", err, context.Canceled)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled initialization created root: %v", err)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%q) error = %v", path, err)
	}
	got := info.Mode() & (os.ModeType | os.ModePerm)
	if got != want {
		t.Fatalf("mode for %q = %v, want %v", path, got, want)
	}
}
