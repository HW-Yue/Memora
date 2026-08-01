package instancebackup

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
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/HW-Yue/Memora/internal/instance"
)

const (
	Version       = "memora.instance-portable-backup/v1"
	manifestName  = "backup.json"
	snapshotName  = "snapshot"
	maxFiles      = 100000
	maxTotalBytes = int64(8 << 30)
)

type Clock interface{ Now() time.Time }
type Fault func(point string) error

type Options struct {
	Clock Clock
	Fault Fault
}

type File struct {
	Path   string `json:"path"`
	Mode   uint32 `json:"mode"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	Version       string `json:"version"`
	InstanceID    string `json:"instance_id"`
	FormatVersion uint32 `json:"format_version"`
	PageSize      uint32 `json:"page_size"`
	CreatedAt     string `json:"created_at"`
	FileCount     int    `json:"file_count"`
	TotalBytes    int64  `json:"total_bytes"`
	Files         []File `json:"files"`
	Hash          string `json:"hash"`
}

type Receipt struct {
	Version    string `json:"version"`
	Status     string `json:"status"`
	Path       string `json:"path"`
	InstanceID string `json:"instance_id"`
	Hash       string `json:"hash"`
	FileCount  int    `json:"file_count"`
	TotalBytes int64  `json:"total_bytes"`
}

type RestoreReceipt struct {
	Version    string `json:"version"`
	Status     string `json:"status"`
	Path       string `json:"path"`
	InstanceID string `json:"instance_id"`
	BackupHash string `json:"backup_hash"`
	FileCount  int    `json:"file_count"`
	TotalBytes int64  `json:"total_bytes"`
}

func Restore(ctx context.Context, backupRoot, target string, options Options) (RestoreReceipt, error) {
	manifest, err := Verify(backupRoot)
	if err != nil {
		return RestoreReceipt{}, err
	}
	if err := validateRoots(backupRoot, target); err != nil {
		return RestoreReceipt{}, err
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return RestoreReceipt{}, errors.New("restore target already exists")
		}
		return RestoreReceipt{}, fmt.Errorf("inspect restore target: %w", err)
	}
	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return RestoreReceipt{}, err
	}
	staging, err := os.MkdirTemp(parent, ".memora-restore-*")
	if err != nil {
		return RestoreReceipt{}, err
	}
	defer os.RemoveAll(staging)
	if err := os.Chmod(staging, 0o700); err != nil {
		return RestoreReceipt{}, err
	}
	for _, file := range manifest.Files {
		if err := ctx.Err(); err != nil {
			return RestoreReceipt{}, err
		}
		destination := filepath.Join(staging, filepath.FromSlash(file.Path))
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return RestoreReceipt{}, err
		}
		if err := copyFile(filepath.Join(backupRoot, snapshotName, filepath.FromSlash(file.Path)), destination); err != nil {
			return RestoreReceipt{}, err
		}
	}
	files, total, err := inspectTree(staging)
	if err != nil || len(files) != len(manifest.Files) || total != manifest.TotalBytes {
		return RestoreReceipt{}, errors.New("restored staging file set does not match backup")
	}
	for index := range files {
		if files[index] != manifest.Files[index] {
			return RestoreReceipt{}, errors.New("restored staging file does not match backup")
		}
	}
	metadata, err := instance.Read(staging)
	if err != nil || metadata.InstanceID != manifest.InstanceID || metadata.FormatVersion != manifest.FormatVersion || metadata.PageSize != manifest.PageSize {
		return RestoreReceipt{}, errors.New("restored staging Instance metadata does not match backup")
	}
	if err := fault(options, "restore_before_publish"); err != nil {
		return RestoreReceipt{}, err
	}
	if err := os.Rename(staging, target); err != nil {
		return RestoreReceipt{}, fmt.Errorf("publish restored Instance: %w", err)
	}
	metadata, err = instance.Read(target)
	if err != nil || metadata.InstanceID != manifest.InstanceID {
		return RestoreReceipt{}, errors.New("published restored Instance failed verification")
	}
	return RestoreReceipt{Version: "memora.instance-restore-receipt/v1", Status: "restored", Path: target,
		InstanceID: manifest.InstanceID, BackupHash: manifest.Hash, FileCount: manifest.FileCount, TotalBytes: manifest.TotalBytes}, nil
}

func Create(ctx context.Context, source, destination string, options Options) (Receipt, error) {
	if err := validateRoots(source, destination); err != nil {
		return Receipt{}, err
	}
	metadata, err := instance.Read(source)
	if err != nil {
		return Receipt{}, fmt.Errorf("read backup source Instance: %w", err)
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return Receipt{}, errors.New("backup destination already exists")
		}
		return Receipt{}, fmt.Errorf("inspect backup destination: %w", err)
	}
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return Receipt{}, err
	}
	staging, err := os.MkdirTemp(parent, ".memora-backup-*")
	if err != nil {
		return Receipt{}, err
	}
	defer os.RemoveAll(staging)
	if err := os.Chmod(staging, 0o700); err != nil {
		return Receipt{}, err
	}
	snapshotRoot := filepath.Join(staging, snapshotName)
	if err := os.Mkdir(snapshotRoot, 0o700); err != nil {
		return Receipt{}, err
	}
	files, total, err := copyTree(ctx, source, snapshotRoot, options)
	if err != nil {
		return Receipt{}, err
	}
	createdAt := time.Now().UTC()
	if options.Clock != nil {
		createdAt = options.Clock.Now().UTC()
	}
	manifest := Manifest{Version: Version, InstanceID: metadata.InstanceID, FormatVersion: metadata.FormatVersion,
		PageSize: metadata.PageSize, CreatedAt: createdAt.Format(time.RFC3339Nano), FileCount: len(files),
		TotalBytes: total, Files: files}
	manifest.Hash, err = manifestHash(manifest)
	if err != nil {
		return Receipt{}, err
	}
	if err := writeManifest(filepath.Join(staging, manifestName), manifest); err != nil {
		return Receipt{}, err
	}
	if _, err := Verify(staging); err != nil {
		return Receipt{}, fmt.Errorf("verify staged backup: %w", err)
	}
	if err := fault(options, "before_publish"); err != nil {
		return Receipt{}, err
	}
	if err := os.Rename(staging, destination); err != nil {
		return Receipt{}, fmt.Errorf("publish backup: %w", err)
	}
	verified, err := Verify(destination)
	if err != nil {
		return Receipt{}, fmt.Errorf("verify published backup: %w", err)
	}
	return Receipt{Version: "memora.instance-backup-receipt/v1", Status: "created", Path: destination,
		InstanceID: verified.InstanceID, Hash: verified.Hash, FileCount: verified.FileCount, TotalBytes: verified.TotalBytes}, nil
}

func Verify(root string) (Manifest, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return Manifest{}, errors.New("backup path must be absolute and normalized")
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return Manifest{}, errors.New("backup root is not a private directory")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 2 || entries[0].Name() != manifestName || entries[1].Name() != snapshotName {
		return Manifest{}, errors.New("backup root has an unexpected layout")
	}
	manifestInfo, err := os.Lstat(filepath.Join(root, manifestName))
	if err != nil || !manifestInfo.Mode().IsRegular() || manifestInfo.Mode().Perm() != 0o600 {
		return Manifest{}, errors.New("backup manifest is not a private regular file")
	}
	snapshotInfo, err := os.Lstat(filepath.Join(root, snapshotName))
	if err != nil || !snapshotInfo.IsDir() || snapshotInfo.Mode().Perm()&0o077 != 0 {
		return Manifest{}, errors.New("backup snapshot is not a private directory")
	}
	encoded, err := os.ReadFile(filepath.Join(root, manifestName))
	if err != nil {
		return Manifest{}, fmt.Errorf("read backup manifest: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if decoder.Decode(&manifest) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return Manifest{}, errors.New("backup manifest is invalid")
	}
	if err := validateManifest(manifest); err != nil {
		return Manifest{}, err
	}
	want, err := manifestHash(manifest)
	if err != nil || want != manifest.Hash {
		return Manifest{}, errors.New("backup manifest hash does not match")
	}
	actual, total, err := inspectTree(filepath.Join(root, snapshotName))
	if err != nil {
		return Manifest{}, err
	}
	if len(actual) != manifest.FileCount || total != manifest.TotalBytes {
		return Manifest{}, errors.New("backup file set does not match manifest")
	}
	for index := range actual {
		if actual[index] != manifest.Files[index] {
			return Manifest{}, fmt.Errorf("backup file %q does not match manifest", actual[index].Path)
		}
	}
	metadata, err := instance.Read(filepath.Join(root, snapshotName))
	if err != nil || metadata.InstanceID != manifest.InstanceID || metadata.FormatVersion != manifest.FormatVersion || metadata.PageSize != manifest.PageSize {
		return Manifest{}, errors.New("backup Instance metadata does not match manifest")
	}
	return manifest, nil
}

func copyTree(ctx context.Context, source, target string, options Options) ([]File, int64, error) {
	paths := []string{}
	err := filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == source {
			return nil
		}
		relative, err := filepath.Rel(source, path)
		if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("backup source path is unsafe")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("backup source contains a symbolic link")
		}
		destination := filepath.Join(target, relative)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o700)
		}
		if !info.Mode().IsRegular() {
			return errors.New("backup source contains a non-regular file")
		}
		paths = append(paths, relative)
		return copyFile(path, destination)
	})
	if err != nil {
		return nil, 0, err
	}
	if err := fault(options, "after_copy"); err != nil {
		return nil, 0, err
	}
	return inspectTree(target)
}

func inspectTree(root string) ([]File, int64, error) {
	files, total := []File{}, int64(0)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("backup snapshot contains a symbolic link")
		}
		if entry.IsDir() {
			if info.Mode().Perm()&0o077 != 0 {
				return errors.New("backup directory permissions are not private")
			}
			return nil
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
			return errors.New("backup snapshot contains an invalid file")
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		hash, err := fileHash(path)
		if err != nil {
			return err
		}
		files = append(files, File{Path: filepath.ToSlash(relative), Mode: uint32(info.Mode().Perm()), Size: info.Size(), SHA256: hash})
		total += info.Size()
		if len(files) > maxFiles || total > maxTotalBytes {
			return errors.New("backup exceeds its budget")
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, total, nil
}

func copyFile(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err = io.Copy(output, input); err == nil {
		err = output.Sync()
	}
	closeErr := output.Close()
	if err == nil {
		err = closeErr
	}
	return err
}

func fileHash(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func writeManifest(path string, value Manifest) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.Write(encoded); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	return err
}

func manifestHash(value Manifest) (string, error) {
	value.Hash = ""
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func validateManifest(value Manifest) error {
	if value.Version != Version || value.InstanceID == "" || value.FormatVersion == 0 || value.PageSize == 0 ||
		value.FileCount != len(value.Files) || value.FileCount == 0 || value.FileCount > maxFiles || value.TotalBytes < 1 || value.TotalBytes > maxTotalBytes || len(value.Hash) != 64 {
		return errors.New("backup manifest metadata is invalid")
	}
	if _, err := hex.DecodeString(value.Hash); err != nil {
		return errors.New("backup manifest hash is invalid")
	}
	if _, err := time.Parse(time.RFC3339Nano, value.CreatedAt); err != nil {
		return errors.New("backup timestamp is invalid")
	}
	seen, total := map[string]bool{}, int64(0)
	for _, file := range value.Files {
		clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(file.Path)))
		if file.Path == "" || clean != file.Path || strings.HasPrefix(file.Path, "../") || filepath.IsAbs(file.Path) || seen[file.Path] ||
			file.Mode != 0o600 || file.Size < 0 || len(file.SHA256) != 64 {
			return errors.New("backup manifest file is invalid")
		}
		if _, err := hex.DecodeString(file.SHA256); err != nil {
			return errors.New("backup manifest file hash is invalid")
		}
		seen[file.Path] = true
		total += file.Size
	}
	if total != value.TotalBytes {
		return errors.New("backup manifest total is invalid")
	}
	return nil
}

func validateRoots(source, destination string) error {
	if !filepath.IsAbs(source) || !filepath.IsAbs(destination) || filepath.Clean(source) != source || filepath.Clean(destination) != destination {
		return errors.New("backup source and destination must be absolute and normalized")
	}
	if source == destination || strings.HasPrefix(destination+string(filepath.Separator), source+string(filepath.Separator)) || strings.HasPrefix(source+string(filepath.Separator), destination+string(filepath.Separator)) {
		return errors.New("backup source and destination must be disjoint")
	}
	return nil
}

func fault(options Options, point string) error {
	if options.Fault != nil {
		return options.Fault(point)
	}
	return nil
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }
