package instance_test

import (
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/HW-Yue/Memora/internal/instance"
)

var metadataMagic = [8]byte{'M', 'E', 'M', 'O', 'R', 'A', 0, 1}

// encodeMetadata mirrors the on-disk layout so the format itself stays pinned:
// magic, format version, page size, creation time, instance ID, checksum.
func encodeMetadata(formatVersion, pageSize uint32, instanceID uuid.UUID, createdUnixNano uint64) []byte {
	encoded := make([]byte, 44)
	copy(encoded[0:8], metadataMagic[:])
	binary.LittleEndian.PutUint32(encoded[8:12], formatVersion)
	binary.LittleEndian.PutUint32(encoded[12:16], pageSize)
	binary.LittleEndian.PutUint64(encoded[16:24], createdUnixNano)
	copy(encoded[24:40], instanceID[:])
	binary.LittleEndian.PutUint32(encoded[40:44], crc32.ChecksumIEEE(encoded[:40]))
	return encoded
}

func writeMetadata(t *testing.T, root string, encoded []byte) {
	t.Helper()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "instance.meta"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}

func newInstance(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "instance")
	if _, err := instance.Initialize(context.Background(), root, instance.Options{}); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestDefaultLocationsResolvesHomeAndGuardsInputs(t *testing.T) {
	home := t.TempDir()
	locations, err := instance.DefaultLocations(home, "default", "")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "Library", "Application Support", "Memora", "instances", "default"); locations.DataDir != want {
		t.Fatalf("DataDir = %q, want %q", locations.DataDir, want)
	}
	if locations.CacheDir == "" || locations.LogDir == "" {
		t.Fatalf("cache and log roots must be resolved too: %+v", locations)
	}

	override := filepath.Join(t.TempDir(), "elsewhere") + string(filepath.Separator)
	locations, err = instance.DefaultLocations(home, "default", override)
	if err != nil {
		t.Fatal(err)
	}
	if locations.DataDir != filepath.Clean(override) {
		t.Fatalf("DataDir override = %q", locations.DataDir)
	}

	if _, err := instance.DefaultLocations("relative/home", "default", ""); !errors.Is(err, instance.ErrPathNotAbsolute) {
		t.Fatalf("a relative home: %v", err)
	}
	if _, err := instance.DefaultLocations(home, "default", "relative/instance"); !errors.Is(err, instance.ErrPathNotAbsolute) {
		t.Fatalf("a relative override: %v", err)
	}
	for _, name := range []string{"", ".", "..", "a/b", `a\b`} {
		if _, err := instance.DefaultLocations(home, name, ""); err == nil {
			t.Fatalf("instance name %q must be refused", name)
		}
	}
}

