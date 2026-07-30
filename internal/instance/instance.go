package instance

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	metadataFilename             = "instance.meta"
	metadataSize                 = 44
	FormatVersion                = uint32(2)
	MinimumUpgradableVersion     = uint32(1)
	DefaultPageSize              = uint32(16 * 1024)
	MigrationJournalRelativePath = "system/format-migration.json"
)

var (
	ErrPathNotAbsolute     = errors.New("instance path is not absolute")
	ErrCorruptMetadata     = errors.New("instance metadata is corrupt")
	ErrUpgradeRequired     = errors.New("instance format upgrade is required")
	ErrNewerFormat         = errors.New("instance format is newer than this binary")
	ErrMigrationIncomplete = errors.New("instance format migration is incomplete")
	metadataMagic          = [8]byte{'M', 'E', 'M', 'O', 'R', 'A', 0, 1}
)

var instanceDirectories = [...]string{
	"system",
	"databases",
	"redo",
	"undo",
	"binlog",
	"tmp",
}

type Locations struct {
	DataDir  string
	CacheDir string
	LogDir   string
}

func DefaultLocations(home, instanceName, dataDirOverride string) (Locations, error) {
	if !filepath.IsAbs(home) {
		return Locations{}, ErrPathNotAbsolute
	}
	if instanceName == "" || instanceName == "." || instanceName == ".." || strings.ContainsAny(instanceName, `/\`) {
		return Locations{}, fmt.Errorf("invalid instance name %q", instanceName)
	}
	dataDir := filepath.Join(home, "Library", "Application Support", "Memora", "instances", instanceName)
	if dataDirOverride != "" {
		if !filepath.IsAbs(dataDirOverride) {
			return Locations{}, ErrPathNotAbsolute
		}
		dataDir = filepath.Clean(dataDirOverride)
	}
	return Locations{
		DataDir:  dataDir,
		CacheDir: filepath.Join(home, "Library", "Caches", "Memora"),
		LogDir:   filepath.Join(home, "Library", "Logs", "Memora"),
	}, nil
}

type Clock interface {
	Now() time.Time
}

type IDSource interface {
	Next() (string, error)
}

type Options struct {
	Clock    Clock
	IDs      IDSource
	PageSize uint32
}

type Metadata struct {
	FormatVersion uint32
	PageSize      uint32
	CreatedAt     time.Time
	InstanceID    string
}

type Result struct {
	Metadata Metadata
	Created  bool
}

type CompatibilityStatus string

const (
	CompatibilityCurrent         CompatibilityStatus = "current"
	CompatibilityUpgradeRequired CompatibilityStatus = "upgrade_required"
	CompatibilityNewerFormat     CompatibilityStatus = "newer_format"
)

type Inspection struct {
	Metadata Metadata
	Status   CompatibilityStatus
}

func Read(root string) (Metadata, error) {
	if !filepath.IsAbs(root) {
		return Metadata{}, ErrPathNotAbsolute
	}
	if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(MigrationJournalRelativePath))); err == nil {
		return Metadata{}, ErrMigrationIncomplete
	} else if !errors.Is(err, os.ErrNotExist) {
		return Metadata{}, fmt.Errorf("inspect instance migration journal: %w", err)
	}
	inspection, err := Inspect(root)
	if err != nil {
		return Metadata{}, err
	}
	switch inspection.Status {
	case CompatibilityCurrent:
		return inspection.Metadata, nil
	case CompatibilityUpgradeRequired:
		return Metadata{}, ErrUpgradeRequired
	case CompatibilityNewerFormat:
		return Metadata{}, ErrNewerFormat
	default:
		return Metadata{}, ErrCorruptMetadata
	}
}

func Inspect(root string) (Inspection, error) {
	if !filepath.IsAbs(root) {
		return Inspection{}, ErrPathNotAbsolute
	}
	metadata, err := readMetadata(filepath.Join(root, metadataFilename))
	if err != nil {
		return Inspection{}, err
	}
	status := CompatibilityCurrent
	if metadata.FormatVersion < FormatVersion {
		if metadata.FormatVersion < MinimumUpgradableVersion {
			return Inspection{}, ErrCorruptMetadata
		}
		status = CompatibilityUpgradeRequired
	} else if metadata.FormatVersion > FormatVersion {
		status = CompatibilityNewerFormat
	}
	return Inspection{Metadata: metadata, Status: status}, nil
}

func Initialize(ctx context.Context, root string, options Options) (Result, error) {
	if !filepath.IsAbs(root) {
		return Result{}, ErrPathNotAbsolute
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	options = withDefaults(options)
	if err := validatePageSize(options.PageSize); err != nil {
		return Result{}, err
	}

	metadataPath := filepath.Join(root, metadataFilename)
	existing, err := Read(root)
	if err == nil {
		if err := createDirectories(ctx, root); err != nil {
			return Result{}, err
		}
		return Result{Metadata: existing, Created: false}, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return Result{}, err
	}

	if err := createDirectories(ctx, root); err != nil {
		return Result{}, err
	}
	id, err := options.IDs.Next()
	if err != nil {
		return Result{}, fmt.Errorf("allocate instance ID: %w", err)
	}
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return Result{}, fmt.Errorf("parse instance ID: %w", err)
	}
	metadata := Metadata{
		FormatVersion: FormatVersion,
		PageSize:      options.PageSize,
		CreatedAt:     options.Clock.Now().UTC(),
		InstanceID:    parsedID.String(),
	}
	encoded := encodeMetadata(metadata, parsedID)
	created, err := publishMetadata(ctx, root, metadataPath, encoded)
	if err != nil {
		return Result{}, err
	}
	if !created {
		metadata, err = readMetadata(metadataPath)
		if err != nil {
			return Result{}, err
		}
	}
	return Result{Metadata: metadata, Created: created}, nil
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

type uuidSource struct{}

func (uuidSource) Next() (string, error) { return uuid.NewString(), nil }

func withDefaults(options Options) Options {
	if options.Clock == nil {
		options.Clock = systemClock{}
	}
	if options.IDs == nil {
		options.IDs = uuidSource{}
	}
	if options.PageSize == 0 {
		options.PageSize = DefaultPageSize
	}
	return options
}

func validatePageSize(size uint32) error {
	if size < 4*1024 || size > 64*1024 || size&(size-1) != 0 {
		return fmt.Errorf("invalid page size %d: must be a power of two between 4096 and 65536", size)
	}
	return nil
}

func createDirectories(ctx context.Context, root string) error {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("create instance root: %w", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return fmt.Errorf("secure instance root: %w", err)
	}
	for _, name := range instanceDirectories {
		if err := ctx.Err(); err != nil {
			return err
		}
		path := filepath.Join(root, name)
		if err := os.MkdirAll(path, 0o700); err != nil {
			return fmt.Errorf("create instance directory %q: %w", name, err)
		}
		if err := os.Chmod(path, 0o700); err != nil {
			return fmt.Errorf("secure instance directory %q: %w", name, err)
		}
	}
	return nil
}

func encodeMetadata(metadata Metadata, id uuid.UUID) []byte {
	encoded := make([]byte, metadataSize)
	copy(encoded[0:8], metadataMagic[:])
	binary.LittleEndian.PutUint32(encoded[8:12], metadata.FormatVersion)
	binary.LittleEndian.PutUint32(encoded[12:16], metadata.PageSize)
	binary.LittleEndian.PutUint64(encoded[16:24], uint64(metadata.CreatedAt.UnixNano()))
	copy(encoded[24:40], id[:])
	binary.LittleEndian.PutUint32(encoded[40:44], crc32.ChecksumIEEE(encoded[:40]))
	return encoded
}

func readMetadata(path string) (Metadata, error) {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return Metadata{}, err
	}
	if len(encoded) != metadataSize || !bytes.Equal(encoded[:8], metadataMagic[:]) {
		return Metadata{}, ErrCorruptMetadata
	}
	wantChecksum := binary.LittleEndian.Uint32(encoded[40:44])
	if got := crc32.ChecksumIEEE(encoded[:40]); got != wantChecksum {
		return Metadata{}, ErrCorruptMetadata
	}
	formatVersion := binary.LittleEndian.Uint32(encoded[8:12])
	pageSize := binary.LittleEndian.Uint32(encoded[12:16])
	if formatVersion == 0 || validatePageSize(pageSize) != nil {
		return Metadata{}, ErrCorruptMetadata
	}
	id, err := uuid.FromBytes(encoded[24:40])
	if err != nil {
		return Metadata{}, ErrCorruptMetadata
	}
	return Metadata{
		FormatVersion: formatVersion,
		PageSize:      pageSize,
		CreatedAt:     time.Unix(0, int64(binary.LittleEndian.Uint64(encoded[16:24]))).UTC(),
		InstanceID:    id.String(),
	}, nil
}

func RewriteFormatVersion(root string, expected, target uint32) (Metadata, error) {
	if !filepath.IsAbs(root) {
		return Metadata{}, ErrPathNotAbsolute
	}
	if expected != MinimumUpgradableVersion || target != FormatVersion || target != expected+1 {
		return Metadata{}, errors.New("unsupported instance format migration")
	}
	inspection, err := Inspect(root)
	if err != nil {
		return Metadata{}, err
	}
	if inspection.Metadata.FormatVersion != expected {
		return Metadata{}, fmt.Errorf(
			"instance format changed: got %d, expected %d",
			inspection.Metadata.FormatVersion,
			expected,
		)
	}
	id, err := uuid.Parse(inspection.Metadata.InstanceID)
	if err != nil {
		return Metadata{}, ErrCorruptMetadata
	}
	metadata := inspection.Metadata
	metadata.FormatVersion = target
	if err := replaceMetadata(root, encodeMetadata(metadata, id)); err != nil {
		return Metadata{}, err
	}
	return metadata, nil
}

func replaceMetadata(root string, encoded []byte) error {
	temporary, err := os.CreateTemp(root, ".instance.meta-migrate-")
	if err != nil {
		return fmt.Errorf("create migrated instance metadata: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure migrated instance metadata: %w", err)
	}
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write migrated instance metadata: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync migrated instance metadata: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close migrated instance metadata: %w", err)
	}
	if err := os.Rename(temporaryPath, filepath.Join(root, metadataFilename)); err != nil {
		return fmt.Errorf("publish migrated instance metadata: %w", err)
	}
	return syncDirectory(root)
}

func publishMetadata(ctx context.Context, root, destination string, encoded []byte) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	temporary, err := os.CreateTemp(root, ".instance.meta-")
	if err != nil {
		return false, fmt.Errorf("create temporary instance metadata: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()

	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return false, fmt.Errorf("secure temporary instance metadata: %w", err)
	}
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		return false, fmt.Errorf("write temporary instance metadata: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return false, fmt.Errorf("sync temporary instance metadata: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return false, fmt.Errorf("close temporary instance metadata: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := os.Link(temporaryPath, destination); err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, nil
		}
		return false, fmt.Errorf("publish instance metadata: %w", err)
	}
	if err := syncDirectory(root); err != nil {
		return false, err
	}
	return true, nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open instance root for sync: %w", err)
	}
	defer func() { _ = directory.Close() }()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync instance root: %w", err)
	}
	return nil
}
