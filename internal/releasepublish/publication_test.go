package releasepublish_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/releasepublish"
)

const publicationEpoch = int64(1785399977)

func TestBuildPublicationIsDeterministicAndSelfVerifying(t *testing.T) {
	t.Parallel()

	root := publicationFixture(t)
	firstDir := filepath.Join(t.TempDir(), "publication")
	secondDir := filepath.Join(t.TempDir(), "publication")
	options := releasepublish.BuildOptions{
		RepositoryRoot: root,
		OutputDir:      firstDir,
		Version:        "1.2.3",
		Commit:         targetCommit,
		SourceEpoch:    publicationEpoch,
	}
	first, err := releasepublish.Build(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	options.OutputDir = secondDir
	second, err := releasepublish.Build(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) ||
		first.Bundle.Version != releasepublish.BundleManifestVersion ||
		first.Bundle.ProductVersion != "1.2.3" ||
		first.Bundle.Commit != targetCommit {
		t.Fatalf("publications = %#v / %#v", first, second)
	}
	for _, name := range publicationFiles(t, firstDir) {
		left, err := os.ReadFile(filepath.Join(firstDir, name))
		if err != nil {
			t.Fatal(err)
		}
		right, err := os.ReadFile(filepath.Join(secondDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(left, right) {
			t.Fatalf("publication file %q is not deterministic", name)
		}
	}
	if _, err := releasepublish.Verify(firstDir, "1.2.3"); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyPublicationRejectsMissingAndMismatchedAssets(t *testing.T) {
	t.Parallel()

	root := publicationFixture(t)
	output := filepath.Join(t.TempDir(), "publication")
	if _, err := releasepublish.Build(context.Background(), releasepublish.BuildOptions{
		RepositoryRoot: root,
		OutputDir:      output,
		Version:        "1.2.3",
		Commit:         targetCommit,
		SourceEpoch:    publicationEpoch,
	}); err != nil {
		t.Fatal(err)
	}
	checksum := filepath.Join(output, "memora_1.2.3_skill_bundle.tar.gz.sha256")
	if err := os.WriteFile(checksum, []byte(strings.Repeat("0", 64)+"  memora_1.2.3_skill_bundle.tar.gz\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := releasepublish.Verify(output, "1.2.3"); err == nil ||
		!strings.Contains(err.Error(), "checksum") {
		t.Fatalf("checksum mismatch error = %v", err)
	}

	missingOutput := filepath.Join(t.TempDir(), "publication")
	if _, err := releasepublish.Build(context.Background(), releasepublish.BuildOptions{
		RepositoryRoot: root,
		OutputDir:      missingOutput,
		Version:        "1.2.3",
		Commit:         targetCommit,
		SourceEpoch:    publicationEpoch,
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(missingOutput, "release", "checksums.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := releasepublish.Verify(missingOutput, "1.2.3"); err == nil {
		t.Fatal("Verify accepted a missing release asset")
	}
}

func publicationFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"go.mod":                           "module example.test/memora\n\ngo 1.25.0\n",
		"README.md":                        "# Memora fixture\n",
		"LICENSE":                          "PolyForm fixture\nRequired Notice: Copyright fixture\n",
		"COMMERCIAL-LICENSE.md":            "Commercial use requires a separate paid license.\n",
		"cmd/memora/main.go":               fixtureMain,
		"skills/memora/SKILL.md":           "# Skill\n",
		"skills/memora/contract.json":      "{}\n",
		"skills/memora/scripts/install.sh": "#!/bin/sh\nexit 0\n",
		"adapters/codex/manifest.json":     "{}\n",
		"adapters/codex/.agents/skills/memora/SKILL.md":       "# Codex Skill\n",
		"adapters/claude-code/manifest.json":                  "{}\n",
		"adapters/claude-code/.claude/skills/memora/SKILL.md": "# Claude Skill\n",
	}
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if strings.HasSuffix(name, ".sh") {
			mode = 0o755
		}
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func publicationFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			files = append(files, filepath.ToSlash(relative))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	return files
}

const fixtureMain = `package main

import (
	"encoding/json"
	"os"
)

var version = "dev"
var commit = "unknown"
var builtAt = "unknown"

func main() {
	if len(os.Args) == 3 && os.Args[1] == "version" && os.Args[2] == "--json" {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{
			"version": version, "commit": commit, "built_at": builtAt,
		})
	}
}
`
