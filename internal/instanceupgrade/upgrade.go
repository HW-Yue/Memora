package instanceupgrade

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/HW-Yue/Memora/internal/daemon"
	"github.com/HW-Yue/Memora/internal/instance"
	"github.com/google/uuid"
)

const (
	PlanVersion     = "memora.instance-upgrade-plan/v1"
	ReceiptVersion  = "memora.instance-upgrade-receipt/v1"
	BackupVersion   = "memora.instance-backup/v1"
	JournalVersion  = "memora.instance-migration/v1"
	StateVersion    = "memora.instance-format-state/v1"
	formatStatePath = "system/format-state.json"
	backupManifest  = "backup.json"
	backupSnapshot  = "snapshot"
)

const (
	maxBackupFiles    = 10_000
	maxBackupFileSize = int64(16 << 30)
	maxBackupSize     = int64(64 << 30)
)

var (
	ErrApprovalRequired     = errors.New("instance upgrade requires explicit approval")
	ErrAlreadyCurrent       = errors.New("instance format is already current")
	ErrDowngradeUnsupported = errors.New("instance format downgrade is unsupported")
	ErrDaemonRunning        = errors.New("instance daemon must be stopped")
	ErrMigrationIncomplete  = errors.New("instance migration is incomplete; run doctor repair")
	ErrBackupRequired       = errors.New("an explicit backup or pending migration journal is required")
)

type Plan struct {
	Version       string   `json:"version"`
	InstanceID    string   `json:"instance_id"`
	FromVersion   uint32   `json:"from_version"`
	ToVersion     uint32   `json:"to_version"`
	BackupRoot    string   `json:"backup_root"`
	RequiresStop  bool     `json:"requires_daemon_stop"`
	RequiresApply bool     `json:"requires_explicit_apply"`
	Steps         []string `json:"steps"`
}

type Receipt struct {
	Version        string `json:"version"`
	Status         string `json:"status"`
	MigrationID    string `json:"migration_id"`
	InstanceID     string `json:"instance_id"`
	FromVersion    uint32 `json:"from_version"`
	ToVersion      uint32 `json:"to_version"`
	BackupPath     string `json:"backup_path"`
	CompletedAt    string `json:"completed_at"`
	BackupRetained bool   `json:"backup_retained"`
}

