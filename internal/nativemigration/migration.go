package nativemigration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

const (
	NativeFilename = "database.memora"
	LegacyFilename = "prototype.sqlite"
)

var ErrLegacyMigrationRequired = errors.New("legacy SQLite authority requires the isolated compatibility migrator")

type Result struct {
	File       *nativestore.File
	Created    bool
	Migrated   bool
	LegacyPath string
	BackupPath string
}

// OpenDefault opens only the native runtime authority. SQLite parsing is kept
// outside the main module so the daemon cannot silently fall back to it.
func OpenDefault(ctx context.Context, dataDir string) (Result, error) {
	if !filepath.IsAbs(dataDir) {
		return Result{}, fmt.Errorf("native data directory must be absolute")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	directory := filepath.Join(dataDir, "databases")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return Result{}, err
	}
	nativePath := filepath.Join(directory, NativeFilename)
	legacyPath := filepath.Join(directory, LegacyFilename)
	if file, err := nativestore.Open(nativePath); err == nil {
		return Result{File: file, LegacyPath: legacyPath}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Result{}, err
	}
	if _, err := os.Stat(legacyPath); err == nil {
		return Result{LegacyPath: legacyPath}, ErrLegacyMigrationRequired
	} else if !errors.Is(err, os.ErrNotExist) {
		return Result{}, err
	}
	file, err := nativestore.Create(nativePath, nativestore.FileKindDatabase)
	return Result{File: file, Created: err == nil, LegacyPath: legacyPath}, err
}
