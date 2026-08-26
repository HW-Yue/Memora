package releasepublish

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
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
	"strconv"
	"strings"
	"time"

	"github.com/HW-Yue/Memora/internal/release"
)

const BundleManifestVersion = "memora.skill-bundle/v1"

const (
	maxBundleFiles    = 128
	maxBundleFileSize = 1 << 20
	maxBundleSize     = 8 << 20
)

type BundleFile struct {
	Path   string `json:"path"`
	Mode   string `json:"mode"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type BundleManifest struct {
	Version         string       `json:"version"`
	Product         string       `json:"product"`
	ProductVersion  string       `json:"product_version"`
	Commit          string       `json:"commit"`
	BuiltAt         string       `json:"built_at"`
	SourceDateEpoch int64        `json:"source_date_epoch"`
	Files           []BundleFile `json:"files"`
}

type bundleSource struct {
	BundleFile
	Content []byte
}

func buildBundle(options BuildOptions, outputDirectory string) (BundleManifest, error) {
	sources, err := collectBundleSources(options.RepositoryRoot)
	if err != nil {
		return BundleManifest{}, err
	}
	builtAt := time.Unix(options.SourceEpoch, 0).UTC()
	manifest := BundleManifest{
		Version: BundleManifestVersion, Product: "memora",
		ProductVersion: options.Version, Commit: options.Commit,
		BuiltAt: builtAt.Format(time.RFC3339), SourceDateEpoch: options.SourceEpoch,
		Files: make([]BundleFile, 0, len(sources)),
	}
	for _, source := range sources {
		manifest.Files = append(manifest.Files, source.BundleFile)
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return BundleManifest{}, fmt.Errorf("encode Skill bundle manifest: %w", err)
	}
	encoded = append(encoded, '\n')
	name := bundleArchiveName(options.Version)
	archivePath := filepath.Join(outputDirectory, name)
	if err := writeBundleArchive(archivePath, encoded, sources, builtAt); err != nil {
		return BundleManifest{}, err
	}
	hash, _, err := hashFile(archivePath)
	if err != nil {
		return BundleManifest{}, err
	}
	checksum := hash + "  " + name + "\n"
	if err := os.WriteFile(archivePath+".sha256", []byte(checksum), 0o644); err != nil {
		return BundleManifest{}, fmt.Errorf("write Skill bundle checksum: %w", err)
	}
	return manifest, nil
}

func collectBundleSources(root string) ([]bundleSource, error) {
	names := []string{"COMMERCIAL-LICENSE.md", "LICENSE", "README.md"}
	for _, directory := range []string{
		"adapters/claude-code",
		"adapters/codex",
		"skills/memora",
	} {
		absolute := filepath.Join(root, filepath.FromSlash(directory))
		info, err := os.Lstat(absolute)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("Skill bundle requires regular directory %s", directory)
		}
		err = filepath.WalkDir(absolute, func(current string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if current == absolute {
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("Skill bundle source %s contains a symlink", directory)
			}
			if entry.IsDir() {
				return nil
			}
			if !entry.Type().IsRegular() {
				return fmt.Errorf("Skill bundle source %s contains a non-regular file", directory)
			}
			relative, err := filepath.Rel(root, current)
			if err != nil {
				return err
			}
			names = append(names, filepath.ToSlash(relative))
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(names)
	if len(names) == 0 || len(names) > maxBundleFiles {
		return nil, errors.New("Skill bundle file count exceeds its budget")
	}
	sources := make([]bundleSource, 0, len(names))
	var total int64
	for _, name := range names {
		if !validBundlePath(name) {
			return nil, fmt.Errorf("Skill bundle path %q is invalid", name)
		}
		absolute := filepath.Join(root, filepath.FromSlash(name))
		info, err := os.Lstat(absolute)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("Skill bundle file %s must be a regular file", name)
		}
		if info.Size() <= 0 || info.Size() > maxBundleFileSize {
			return nil, fmt.Errorf("Skill bundle file %s exceeds its size budget", name)
		}
		content, err := os.ReadFile(absolute)
		if err != nil {
			return nil, fmt.Errorf("read Skill bundle file %s: %w", name, err)
		}
		total += int64(len(content))
		if total > maxBundleSize {
			return nil, errors.New("Skill bundle exceeds its total size budget")
		}
		mode := int64(0o644)
		if info.Mode().Perm()&0o111 != 0 {
			mode = 0o755
		}
		sources = append(sources, bundleSource{
			BundleFile: BundleFile{
				Path: name, Mode: fmt.Sprintf("%04o", mode), Size: int64(len(content)),
				SHA256: digest(content),
			},
			Content: content,
		})
	}
	return sources, nil
}

func writeBundleArchive(
	archivePath string,
	manifest []byte,
	sources []bundleSource,
	builtAt time.Time,
) error {
	file, err := os.OpenFile(archivePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("create Skill bundle: %w", err)
	}
	gzipWriter := gzip.NewWriter(file)
	gzipWriter.Header.ModTime = builtAt
	gzipWriter.Header.OS = 3
	tarWriter := tar.NewWriter(gzipWriter)
	writeEntry := func(name string, mode int64, content []byte) error {
		header := &tar.Header{
			Name: name, Mode: mode, Size: int64(len(content)),
			ModTime: builtAt, Typeflag: tar.TypeReg, Format: tar.FormatUSTAR,
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
		_, err := tarWriter.Write(content)
		return err
	}
	writeErr := writeEntry("bundle.json", 0o644, manifest)
	for _, source := range sources {
		if writeErr != nil {
			break
		}
		mode, _ := strconv.ParseInt(source.Mode, 8, 64)
		writeErr = writeEntry(source.Path, mode, source.Content)
	}
	writeErr = errors.Join(writeErr, tarWriter.Close(), gzipWriter.Close(), file.Close())
	if writeErr != nil {
		return fmt.Errorf("write Skill bundle: %w", writeErr)
	}
	return nil
}

func verifyBundle(
	directory, version string,
	releaseManifest release.Manifest,
) (BundleManifest, error) {
	name := bundleArchiveName(version)
	archivePath := filepath.Join(directory, name)
	hash, _, err := hashFile(archivePath)
	if err != nil {
		return BundleManifest{}, err
	}
	checksum, err := os.ReadFile(filepath.Join(directory, name+".sha256"))
	if err != nil {
		return BundleManifest{}, fmt.Errorf("read Skill bundle checksum: %w", err)
	}
	if string(checksum) != hash+"  "+name+"\n" {
		return BundleManifest{}, errors.New("Skill bundle checksum does not match")
	}
	file, err := os.Open(archivePath)
	if err != nil {
		return BundleManifest{}, fmt.Errorf("open Skill bundle: %w", err)
	}
	defer func() { _ = file.Close() }()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return BundleManifest{}, errors.New("Skill bundle gzip stream is invalid")
	}
	defer func() { _ = gzipReader.Close() }()
	reproducibleTime := time.Unix(releaseManifest.SourceDateEpoch, 0).UTC()
	if !gzipReader.Header.ModTime.Equal(reproducibleTime) ||
		gzipReader.Header.Name != "" ||
		gzipReader.Header.Comment != "" ||
		gzipReader.Header.OS != 3 {
		return BundleManifest{}, errors.New("Skill bundle gzip metadata is not reproducible")
	}
	tarReader := tar.NewReader(gzipReader)
	manifestBytes, err := readBundleEntry(tarReader, "bundle.json", 0o644, reproducibleTime, maxBundleFileSize)
	if err != nil {
		return BundleManifest{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(manifestBytes))
	decoder.DisallowUnknownFields()
	var manifest BundleManifest
	if err := decoder.Decode(&manifest); err != nil {
		return BundleManifest{}, errors.New("Skill bundle manifest is invalid")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return BundleManifest{}, errors.New("Skill bundle manifest has trailing content")
	}
	if err := validateBundleManifest(manifest, version, releaseManifest); err != nil {
		return BundleManifest{}, err
	}
	for _, expected := range manifest.Files {
		mode, _ := strconv.ParseInt(expected.Mode, 8, 64)
		content, err := readBundleEntry(
			tarReader, expected.Path, mode, reproducibleTime, maxBundleFileSize,
		)
		if err != nil {
			return BundleManifest{}, err
		}
		if int64(len(content)) != expected.Size || digest(content) != expected.SHA256 {
			return BundleManifest{}, fmt.Errorf("Skill bundle entry %s does not match its manifest", expected.Path)
		}
	}
	if _, err := tarReader.Next(); !errors.Is(err, io.EOF) {
		return BundleManifest{}, errors.New("Skill bundle has unexpected trailing entries")
	}
	return manifest, nil
}

func readBundleEntry(
	reader *tar.Reader,
	name string,
	mode int64,
	builtAt time.Time,
	maximum int64,
) ([]byte, error) {
	header, err := reader.Next()
	if err != nil ||
		header.Name != name ||
		header.Typeflag != tar.TypeReg ||
		header.Mode != mode ||
		!header.ModTime.Equal(builtAt) ||
		header.Uid != 0 ||
		header.Gid != 0 ||
		header.Uname != "" ||
		header.Gname != "" ||
		len(header.PAXRecords) != 0 ||
		header.Size <= 0 ||
		header.Size > maximum {
		return nil, fmt.Errorf("Skill bundle entry %s has invalid layout or metadata", name)
	}
	value, err := io.ReadAll(io.LimitReader(reader, header.Size+1))
	if err != nil || int64(len(value)) != header.Size {
		return nil, fmt.Errorf("Skill bundle entry %s is truncated", name)
	}
	return value, nil
}

func validateBundleManifest(
	manifest BundleManifest,
	version string,
	releaseManifest release.Manifest,
) error {
	if manifest.Version != BundleManifestVersion ||
		manifest.Product != "memora" ||
		manifest.ProductVersion != version ||
		manifest.Commit != releaseManifest.Commit ||
		manifest.SourceDateEpoch != releaseManifest.SourceDateEpoch ||
		manifest.BuiltAt != releaseManifest.BuiltAt ||
		len(manifest.Files) == 0 ||
		len(manifest.Files) > maxBundleFiles {
		return errors.New("Skill bundle manifest metadata is invalid")
	}
	mandatory := map[string]bool{
		"COMMERCIAL-LICENSE.md":                               false,
		"LICENSE":                                             false,
		"README.md":                                           false,
		"skills/memora/SKILL.md":                              false,
		"skills/memora/contract.json":                         false,
		"skills/memora/scripts/install.sh":                    false,
		"adapters/codex/manifest.json":                        false,
		"adapters/codex/.agents/skills/memora/SKILL.md":       false,
		"adapters/claude-code/manifest.json":                  false,
		"adapters/claude-code/.claude/skills/memora/SKILL.md": false,
	}
	previous := ""
	var total int64
	for _, file := range manifest.Files {
		mode, err := strconv.ParseInt(file.Mode, 8, 64)
		if !validBundlePath(file.Path) ||
			file.Path <= previous ||
			err != nil ||
			(mode != 0o644 && mode != 0o755) ||
			file.Size <= 0 ||
			file.Size > maxBundleFileSize ||
			!validDigest(file.SHA256) {
			return errors.New("Skill bundle manifest file is invalid")
		}
		previous = file.Path
		total += file.Size
		if total > maxBundleSize {
			return errors.New("Skill bundle manifest exceeds its size budget")
		}
		if _, ok := mandatory[file.Path]; ok {
			mandatory[file.Path] = true
		}
	}
	for name, present := range mandatory {
		if !present {
			return fmt.Errorf("Skill bundle manifest is missing %s", name)
		}
	}
	return nil
}

func validBundlePath(name string) bool {
	if name == "" ||
		name != path.Clean(name) ||
		strings.HasPrefix(name, "/") ||
		strings.Contains(name, `\`) ||
		strings.Contains(name, "\x00") {
		return false
	}
	switch name {
	case "COMMERCIAL-LICENSE.md", "LICENSE", "README.md":
		return true
	}
	return strings.HasPrefix(name, "adapters/claude-code/") ||
		strings.HasPrefix(name, "adapters/codex/") ||
		strings.HasPrefix(name, "skills/memora/")
}

func bundleArchiveName(version string) string {
	return fmt.Sprintf("memora_%s_skill_bundle.tar.gz", version)
}

func hashFile(name string) (string, int64, error) {
	file, err := os.Open(name)
	if err != nil {
		return "", 0, fmt.Errorf("open publication file: %w", err)
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, fmt.Errorf("hash publication file: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

func digest(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