type BackupFile struct {
	Path   string `json:"path"`
	Mode   uint32 `json:"mode"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Backup struct {
	Version     string       `json:"version"`
	InstanceID  string       `json:"instance_id"`
	FromVersion uint32       `json:"from_version"`
	ToVersion   uint32       `json:"to_version"`
	MigrationID string       `json:"migration_id"`
	CreatedAt   string       `json:"created_at"`
	Files       []BackupFile `json:"files"`
}

type Journal struct {
	Version     string `json:"version"`
	MigrationID string `json:"migration_id"`
	InstanceID  string `json:"instance_id"`
	FromVersion uint32 `json:"from_version"`
	ToVersion   uint32 `json:"to_version"`
	BackupPath  string `json:"backup_path"`
	StartedAt   string `json:"started_at"`
}

type Options struct {
	Approved bool
	Clock    instance.Clock
	IDs      instance.IDSource
	Fault    func(string) error
}

func Preview(root string) (Plan, error) {
	if !absoluteClean(root) {
		return Plan{}, instance.ErrPathNotAbsolute
	}
	if _, present, err := readJournal(root); err != nil {
		return Plan{}, err
	} else if present {
		return Plan{}, ErrMigrationIncomplete
	}
	inspection, err := instance.Inspect(root)
	if err != nil {
		return Plan{}, err
	}
	switch inspection.Status {
	case instance.CompatibilityCurrent:
		return Plan{}, ErrAlreadyCurrent
	case instance.CompatibilityNewerFormat:
		return Plan{}, ErrDowngradeUnsupported
	case instance.CompatibilityUpgradeRequired:
	default:
		return Plan{}, instance.ErrCorruptMetadata
	}
	if inspection.Metadata.FormatVersion != instance.MinimumUpgradableVersion ||
		instance.FormatVersion != inspection.Metadata.FormatVersion+1 {
		return Plan{}, ErrDowngradeUnsupported
	}
	return Plan{
		Version:       PlanVersion,
		InstanceID:    inspection.Metadata.InstanceID,
		FromVersion:   inspection.Metadata.FormatVersion,
		ToVersion:     instance.FormatVersion,
		BackupRoot:    backupRoot(root),
		RequiresStop:  true,
		RequiresApply: true,
		Steps: []string{
			"verify_daemon_stopped",
			"create_verified_backup",
			"publish_migration_journal",
			"replace_instance_metadata",
			"publish_format_state",
			"remove_migration_journal",
			"verify_current_format",
		},
	}, nil
}

func Apply(ctx context.Context, root string, options Options) (Receipt, error) {
	if !options.Approved {
		return Receipt{}, ErrApprovalRequired
	}
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	initialPlan, err := Preview(root)
	if err != nil {
		return Receipt{}, err
	}
	options = withDefaults(options)
	lease, err := daemon.AcquireMaintenance(root)
	if err != nil {
		if errors.Is(err, daemon.ErrAlreadyRunning) {
			return Receipt{}, ErrDaemonRunning
		}
		return Receipt{}, err
	}
	defer func() { _ = lease.Close() }()
	plan, err := Preview(root)
	if err != nil {
		return Receipt{}, err
	}
	if plan.InstanceID != initialPlan.InstanceID ||
		plan.FromVersion != initialPlan.FromVersion ||
		plan.ToVersion != initialPlan.ToVersion {
		return Receipt{}, errors.New("instance upgrade plan changed while acquiring maintenance lock")
	}
	migrationID, err := options.IDs.Next()
	if err != nil {
		return Receipt{}, fmt.Errorf("allocate migration ID: %w", err)
	}
	if _, err := uuid.Parse(migrationID); err != nil {
		return Receipt{}, fmt.Errorf("parse migration ID: %w", err)
	}
	now := options.Clock.Now().UTC()
	backupPath, _, err := createBackup(ctx, root, plan, migrationID, now)
	if err != nil {
		return Receipt{}, err
	}
	if err := fault(options, "after_backup"); err != nil {
		return Receipt{}, err
	}
	journal := Journal{
		Version: JournalVersion, MigrationID: migrationID, InstanceID: plan.InstanceID,
		FromVersion: plan.FromVersion, ToVersion: plan.ToVersion,
		BackupPath: backupPath, StartedAt: now.Format(time.RFC3339Nano),
	}
	if err := writeJSONAtomic(
		filepath.Join(root, filepath.FromSlash(instance.MigrationJournalRelativePath)),
		journal,
	); err != nil {
		return Receipt{}, err
	}
	if err := fault(options, "after_journal"); err != nil {
		return Receipt{}, err
	}
	if _, err := instance.RewriteFormatVersion(root, plan.FromVersion, plan.ToVersion); err != nil {
		return Receipt{}, err
	}
	if err := fault(options, "after_metadata"); err != nil {
		return Receipt{}, err
	}
	state := struct {
		Version       string `json:"version"`
		MigrationID   string `json:"migration_id"`
		InstanceID    string `json:"instance_id"`
		FromVersion   uint32 `json:"from_version"`
		FormatVersion uint32 `json:"format_version"`
		BackupPath    string `json:"backup_path"`
		CompletedAt   string `json:"completed_at"`
	}{
		Version: StateVersion, MigrationID: migrationID, InstanceID: plan.InstanceID,
		FromVersion: plan.FromVersion, FormatVersion: plan.ToVersion,
		BackupPath: backupPath, CompletedAt: now.Format(time.RFC3339Nano),
	}
	if err := writeJSONAtomic(filepath.Join(root, filepath.FromSlash(formatStatePath)), state); err != nil {
		return Receipt{}, err
	}
	if err := fault(options, "after_state"); err != nil {
		return Receipt{}, err
	}
	journalPath := filepath.Join(root, filepath.FromSlash(instance.MigrationJournalRelativePath))
	if err := os.Remove(journalPath); err != nil {
		return Receipt{}, fmt.Errorf("remove migration journal: %w", err)
	}
	if err := syncDirectory(filepath.Dir(journalPath)); err != nil {
		return Receipt{}, err
	}
	metadata, err := instance.Read(root)
	if err != nil || metadata.FormatVersion != plan.ToVersion || metadata.InstanceID != plan.InstanceID {
		return Receipt{}, errors.New("verify migrated instance format failed")
	}
	return Receipt{
		Version: ReceiptVersion, Status: "upgraded", MigrationID: migrationID,
		InstanceID: plan.InstanceID, FromVersion: plan.FromVersion, ToVersion: plan.ToVersion,
		BackupPath: backupPath, CompletedAt: now.Format(time.RFC3339Nano), BackupRetained: true,
	}, nil
}

func Repair(ctx context.Context, root, requestedBackup string, options Options) (Receipt, error) {
	if !options.Approved {
		return Receipt{}, ErrApprovalRequired
	}
	if !absoluteClean(root) {
		return Receipt{}, instance.ErrPathNotAbsolute
	}
	if requestedBackup != "" && !absoluteClean(requestedBackup) {
		return Receipt{}, errors.New("repair backup path must be absolute and normalized")
	}
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	options = withDefaults(options)
	lease, err := daemon.AcquireMaintenance(root)
	if err != nil {
		if errors.Is(err, daemon.ErrAlreadyRunning) {
			return Receipt{}, ErrDaemonRunning
		}
		return Receipt{}, err
	}
	defer func() { _ = lease.Close() }()
	journal, pending, err := readJournal(root)
	if err != nil {
		return Receipt{}, err
	}
	backupPath := requestedBackup
	if backupPath == "" {
		if !pending {
			return Receipt{}, ErrBackupRequired
		}
		backupPath = journal.BackupPath
	}
	if pathsOverlap(root, backupPath) {
		return Receipt{}, errors.New("repair backup and Instance paths must be disjoint")
	}
	backup, err := VerifyBackup(backupPath)
	if err != nil {
		return Receipt{}, err
	}
	inspection, err := instance.Inspect(root)
	if err != nil {
		return Receipt{}, err
	}
	if inspection.Status == instance.CompatibilityNewerFormat {
		return Receipt{}, ErrDowngradeUnsupported
	}
	if inspection.Status != instance.CompatibilityCurrent &&
		inspection.Status != instance.CompatibilityUpgradeRequired {
		return Receipt{}, instance.ErrCorruptMetadata
	}
	if inspection.Metadata.InstanceID != backup.InstanceID {
		return Receipt{}, errors.New("repair backup belongs to a different Instance")
	}
	if pending && (journal.InstanceID != backup.InstanceID ||
		journal.MigrationID != backup.MigrationID ||
		journal.BackupPath != backupPath) {
		return Receipt{}, errors.New("repair backup does not match the pending migration")
	}
	now := options.Clock.Now().UTC()
	if !pending {
		journal = Journal{
			Version: JournalVersion, MigrationID: backup.MigrationID,
			InstanceID: backup.InstanceID, FromVersion: backup.FromVersion,
			ToVersion: backup.ToVersion, BackupPath: backupPath,
			StartedAt: now.Format(time.RFC3339Nano),
		}
		if err := writeJSONAtomic(
			filepath.Join(root, filepath.FromSlash(instance.MigrationJournalRelativePath)),
			journal,
		); err != nil {
			return Receipt{}, err
		}
		if err := fault(options, "after_repair_journal"); err != nil {
			return Receipt{}, err
		}
	}
	if err := restoreBackup(ctx, root, backupPath, backup); err != nil {
		return Receipt{}, err
	}
	for _, relative := range []string{formatStatePath, instance.MigrationJournalRelativePath} {
		if err := os.Remove(filepath.Join(root, filepath.FromSlash(relative))); err != nil &&
			!errors.Is(err, os.ErrNotExist) {
			return Receipt{}, fmt.Errorf("remove migration state %s: %w", relative, err)
		}
	}
	if err := syncDirectory(filepath.Join(root, "system")); err != nil {
		return Receipt{}, err
	}
	restored, err := instance.Inspect(root)
	if err != nil ||
		restored.Status != instance.CompatibilityUpgradeRequired ||
		restored.Metadata.FormatVersion != backup.FromVersion ||
		restored.Metadata.InstanceID != backup.InstanceID {
		return Receipt{}, errors.New("verify repaired Instance failed")
	}
	return Receipt{
		Version: ReceiptVersion, Status: "rolled_back", MigrationID: backup.MigrationID,
		InstanceID: backup.InstanceID, FromVersion: backup.ToVersion, ToVersion: backup.FromVersion,
		BackupPath: backupPath, CompletedAt: now.Format(time.RFC3339Nano), BackupRetained: true,
	}, nil
}

func VerifyBackup(root string) (Backup, error) {
	if !absoluteClean(root) {
		return Backup{}, errors.New("backup path must be absolute and normalized")
	}
	info, err := os.Lstat(root)
	if err != nil ||
		!info.IsDir() ||
		info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm() != 0o700 {
		return Backup{}, errors.New("backup root must be a private regular directory")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 2 {
		return Backup{}, errors.New("backup root has an unexpected layout")
	}
	expectedTop := map[string]bool{backupManifest: false, backupSnapshot: false}
	for _, entry := range entries {
		if _, ok := expectedTop[entry.Name()]; !ok {
			return Backup{}, errors.New("backup root has an unexpected layout")
		}
		expectedTop[entry.Name()] = true
	}
	manifestPath := filepath.Join(root, backupManifest)
	manifestInfo, err := os.Lstat(manifestPath)
	if err != nil ||
		!manifestInfo.Mode().IsRegular() ||
		manifestInfo.Mode().Perm() != 0o600 {
		return Backup{}, errors.New("backup manifest must be a private regular file")
	}
	snapshotRoot := filepath.Join(root, backupSnapshot)
	snapshotInfo, err := os.Lstat(snapshotRoot)
	if err != nil ||
		!snapshotInfo.IsDir() ||
		snapshotInfo.Mode()&os.ModeSymlink != 0 ||
		snapshotInfo.Mode().Perm() != 0o700 {
		return Backup{}, errors.New("backup snapshot must be a private regular directory")
	}
	encoded, err := os.ReadFile(manifestPath)
	if err != nil {
		return Backup{}, fmt.Errorf("read backup manifest: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var backup Backup
	if err := decoder.Decode(&backup); err != nil {
		return Backup{}, errors.New("backup manifest is invalid")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Backup{}, errors.New("backup manifest has trailing content")
	}
	if err := validateBackupManifest(backup); err != nil {
		return Backup{}, err
	}
	actual, err := inspectSnapshot(snapshotRoot)
	if err != nil {
		return Backup{}, err
	}
	if len(actual) != len(backup.Files) {
		return Backup{}, errors.New("backup snapshot file set does not match manifest")
	}
	for index, expected := range backup.Files {
		got := actual[index]
		if got.Path != expected.Path ||
			got.Mode != expected.Mode ||
			got.Size != expected.Size ||
			got.SHA256 != expected.SHA256 {
			return Backup{}, fmt.Errorf("backup file %s does not match manifest", expected.Path)
		}
	}
	inspection, err := instance.Inspect(snapshotRoot)
	if err != nil ||
		inspection.Metadata.InstanceID != backup.InstanceID ||
		inspection.Metadata.FormatVersion != backup.FromVersion {
		return Backup{}, errors.New("backup instance metadata does not match manifest")
	}
	return backup, nil
}

func createBackup(
	ctx context.Context,
	root string,
	plan Plan,
	migrationID string,
	createdAt time.Time,
) (string, Backup, error) {
	parent := backupRoot(root)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", Backup{}, fmt.Errorf("create backup root: %w", err)
	}
	if err := os.Chmod(parent, 0o700); err != nil {
		return "", Backup{}, fmt.Errorf("secure backup root: %w", err)
	}
	destination := filepath.Join(parent, migrationID)
	if _, err := os.Lstat(destination); err == nil {
		return "", Backup{}, errors.New("migration backup already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", Backup{}, err
	}
	staging, err := os.MkdirTemp(parent, ".backup-*")
	if err != nil {
		return "", Backup{}, fmt.Errorf("create backup staging directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(staging) }()
	if err := os.Chmod(staging, 0o700); err != nil {
		return "", Backup{}, err
	}
	snapshotRoot := filepath.Join(staging, backupSnapshot)
	if err := os.Mkdir(snapshotRoot, 0o700); err != nil {
		return "", Backup{}, err
	}
	files, err := copySnapshot(ctx, root, snapshotRoot)
	if err != nil {
		return "", Backup{}, err
	}
	backup := Backup{
		Version: BackupVersion, InstanceID: plan.InstanceID,
		FromVersion: plan.FromVersion, ToVersion: plan.ToVersion,
		MigrationID: migrationID, CreatedAt: createdAt.Format(time.RFC3339Nano),
		Files: files,
	}
	if err := writeJSONAtomic(filepath.Join(staging, backupManifest), backup); err != nil {
		return "", Backup{}, err
	}
	if err := syncDirectory(staging); err != nil {
		return "", Backup{}, err
	}
	if err := os.Rename(staging, destination); err != nil {
		return "", Backup{}, fmt.Errorf("publish backup: %w", err)
	}
	if err := syncDirectory(parent); err != nil {
		return "", Backup{}, err
	}
	if _, err := VerifyBackup(destination); err != nil {
		return "", Backup{}, fmt.Errorf("verify published backup: %w", err)
	}
	return destination, backup, nil
}

func copySnapshot(ctx context.Context, sourceRoot, destinationRoot string) ([]BackupFile, error) {
	var files []BackupFile
	var total int64
	err := filepath.WalkDir(sourceRoot, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(sourceRoot, current)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == "." {
			return nil
		}
		if excludedBackupPath(relative) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !safeRelative(relative) || entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("backup source path %q is unsafe", relative)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		destination := filepath.Join(destinationRoot, filepath.FromSlash(relative))
		if entry.IsDir() {
			if err := os.MkdirAll(destination, 0o700); err != nil {
				return err
			}
			return os.Chmod(destination, 0o700)
		}
		if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maxBackupFileSize {
			return fmt.Errorf("backup source %q is not a bounded regular file", relative)
		}
		total += info.Size()
		if len(files) >= maxBackupFiles || total > maxBackupSize {
			return errors.New("backup exceeds its file or size budget")
		}
		hash, size, err := copyFile(current, destination, 0o600)
		if err != nil {
			return err
		}
		files = append(files, BackupFile{
			Path: relative, Mode: 0o600, Size: size, SHA256: hash,
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("copy Instance backup: %w", err)
	}
	sort.Slice(files, func(left, right int) bool { return files[left].Path < files[right].Path })
	hasMetadata := false
	for _, file := range files {
		if file.Path == "instance.meta" {
			hasMetadata = true
			break
		}
	}
	if !hasMetadata {
		return nil, errors.New("backup does not contain instance.meta")
	}
	return files, nil
}

func restoreBackup(ctx context.Context, root, backupPath string, backup Backup) error {
	if err := pruneForRestore(ctx, root, backup.Files); err != nil {
		return err
	}
	files := append([]BackupFile(nil), backup.Files...)
	sort.SliceStable(files, func(left, right int) bool {
		if files[left].Path == "instance.meta" {
			return false
		}
		if files[right].Path == "instance.meta" {
			return true
		}
		return files[left].Path < files[right].Path
	})
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		source := filepath.Join(backupPath, backupSnapshot, filepath.FromSlash(file.Path))
		destination := filepath.Join(root, filepath.FromSlash(file.Path))
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return err
		}
		hash, size, err := copyFileAtomic(source, destination, os.FileMode(file.Mode))
		if err != nil {
			return err
		}
		if hash != file.SHA256 || size != file.Size {
			return fmt.Errorf("restored backup file %s failed verification", file.Path)
		}
	}
	return syncDirectory(root)
}

func pruneForRestore(ctx context.Context, root string, files []BackupFile) error {
	expectedFiles := make(map[string]bool, len(files))
	expectedDirectories := map[string]bool{".": true}
	for _, file := range files {
		expectedFiles[file.Path] = true
		for directory := path.Dir(file.Path); directory != "."; directory = path.Dir(directory) {
			expectedDirectories[directory] = true
		}
	}
	var extraFiles []string
	var extraDirectories []string
	err := filepath.WalkDir(root, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == "." {
			return nil
		}
		if excludedBackupPath(relative) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !safeRelative(relative) || entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("repair target path %q is unsafe", relative)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if !expectedDirectories[relative] {
				extraDirectories = append(extraDirectories, current)
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("repair target path %q is not a regular file", relative)
		}
		if !expectedFiles[relative] {
			extraFiles = append(extraFiles, current)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("inspect repair target: %w", err)
	}
	for _, file := range extraFiles {
		if err := os.Remove(file); err != nil {
			return fmt.Errorf("remove post-backup file: %w", err)
		}
	}
	sort.Slice(extraDirectories, func(left, right int) bool {
		return len(extraDirectories[left]) > len(extraDirectories[right])
	})
	for _, directory := range extraDirectories {
		if err := os.Remove(directory); err != nil && !errors.Is(err, os.ErrNotExist) {
			if !errors.Is(err, syscall.ENOTEMPTY) {
				return fmt.Errorf("remove post-backup directory: %w", err)
			}
		}
	}
	return syncDirectory(root)
}

func inspectSnapshot(root string) ([]BackupFile, error) {
	var files []BackupFile
	var total int64
	err := filepath.WalkDir(root, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == root {
			return nil
		}
		relative, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if !safeRelative(relative) || entry.Type()&os.ModeSymlink != 0 {
			return errors.New("backup snapshot contains an unsafe path")
		}
		if entry.IsDir() {
			info, err := entry.Info()
			if err != nil || info.Mode().Perm() != 0o700 {
				return errors.New("backup snapshot contains a non-private directory")
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxBackupFileSize {
			return errors.New("backup snapshot contains an invalid file")
		}
		total += info.Size()
		if len(files) >= maxBackupFiles || total > maxBackupSize {
			return errors.New("backup snapshot exceeds its budget")
		}
		hash, size, err := hashFile(current)
		if err != nil {
			return err
		}
		files = append(files, BackupFile{
			Path: relative, Mode: uint32(info.Mode().Perm()), Size: size, SHA256: hash,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(left, right int) bool { return files[left].Path < files[right].Path })
	return files, nil
}

func validateBackupManifest(backup Backup) error {
	if backup.Version != BackupVersion ||
		backup.FromVersion != instance.MinimumUpgradableVersion ||
		backup.ToVersion != instance.FormatVersion ||
		backup.ToVersion != backup.FromVersion+1 ||
		uuid.Validate(backup.InstanceID) != nil ||
		uuid.Validate(backup.MigrationID) != nil ||
		len(backup.Files) == 0 ||
		len(backup.Files) > maxBackupFiles {
		return errors.New("backup manifest metadata is invalid")
	}
	if _, err := time.Parse(time.RFC3339Nano, backup.CreatedAt); err != nil {
		return errors.New("backup manifest timestamp is invalid")
	}
	previous := ""
	var total int64
	for _, file := range backup.Files {
		if !safeRelative(file.Path) ||
			file.Path <= previous ||
			file.Mode != 0o600 ||
			file.Size < 0 ||
			file.Size > maxBackupFileSize ||
			!validHash(file.SHA256) {
			return errors.New("backup manifest file is invalid")
		}
		previous = file.Path
		total += file.Size
		if total > maxBackupSize {
			return errors.New("backup manifest exceeds its size budget")
		}
	}
	return nil
}

func readJournal(root string) (Journal, bool, error) {
	journalPath := filepath.Join(root, filepath.FromSlash(instance.MigrationJournalRelativePath))
	value, err := os.ReadFile(journalPath)
	if errors.Is(err, os.ErrNotExist) {
		return Journal{}, false, nil
	}
	if err != nil {
		return Journal{}, false, fmt.Errorf("read migration journal: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.DisallowUnknownFields()
	var journal Journal
	if err := decoder.Decode(&journal); err != nil {
		return Journal{}, false, errors.New("migration journal is invalid")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Journal{}, false, errors.New("migration journal has trailing content")
	}
	if journal.Version != JournalVersion ||
		uuid.Validate(journal.MigrationID) != nil ||
		uuid.Validate(journal.InstanceID) != nil ||
		journal.FromVersion != instance.MinimumUpgradableVersion ||
		journal.ToVersion != instance.FormatVersion ||
		!absoluteClean(journal.BackupPath) {
		return Journal{}, false, errors.New("migration journal metadata is invalid")
	}
	if _, err := time.Parse(time.RFC3339Nano, journal.StartedAt); err != nil {
		return Journal{}, false, errors.New("migration journal timestamp is invalid")
	}
	return journal, true, nil
}

func writeJSONAtomic(destination string, value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), "."+filepath.Base(destination)+"-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(destination))
}

func copyFile(source, destination string, mode os.FileMode) (string, int64, error) {
	input, err := os.Open(source)
	if err != nil {
		return "", 0, err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return "", 0, err
	}
	hash := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(output, hash), input)
	syncErr := output.Sync()
	closeErr := output.Close()
	if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

func copyFileAtomic(source, destination string, mode os.FileMode) (string, int64, error) {
	temporary, err := os.CreateTemp(filepath.Dir(destination), "."+filepath.Base(destination)+"-restore-")
	if err != nil {
		return "", 0, err
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		return "", 0, err
	}
	_ = os.Remove(temporaryPath)
	hash, size, err := copyFile(source, temporaryPath, mode.Perm())
	if err != nil {
		_ = os.Remove(temporaryPath)
		return "", 0, err
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		_ = os.Remove(temporaryPath)
		return "", 0, err
	}
	return hash, size, syncDirectory(filepath.Dir(destination))
}

func hashFile(name string) (string, int64, error) {
	file, err := os.Open(name)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

func syncDirectory(name string) error {
	directory, err := os.Open(name)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func excludedBackupPath(relative string) bool {
	return relative == "tmp" ||
		strings.HasPrefix(relative, "tmp/") ||
		relative == "system/daemon.lock" ||
		relative == "system/daemon.pid" ||
		relative == instance.MigrationJournalRelativePath
}

func safeRelative(value string) bool {
	return value != "" &&
		value == path.Clean(value) &&
		value != "." &&
		!strings.HasPrefix(value, "/") &&
		!strings.Contains(value, `\`) &&
		!strings.Contains(value, "\x00")
}

func backupRoot(root string) string {
	return filepath.Join(filepath.Dir(root), filepath.Base(root)+".memora-backups")
}

func absoluteClean(value string) bool {
	return filepath.IsAbs(value) && filepath.Clean(value) == value
}

func pathsOverlap(left, right string) bool {
	contains := func(parent, child string) bool {
		relative, err := filepath.Rel(parent, child)
		return err == nil &&
			(relative == "." ||
				(relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))))
	}
	return contains(left, right) || contains(right, left)
}

func validHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func fault(options Options, phase string) error {
	if options.Fault == nil {
		return nil
	}
	if err := options.Fault(phase); err != nil {
		return fmt.Errorf("injected migration interruption after %s: %w", phase, err)
	}
	return nil
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
	return options
}
