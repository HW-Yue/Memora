package claudeadapter

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/HW-Yue/Memora/internal/skillcontract"
)

const Version = "memora.claude-code-adapter/v1"

type File struct {
	Content []byte
	Mode    os.FileMode
}

type Bundle struct {
	Files    map[string]File
	Manifest Manifest
}

type Manifest struct {
	Version         string         `json:"version"`
	CanonicalDigest string         `json:"canonical_digest"`
	ProtocolDigest  string         `json:"protocol_digest"`
	Files           []ManifestFile `json:"files"`
}

type ManifestFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Mode   string `json:"mode"`
}

func Build(canonicalDirectory string) (Bundle, error) {
	canonical, err := skillcontract.Load(canonicalDirectory)
	if err != nil {
		return Bundle{}, fmt.Errorf("build Claude Code adapter: %w", err)
	}
	if err := canonical.Validate(); err != nil {
		return Bundle{}, fmt.Errorf("build Claude Code adapter: %w", err)
	}
	canonicalSkill := []byte(canonical.Markdown)
	skill, err := addClaudeFrontmatter(canonical.Markdown)
	if err != nil {
		return Bundle{}, err
	}
	contract, err := os.ReadFile(filepath.Join(canonicalDirectory, "contract.json"))
	if err != nil {
		return Bundle{}, fmt.Errorf("read canonical contract: %w", err)
	}
	installer, err := os.ReadFile(filepath.Join(canonicalDirectory, canonical.Contract.BootstrapScript))
	if err != nil {
		return Bundle{}, fmt.Errorf("read canonical bootstrap: %w", err)
	}
	files := map[string]File{
		".claude/skills/memora/SKILL.md":           {Content: []byte(skill), Mode: 0o644},
		".claude/skills/memora/contract.json":      {Content: contract, Mode: 0o644},
		".claude/skills/memora/scripts/install.sh": {Content: installer, Mode: 0o755},
	}
	manifest := Manifest{
		Version: Version, CanonicalDigest: digest(canonicalSkill), ProtocolDigest: digest(contract),
		Files: manifestFiles(files),
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return Bundle{}, fmt.Errorf("encode Claude Code adapter manifest: %w", err)
	}
	files["manifest.json"] = File{Content: append(encoded, '\n'), Mode: 0o644}
	return Bundle{Files: files, Manifest: manifest}, nil
}

func CanonicalBody(skill string) string {
	if !strings.HasPrefix(skill, "---\n") {
		return skill
	}
	end := strings.Index(skill[4:], "\n---\n")
	if end < 0 {
		return skill
	}
	return skill[4+end+5:]
}

func addClaudeFrontmatter(skill string) (string, error) {
	if !strings.HasPrefix(skill, "---\n") {
		return "", fmt.Errorf("canonical Skill has no YAML frontmatter")
	}
	end := strings.Index(skill[4:], "\n---\n")
	if end < 0 {
		return "", fmt.Errorf("canonical Skill frontmatter is incomplete")
	}
	position := 4 + end
	allowed := "\nallowed-tools: " + strings.Join([]string{
		"Bash(memora assimilate *)", "Bash(memora doctor *)", "Bash(memora exec *)",
		"Bash(memora feedback *)", "Bash(memora maintain *)", "Bash(memora mutate *)",
		"Bash(memora query *)", "Bash(memora reflect *)", "Bash(memora schema *)",
	}, " ")
	return skill[:position] + allowed + skill[position:], nil
}

func (bundle Bundle) Install(root string) error {
	if !filepath.IsAbs(root) {
		return fmt.Errorf("install Claude Code adapter: destination must be absolute")
	}
	for _, relative := range sortedPaths(bundle.Files) {
		file := bundle.Files[relative]
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("create Claude Code adapter directory: %w", err)
		}
		temporary := path + ".tmp"
		if err := os.WriteFile(temporary, file.Content, file.Mode); err != nil {
			return fmt.Errorf("write Claude Code adapter file %q: %w", relative, err)
		}
		if err := os.Chmod(temporary, file.Mode); err != nil {
			_ = os.Remove(temporary)
			return fmt.Errorf("set Claude Code adapter mode %q: %w", relative, err)
		}
		if err := os.Rename(temporary, path); err != nil {
			_ = os.Remove(temporary)
			return fmt.Errorf("publish Claude Code adapter file %q: %w", relative, err)
		}
	}
	return nil
}

func (bundle Bundle) Compare(root string) error {
	for _, relative := range sortedPaths(bundle.Files) {
		want := bundle.Files[relative]
		path := filepath.Join(root, filepath.FromSlash(relative))
		got, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("Claude Code adapter artifact %q: %w", relative, err)
		}
		if string(got) != string(want.Content) {
			return fmt.Errorf("Claude Code adapter artifact %q drifted from generator", relative)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != want.Mode.Perm() {
			return fmt.Errorf("Claude Code adapter artifact %q mode drifted", relative)
		}
	}
	return nil
}

func manifestFiles(files map[string]File) []ManifestFile {
	paths := sortedPaths(files)
	entries := make([]ManifestFile, 0, len(paths))
	for _, path := range paths {
		file := files[path]
		entries = append(entries, ManifestFile{Path: path, SHA256: digest(file.Content), Mode: fmt.Sprintf("%04o", file.Mode.Perm())})
	}
	return entries
}

func sortedPaths(files map[string]File) []string {
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func digest(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}
