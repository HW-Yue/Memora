package pagestoremigration

import (
	"bufio"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/HW-Yue/Memora/internal/store/treecontrol"
)

const (
	GenerationDirectory      = "page-index-v1"
	generationVersion        = "memora.page-index-generation/v5"
	sharedCurrentVersion     = "memora.page-index-generation/v4"
	treeWALGenerationVersion = "memora.page-index-generation/v3"
	rowGenerationVersion     = "memora.page-index-generation/v2"
	legacyGenerationVersion  = "memora.page-index-generation/v1"
	manifestFileName         = "manifest.json"
	maxManifestBytes         = 64 << 10

	// sharedWALDirectory holds the one redo log a v4 generation writes. Every
	// Tree commits into it, which is what makes a publication spanning several
	// Trees a single WAL transaction instead of one per Tree.
	sharedWALDirectory = "redo.wal"

	catalogSpaceID  = uint64(0x4d454d434154) // MEMCAT
	currentSpaceID  = uint64(0x4d454d435552) // MEMCUR
	versionSpaceID  = uint64(0x4d454d564552) // MEMVER
	fulltextSpaceID = uint64(0x4d454d465458) // MEMFTX
)

var (
	ErrConflict       = errors.New("Page index generation conflicts with the requested Plan")
	ErrTargetCorrupt  = errors.New("Page index generation is corrupt")
	ErrOutcomeUnknown = errors.New("Page index generation publication outcome is unknown")
)

type treeManifest struct {
	Kind         string            `json:"kind"`
	SpaceID      uint64            `json:"space_id"`
	PageFile     string            `json:"page_file"`
	WALDirectory string            `json:"wal_directory"`
	State        treeStateManifest `json:"state"`
}

type treeStateManifest struct {
	Generation uint64 `json:"generation"`
	Revision   uint64 `json:"revision"`
	RootPageID uint64 `json:"root_page_id"`
	NextPageID uint64 `json:"next_page_id"`
	LSN        uint64 `json:"lsn"`
}

type generationManifest struct {
	Version           string         `json:"version"`
	PlanVersion       string         `json:"plan_version"`
	PlanDigest        string         `json:"plan_digest"`
	SourceFingerprint string         `json:"source_sha256"`
	Trees             []treeManifest `json:"trees"`
	ContentDigest     string         `json:"content_sha256"`
	Digest            string         `json:"digest"`
}

// expectedTrees describes the fixed part of a v5 generation. No Tree names a
// WAL directory because they all share sharedWALDirectory.
//
// There is no "current" Tree: a Table's current Rows live in that Table's own
// Tree, which is why the fixed list is three and the rest follow the Catalog.
// See docs/storage/per-table-tree-v1.md §2.
var expectedTrees = []treeManifest{
	{Kind: "catalog", SpaceID: catalogSpaceID, PageFile: "catalog.pages"},
	{Kind: "versions", SpaceID: versionSpaceID, PageFile: "versions.pages"},
	{Kind: "fulltext", SpaceID: fulltextSpaceID, PageFile: "fulltext.pages"},
}

// sharedCurrentExpectedTrees describes v4, which kept every Table's current
// Rows in one Tree keyed by (Table, Row). Kept so an existing database still
// opens; the Authority then COW-rebuilds it to v5.
var sharedCurrentExpectedTrees = []treeManifest{
	{Kind: "catalog", SpaceID: catalogSpaceID, PageFile: "catalog.pages"},
	{Kind: "current", SpaceID: currentSpaceID, PageFile: "current.pages"},
	{Kind: "versions", SpaceID: versionSpaceID, PageFile: "versions.pages"},
	{Kind: "fulltext", SpaceID: fulltextSpaceID, PageFile: "fulltext.pages"},
}

