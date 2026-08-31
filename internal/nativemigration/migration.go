package nativemigration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/HW-Yue/Memora/internal/binlog"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

const (
	NativeFilename = "database.memora"
	LegacyFilename = "prototype.sqlite"
	// BinlogDirname holds the binlog, beside the record file it describes. It
	// is a directory rather than a file because the log rolls.
	BinlogDirname = "binlog"
)

// BinlogDirectory is where a data directory keeps its binlog.
func BinlogDirectory(dataDir string) string {
	return filepath.Join(dataDir, "databases", BinlogDirname)
}

var ErrLegacyMigrationRequired = errors.New("legacy SQLite authority requires the isolated compatibility migrator")

type Result struct {
	File *nativestore.File
	// Binlog is the log every committed transaction is also written to. The
	// caller owns closing it, the same way it owns closing File.
	Binlog     *binlog.Log
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
		log, err := attachBinlog(dataDir, file)
		if err != nil {
			_ = file.Close()
			return Result{}, err
		}
		return Result{File: file, Binlog: log, LegacyPath: legacyPath}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Result{}, err
	}
	if _, err := os.Stat(legacyPath); err == nil {
		return Result{LegacyPath: legacyPath}, ErrLegacyMigrationRequired
	} else if !errors.Is(err, os.ErrNotExist) {
		return Result{}, err
	}
	file, err := nativestore.Create(nativePath, nativestore.FileKindDatabase)
	if err != nil {
		return Result{}, err
	}
	log, err := attachBinlog(dataDir, file)
	if err != nil {
		_ = file.Close()
		return Result{}, err
	}
	return Result{File: file, Binlog: log, Created: true, LegacyPath: legacyPath}, nil
}

// attachBinlog opens the binlog and points the record store at it, so every
// transaction this process commits is also written to the log.
//
// The log is opened in append mode and shared across restarts: it is one
// sequence describing the Database's whole life, not one per run. That is the
// only form anything downstream — a restore to a point in time, a replica, a
// backup — can use.
//
// It costs roughly the record file's size again on disk. That is the price of
// the log being separable from the file it describes, which is the whole reason
// to have it. See docs/storage/three-logs-v1.md §5.4.
func attachBinlog(dataDir string, file *nativestore.File) (*binlog.Log, error) {
	log, err := binlog.Open(BinlogDirectory(dataDir))
	if err != nil {
		return nil, fmt.Errorf("open binlog: %w", err)
	}
	file.AttachBinlog(binlog.NewSink(log))
	return log, nil
}