func TestInitializeCreatesTheLayoutAndIsIdempotent(t *testing.T) {
	root := newInstance(t)

	for _, name := range []string{"system", "databases", "redo", "undo", "binlog", "tmp"} {
		info, err := os.Stat(filepath.Join(root, name))
		if err != nil || !info.IsDir() {
			t.Fatalf("directory %q: %v", name, err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("directory %q mode = %v", name, info.Mode().Perm())
		}
	}
	first, err := instance.Read(root)
	if err != nil {
		t.Fatal(err)
	}
	if first.FormatVersion != instance.FormatVersion || first.InstanceID == "" {
		t.Fatalf("metadata = %+v", first)
	}

	again, err := instance.Initialize(context.Background(), root, instance.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if again.Created {
		t.Fatal("a second Initialize must report Created=false")
	}
	if again.Metadata.InstanceID != first.InstanceID {
		t.Fatalf("Initialize must reuse the instance ID: %q then %q", first.InstanceID, again.Metadata.InstanceID)
	}
}

func TestInitializeRejectsBadInput(t *testing.T) {
	if _, err := instance.Initialize(context.Background(), "relative/instance", instance.Options{}); !errors.Is(err, instance.ErrPathNotAbsolute) {
		t.Fatalf("a relative root: %v", err)
	}
	for _, size := range []uint32{2048, 12288, 128 * 1024} {
		root := filepath.Join(t.TempDir(), "instance")
		if _, err := instance.Initialize(context.Background(), root, instance.Options{PageSize: size}); err == nil {
			t.Fatalf("page size %d must be refused", size)
		}
		if _, err := os.Stat(filepath.Join(root, "instance.meta")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("page size %d wrote metadata anyway: %v", size, err)
		}
	}
}

func TestInitializeHonoursACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	root := filepath.Join(t.TempDir(), "instance")
	if _, err := instance.Initialize(ctx, root, instance.Options{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Initialize: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "instance.meta")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a cancelled Initialize must not publish metadata: %v", err)
	}
}

func TestReadRejectsCorruptMetadata(t *testing.T) {
	id := uuid.New()
	cases := map[string][]byte{
		"garbage":   []byte("not instance metadata at all............."),
		"truncated": encodeMetadata(instance.FormatVersion, instance.DefaultPageSize, id, 1)[:20],
		"bad magic": append([]byte("XXXXXXXX"), encodeMetadata(instance.FormatVersion, instance.DefaultPageSize, id, 1)[8:]...),
	}
	for name, encoded := range cases {
		root := filepath.Join(t.TempDir(), "instance")
		writeMetadata(t, root, encoded)
		if _, err := instance.Read(root); !errors.Is(err, instance.ErrCorruptMetadata) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestReadRejectsAnIncompleteMigration(t *testing.T) {
	root := newInstance(t)
	journal := filepath.Join(root, instance.MigrationJournalRelativePath)
	if err := os.MkdirAll(filepath.Dir(journal), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journal, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.Read(root); !errors.Is(err, instance.ErrMigrationIncomplete) {
		t.Fatalf("a journal left behind must block opening: %v", err)
	}
}

func TestInspectReportsFormatCompatibility(t *testing.T) {
	id := uuid.New()
	cases := []struct {
		name    string
		version uint32
		status  instance.CompatibilityStatus
		readErr error
	}{
		{name: "older", version: 1, status: instance.CompatibilityUpgradeRequired, readErr: instance.ErrUpgradeRequired},
		{name: "current", version: instance.FormatVersion, status: instance.CompatibilityCurrent},
		{name: "newer", version: instance.FormatVersion + 1, status: instance.CompatibilityNewerFormat, readErr: instance.ErrNewerFormat},
	}
	for _, tc := range cases {
		root := filepath.Join(t.TempDir(), "instance")
		writeMetadata(t, root, encodeMetadata(tc.version, instance.DefaultPageSize, id, 1))

		inspection, err := instance.Inspect(root)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if inspection.Status != tc.status {
			t.Fatalf("%s status = %q, want %q", tc.name, inspection.Status, tc.status)
		}
		if _, err := instance.Read(root); tc.readErr == nil && err != nil {
			t.Fatalf("%s Read = %v", tc.name, err)
		} else if tc.readErr != nil && !errors.Is(err, tc.readErr) {
			t.Fatalf("%s Read = %v, want %v", tc.name, err, tc.readErr)
		}
	}

	root := filepath.Join(t.TempDir(), "instance")
	writeMetadata(t, root, encodeMetadata(0, instance.DefaultPageSize, id, 1))
	if _, err := instance.Inspect(root); !errors.Is(err, instance.ErrCorruptMetadata) {
		t.Fatalf("format version zero: %v", err)
	}
}

func TestInitializeRefusesAnInstanceThatNeedsUpgrade(t *testing.T) {
	root := filepath.Join(t.TempDir(), "instance")
	writeMetadata(t, root, encodeMetadata(1, instance.DefaultPageSize, uuid.New(), 1))
	if _, err := instance.Initialize(context.Background(), root, instance.Options{}); !errors.Is(err, instance.ErrUpgradeRequired) {
		t.Fatalf("Initialize over an older format: %v", err)
	}
}

func TestRewriteFormatVersionMovesExactlyOneStep(t *testing.T) {
	root := filepath.Join(t.TempDir(), "instance")
	writeMetadata(t, root, encodeMetadata(1, instance.DefaultPageSize, uuid.New(), 1))
	if _, err := instance.RewriteFormatVersion(root, 2, 3); err == nil {
		t.Fatal("only 1 -> 2 is supported")
	}
	metadata, err := instance.RewriteFormatVersion(root, 1, instance.FormatVersion)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.FormatVersion != instance.FormatVersion {
		t.Fatalf("rewritten metadata = %+v", metadata)
	}
	inspection, err := instance.Inspect(root)
	if err != nil || inspection.Status != instance.CompatibilityCurrent {
		t.Fatalf("after rewrite: %+v, %v", inspection, err)
	}
	if _, err := instance.RewriteFormatVersion(root, 1, instance.FormatVersion); err == nil {
		t.Fatal("rewriting an already migrated instance must fail")
	}
}