// treeWALExpectedTrees describes v3 and v2 generations, which gave every Tree
// its own log. Kept so an existing database still opens; the Authority then
// COW-upgrades it to v4 (see openGeneration).
var treeWALExpectedTrees = []treeManifest{
	{Kind: "catalog", SpaceID: catalogSpaceID, PageFile: "catalog.pages", WALDirectory: "catalog.wal"},
	{Kind: "current", SpaceID: currentSpaceID, PageFile: "current.pages", WALDirectory: "current.wal"},
	{Kind: "versions", SpaceID: versionSpaceID, PageFile: "versions.pages", WALDirectory: "versions.wal"},
	{Kind: "fulltext", SpaceID: fulltextSpaceID, PageFile: "fulltext.pages", WALDirectory: "fulltext.wal"},
}

var legacyExpectedTrees = append([]treeManifest(nil), treeWALExpectedTrees[:3]...)

// validate checks a manifest's Tree set: a fixed prefix that every generation
// of this version has, then any number of per-Table Trees.
//
// The tail is open-ended because the number of Trees follows the number of
// Tables (docs/storage/per-table-tree-v1.md §2) — two per Table, the clustered
// Tree and the history Tree. Open-ended is not unchecked: a per-Table Tree's
// space and page file both derive from its Table ID, so a Tree claiming any
// other identity is refused, and no two Trees may share a space or a page file.
func (manifest generationManifest) validate() error {
	expected, validVersion := manifestTreeSpecifications(manifest.Version, manifest.PlanVersion)
	if !validVersion ||
		!canonicalSHA256(manifest.PlanDigest) || !canonicalSHA256(manifest.SourceFingerprint) ||
		!canonicalSHA256(manifest.ContentDigest) || !canonicalSHA256(manifest.Digest) ||
		len(manifest.Trees) < len(expected) {
		return ErrTargetCorrupt
	}
	// Only a shared-log generation may carry per-Table Trees. Older layouts gave
	// every Tree its own log directory and were never taught to grow.
	if len(manifest.Trees) > len(expected) && !manifest.sharedLog() {
		return ErrTargetCorrupt
	}
	spaces := make(map[uint64]bool, len(manifest.Trees))
	files := make(map[string]bool, len(manifest.Trees))
	for index, tree := range manifest.Trees {
		state := tree.State.runtimeState(tree.SpaceID)
		if state.RootPageID == 0 || spaces[tree.SpaceID] || files[tree.PageFile] {
			return ErrTargetCorrupt
		}
		spaces[tree.SpaceID], files[tree.PageFile] = true, true
		if index < len(expected) {
			expectedTree := expected[index]
			if tree.Kind != expectedTree.Kind || tree.SpaceID != expectedTree.SpaceID ||
				tree.PageFile != expectedTree.PageFile || tree.WALDirectory != expectedTree.WALDirectory {
				return ErrTargetCorrupt
			}
		} else if !validTableTree(tree) && !validHistoryTree(tree) {
			return ErrTargetCorrupt
		}
		if _, err := treecontrol.Encode(state); err != nil {
			return fmt.Errorf("%w: %s Tree state: %v", ErrTargetCorrupt, tree.Kind, err)
		}
	}
	want, err := manifestDigest(manifest)
	if err != nil || want != manifest.Digest {
		return ErrTargetCorrupt
	}
	return nil
}

func manifestTreeSpecifications(version, planVersion string) ([]treeManifest, bool) {
	switch {
	case version == generationVersion && planVersion == PlanVersion:
		return expectedTrees, true
	case version == sharedCurrentVersion && planVersion == PlanVersion:
		return sharedCurrentExpectedTrees, true
	case version == treeWALGenerationVersion && planVersion == PlanVersion:
		return treeWALExpectedTrees, true
	case version == rowGenerationVersion && planVersion == rowPlanVersion:
		return treeWALExpectedTrees, true
	case version == legacyGenerationVersion && planVersion == legacyPlanVersion:
		return legacyExpectedTrees, true
	default:
		return nil, false
	}
}

