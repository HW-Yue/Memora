package nativemigration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/HW-Yue/Memora/internal/nativesnapshot"
	"github.com/HW-Yue/Memora/internal/snapshot"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
	sqlitestore "github.com/HW-Yue/Memora/internal/store/sqlite"
)

const (
	NativeFilename = "database.memora"
	LegacyFilename = "prototype.sqlite"
)

type Result struct {
	File       *nativestore.File
	Created    bool
	Migrated   bool
	LegacyPath string
	BackupPath string
}

// OpenDefault opens the native authority, creating it or migrating the legacy
// SQLite authority through a verified logical snapshot. The legacy file and a
// byte-for-byte backup are retained; publication is an atomic rename.
func OpenDefault(ctx context.Context, dataDir string) (Result, error) {
	if !filepath.IsAbs(dataDir) {
		return Result{}, fmt.Errorf("native migration data directory must be absolute")
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
	if _, err := os.Stat(legacyPath); errors.Is(err, os.ErrNotExist) {
		file, createErr := nativestore.Create(nativePath, nativestore.FileKindDatabase)
		return Result{File: file, Created: createErr == nil, LegacyPath: legacyPath}, createErr
	} else if err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	backupPath := legacyPath + ".pre-native.bak"
	if err := copyExclusive(legacyPath, backupPath); err != nil && !errors.Is(err, os.ErrExist) {
		return Result{}, fmt.Errorf("backup legacy authority: %w", err)
	}
	legacy, err := sqlitestore.Open(legacyPath)
	if err != nil {
		return Result{}, err
	}
	exported, exportErr := snapshot.New(legacy).Export(ctx)
	closeErr := legacy.Close()
	if exportErr != nil || closeErr != nil {
		return Result{}, errors.Join(exportErr, closeErr)
	}
	wantHash, err := snapshot.CanonicalHash(exported)
	if err != nil {
		return Result{}, err
	}
	temporary, err := os.CreateTemp(directory, ".database.memora-migrate-")
	if err != nil {
		return Result{}, err
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		return Result{}, err
	}
	_ = os.Remove(temporaryPath)
	defer func() { _ = os.Remove(temporaryPath) }()
	file, err := nativestore.Create(temporaryPath, nativestore.FileKindDatabase)
	if err != nil {
		return Result{}, err
	}
	if err := nativesnapshot.NewNative(file).Import(exported); err != nil {
		_ = file.Close()
		return Result{}, err
	}
	verified, err := nativesnapshot.NewNative(file).Export()
	if err != nil {
		_ = file.Close()
		return Result{}, err
	}
	gotHash, err := snapshot.CanonicalHash(verified)
	if err != nil || gotHash != wantHash {
		_ = file.Close()
		return Result{}, fmt.Errorf("native migration verification failed")
	}
	if err := file.Close(); err != nil {
		return Result{}, err
	}
	if err := os.Rename(temporaryPath, nativePath); err != nil {
		return Result{}, err
	}
	if err := syncDirectory(directory); err != nil {
		return Result{}, err
	}
	file, err = nativestore.Open(nativePath)
	return Result{File: file, Migrated: err == nil, LegacyPath: legacyPath, BackupPath: backupPath}, err
}

func copyExclusive(source, target string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	remove := true
	defer func() {
		_ = output.Close()
		if remove {
			_ = os.Remove(target)
		}
	}()
	if _, err := io.Copy(output, input); err != nil {
		return err
	}
	if err := output.Sync(); err != nil {
		return err
	}
	remove = false
	return output.Close()
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return directory.Sync()
}