func treeStateFromRuntime(state treecontrol.State) treeStateManifest {
	return treeStateManifest{
		Generation: state.Generation, Revision: state.Revision,
		RootPageID: state.RootPageID, NextPageID: state.NextPageID, LSN: state.LSN,
	}
}

// sharedLog reports whether every Tree in the generation commits into one redo
// log. Only v4 does; older generations keep one log per Tree.
// perTableRows reports whether a generation keeps each Table's current Rows in
// that Table's own Tree. Only v5 does; v4 kept them all in one Tree keyed by
// (Table, Row), and is rebuilt on open.
func (manifest generationManifest) perTableRows() bool {
	return manifest.Version == generationVersion
}

func (manifest generationManifest) sharedLog() bool {
	return manifest.Version == generationVersion || manifest.Version == sharedCurrentVersion
}

func (state treeStateManifest) runtimeState(spaceID uint64) treecontrol.State {
	return treecontrol.State{
		SpaceID: spaceID, Generation: state.Generation, Revision: state.Revision,
		RootPageID: state.RootPageID, NextPageID: state.NextPageID, LSN: state.LSN,
	}
}

func manifestDigest(manifest generationManifest) (string, error) {
	payload := struct {
		Version           string         `json:"version"`
		PlanVersion       string         `json:"plan_version"`
		PlanDigest        string         `json:"plan_digest"`
		SourceFingerprint string         `json:"source_sha256"`
		Trees             []treeManifest `json:"trees"`
		ContentDigest     string         `json:"content_sha256"`
	}{
		Version: manifest.Version, PlanVersion: manifest.PlanVersion,
		PlanDigest: manifest.PlanDigest, SourceFingerprint: manifest.SourceFingerprint,
		Trees: manifest.Trees, ContentDigest: manifest.ContentDigest,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func readManifest(directory string) (generationManifest, error) {
	path := filepath.Join(directory, manifestFileName)
	file, err := os.Open(path)
	if err != nil {
		return generationManifest{}, fmt.Errorf("%w: open manifest: %v", ErrTargetCorrupt, err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxManifestBytes {
		return generationManifest{}, fmt.Errorf("%w: manifest file size or type", ErrTargetCorrupt)
	}
	decoder := json.NewDecoder(io.LimitReader(file, maxManifestBytes+1))
	decoder.DisallowUnknownFields()
	var manifest generationManifest
	if err := decoder.Decode(&manifest); err != nil {
		return generationManifest{}, fmt.Errorf("%w: decode manifest: %v", ErrTargetCorrupt, err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return generationManifest{}, fmt.Errorf("%w: manifest trailing data", ErrTargetCorrupt)
	}
	if err := manifest.validate(); err != nil {
		return generationManifest{}, err
	}
	return manifest, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return fmt.Errorf("extra JSON value")
	}
	return err
}

// rewriteManifest replaces a live generation's manifest.
//
// writeManifest creates the manifest of a generation that did not exist a
// moment ago and refuses to overwrite; this one is for a generation already in
// use, which grows a Tree whenever a Table is created. The replacement is
// atomic — a full temporary file, synced, then renamed over the old one — so a
// crash leaves either the previous manifest or the new one, never a half-written
// file that would read as a corrupt generation.
func rewriteManifest(directory string, manifest generationManifest) error {
	digest, err := manifestDigest(manifest)
	if err != nil {
		return err
	}
	manifest.Digest = digest
	if err := manifest.validate(); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	temporary := filepath.Join(directory, manifestFileName+".next")
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create replacement generation manifest: %w", err)
	}
	removeOnError := true
	defer func() {
		if removeOnError {
			_ = file.Close()
			_ = os.Remove(temporary)
		}
	}()
	if _, err := file.Write(encoded); err != nil {
		return fmt.Errorf("write replacement generation manifest: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync replacement generation manifest: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close replacement generation manifest: %w", err)
	}
	if err := os.Rename(temporary, filepath.Join(directory, manifestFileName)); err != nil {
		return fmt.Errorf("replace generation manifest: %w", err)
	}
	removeOnError = false
	return syncGenerationDirectory(directory)
}

func writeManifest(directory string, manifest generationManifest) error {
	digest, err := manifestDigest(manifest)
	if err != nil {
		return err
	}
	manifest.Digest = digest
	if err := manifest.validate(); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	path := filepath.Join(directory, manifestFileName)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create generation manifest: %w", err)
	}
	removeOnError := true
	defer func() {
		_ = file.Close()
		if removeOnError {
			_ = os.Remove(path)
		}
	}()
	writer := bufio.NewWriter(file)
	if _, err := writer.Write(encoded); err != nil {
		return fmt.Errorf("write generation manifest: %w", err)
	}
	if err := writer.Flush(); err != nil {
		return fmt.Errorf("flush generation manifest: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync generation manifest: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close generation manifest: %w", err)
	}
	if err := syncGenerationDirectory(directory); err != nil {
		return fmt.Errorf("sync generation directory: %w", err)
	}
	removeOnError = false
	return nil
}

func contentDigest(directory string, manifest generationManifest) (string, error) {
	if err := validateGenerationEntries(directory, manifest); err != nil {
		return "", err
	}
	paths := make([]string, 0)
	err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == directory || entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(directory, path)
		if err != nil {
			return err
		}
		if filepath.ToSlash(relative) == manifestFileName {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("%w: non-regular generation entry %q", ErrTargetCorrupt, relative)
		}
		paths = append(paths, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("%w: inventory content: %v", ErrTargetCorrupt, err)
	}
	sort.Strings(paths)
	hash := sha256.New()
	_, _ = hash.Write([]byte("memora-page-index-generation-content-v1\x00"))
	var encoded [8]byte
	for _, relative := range paths {
		path := filepath.Join(directory, filepath.FromSlash(relative))
		file, err := os.Open(path)
		if err != nil {
			return "", fmt.Errorf("%w: open content %q: %v", ErrTargetCorrupt, relative, err)
		}
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			_ = file.Close()
			return "", fmt.Errorf("%w: stat content %q", ErrTargetCorrupt, relative)
		}
		binary.BigEndian.PutUint32(encoded[:4], uint32(len(relative)))
		_, _ = hash.Write(encoded[:4])
		_, _ = hash.Write([]byte(relative))
		binary.BigEndian.PutUint64(encoded[:], uint64(info.Size()))
		_, _ = hash.Write(encoded[:])
		if _, err := io.Copy(hash, file); err != nil {
			_ = file.Close()
			return "", fmt.Errorf("%w: hash content %q: %v", ErrTargetCorrupt, relative, err)
		}
		if err := file.Close(); err != nil {
			return "", fmt.Errorf("%w: close content %q: %v", ErrTargetCorrupt, relative, err)
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func validateGenerationEntries(directory string, manifest generationManifest) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return fmt.Errorf("%w: read generation directory: %v", ErrTargetCorrupt, err)
	}
	expected := map[string]bool{manifestFileName: false}
	for _, tree := range manifest.Trees {
		expected[tree.PageFile] = false
		if tree.WALDirectory != "" {
			expected[tree.WALDirectory] = true
		}
	}
	if manifest.sharedLog() {
		expected[sharedWALDirectory] = true
	}
	seen := make(map[string]bool, len(expected))
	for _, entry := range entries {
		wantDirectory, exists := expected[entry.Name()]
		if !exists || entry.Type()&os.ModeSymlink != 0 || entry.IsDir() != wantDirectory {
			return fmt.Errorf("%w: unexpected generation entry %q", ErrTargetCorrupt, entry.Name())
		}
		seen[entry.Name()] = true
	}
	for name := range expected {
		if !seen[name] && name != manifestFileName {
			return fmt.Errorf("%w: missing generation entry %q", ErrTargetCorrupt, name)
		}
	}
	return nil
}

func canonicalSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}

func syncGenerationDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return directory.Sync()
}
