package release_test

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/release"
)

const (
	testVersion = "0.1.0"
	testCommit  = "0123456789abcdef0123456789abcdef01234567"
	testEpoch   = int64(1785399977)
)

func TestBuildProducesDeterministicTraceableMacOSArtifacts(t *testing.T) {
	t.Parallel()

	root := releaseFixture(t, true)
	firstDir := filepath.Join(t.TempDir(), "release")
	secondDir := filepath.Join(t.TempDir(), "release")
	options := release.Options{
		RepositoryRoot: root, OutputDir: firstDir,
		Version: testVersion, Commit: testCommit, SourceEpoch: testEpoch,
	}
	first, err := release.Build(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	options.OutputDir = secondDir
	second, err := release.Build(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) || first.Version != release.ManifestVersion ||
		first.BuiltAt != time.Unix(testEpoch, 0).UTC().Format(time.RFC3339) ||
		len(first.Artifacts) != 2 {
		t.Fatalf("manifests = %#v / %#v", first, second)
	}
	for _, name := range releaseFiles(t, firstDir) {
		left, err := os.ReadFile(filepath.Join(firstDir, name))
		if err != nil {
			t.Fatal(err)
		}
		right, err := os.ReadFile(filepath.Join(secondDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(left, right) {
			t.Fatalf("release file %s is not deterministic", name)
		}
	}
	verified, err := release.Verify(firstDir)
	if err != nil || !reflect.DeepEqual(verified, first) {
		t.Fatalf("Verify() = %#v, %v", verified, err)
	}
	if err := os.WriteFile(filepath.Join(secondDir, "unexpected"), []byte("no\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := release.Verify(secondDir); err == nil ||
		!strings.Contains(err.Error(), "unexpected file set") {
		t.Fatalf("Verify() accepted an extra release file: %v", err)
	}
	if runtime.GOOS != "darwin" {
		return
	}
	nativeArchive := filepath.Join(
		firstDir, "memora_"+testVersion+"_darwin_"+runtime.GOARCH+".tar.gz",
	)
	binary := extractBinary(t, nativeArchive)
	output, err := exec.Command(binary, "version", "--json").CombinedOutput()
	if err != nil {
		t.Fatalf("native release version failed: %v: %s", err, output)
	}
	var metadata struct {
		Version string `json:"version"`
		Commit  string `json:"commit"`
		BuiltAt string `json:"built_at"`
	}
	if json.Unmarshal(output, &metadata) != nil ||
		metadata.Version != testVersion || metadata.Commit != testCommit ||
		metadata.BuiltAt != first.BuiltAt {
		t.Fatalf("native release metadata = %s", output)
	}
	smoke := filepath.Join(repositoryRoot(t), "scripts", "smoke-release.sh")
	smokeOutput, err := exec.Command(
		"/bin/sh", smoke, firstDir, testVersion, runtime.GOARCH,
	).CombinedOutput()
	if err != nil || !strings.Contains(string(smokeOutput), "release smoke passed") {
		t.Fatalf("release smoke failed: %v: %s", err, smokeOutput)
	}
}

func TestVerifyRejectsCorruptedArtifact(t *testing.T) {
	t.Parallel()

	root := releaseFixture(t, true)
	output := filepath.Join(t.TempDir(), "release")
	manifest, err := release.Build(context.Background(), release.Options{
		RepositoryRoot: root, OutputDir: output,
		Version: testVersion, Commit: testCommit, SourceEpoch: testEpoch,
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(output, manifest.Artifacts[0].Name)
	value, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	value[len(value)/2] ^= 0xff
	if err := os.WriteFile(path, value, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := release.Verify(output); err == nil {
		t.Fatal("Verify() accepted a corrupted release artifact")
	}
}

func TestBuildSignedProducesDeterministicTrustedRelease(t *testing.T) {
	t.Parallel()

	seed := make([]byte, ed25519.SeedSize)
	for index := range seed {
		seed[index] = byte(index + 1)
	}
	privateKey := ed25519.NewKeyFromSeed(seed)
	signer := &release.Signer{KeyID: "memora:test-release", PrivateKey: privateKey}
	root := releaseFixture(t, true)
	firstDir := filepath.Join(t.TempDir(), "release")
	secondDir := filepath.Join(t.TempDir(), "release")
	options := release.Options{RepositoryRoot: root, OutputDir: firstDir, Version: testVersion,
		Commit: testCommit, SourceEpoch: testEpoch, Signer: signer}
	if _, err := release.Build(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	options.OutputDir = secondDir
	if _, err := release.Build(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	for _, name := range releaseFiles(t, firstDir) {
		left, _ := os.ReadFile(filepath.Join(firstDir, name))
		right, _ := os.ReadFile(filepath.Join(secondDir, name))
		if !reflect.DeepEqual(left, right) {
			t.Fatalf("signed release file %s is not deterministic", name)
		}
	}
	trusted := map[string]ed25519.PublicKey{"memora:test-release": privateKey.Public().(ed25519.PublicKey)}
	if _, err := release.VerifySigned(firstDir, trusted); err != nil {
		t.Fatalf("VerifySigned() error = %v", err)
	}
	if _, err := release.VerifySigned(firstDir, map[string]ed25519.PublicKey{}); err == nil {
		t.Fatal("VerifySigned() accepted an unknown signer")
	}
	signaturePath := filepath.Join(firstDir, "release.sig")
	encoded, _ := os.ReadFile(signaturePath)
	encoded[len(encoded)/2] ^= 1
	if err := os.WriteFile(signaturePath, encoded, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := release.Verify(firstDir); err == nil {
		t.Fatal("Verify() accepted a corrupted signature")
	}
}

func TestBuildRequiresVersionCommitTimestampLicenseAndNewOutput(t *testing.T) {
	t.Parallel()

	valid := release.Options{
		RepositoryRoot: releaseFixture(t, false),
		OutputDir:      filepath.Join(t.TempDir(), "release"),
		Version:        testVersion,
		Commit:         testCommit,
		SourceEpoch:    testEpoch,
	}
	if _, err := release.Build(context.Background(), valid); err == nil ||
		!strings.Contains(err.Error(), "LICENSE") {
		t.Fatalf("missing LICENSE error = %v", err)
	}
	for _, change := range []func(*release.Options){
		func(value *release.Options) { value.Version = "latest" },
		func(value *release.Options) { value.Commit = "dirty" },
		func(value *release.Options) { value.SourceEpoch = 0 },
		func(value *release.Options) { value.OutputDir = "relative" },
	} {
		options := valid
		change(&options)
		if _, err := release.Build(context.Background(), options); err == nil {
			t.Fatalf("Build() accepted %#v", options)
		}
	}
}

func TestRepositoryLicenseKeepsNoncommercialUseFreeAndCommercialUsePaid(t *testing.T) {
	t.Parallel()

	root := repositoryRoot(t)
	license, err := os.ReadFile(filepath.Join(root, "LICENSE"))
	if err != nil {
		t.Fatal(err)
	}
	commercial, err := os.ReadFile(filepath.Join(root, "COMMERCIAL-LICENSE.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"PolyForm Noncommercial License 1.0.0",
		"Any noncommercial purpose is a permitted purpose.",
		"Required Notice: Copyright 2026 HW-Yue.",
		"Commercial use requires a separate paid commercial license",
	} {
		if !strings.Contains(string(license), required) {
			t.Fatalf("LICENSE is missing %q", required)
		}
	}
	for _, required := range []string{
		"No commercial-use license is granted",
		"separate written, paid commercial",
		"pricing are negotiated separately",
	} {
		if !strings.Contains(string(commercial), required) {
			t.Fatalf("COMMERCIAL-LICENSE.md is missing %q", required)
		}
	}
}

func releaseFixture(t *testing.T, withLicense bool) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "cmd", "memora"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"go.mod":                "module example.test/memora\n\ngo 1.25.0\n",
		"README.md":             "# Memora release fixture\n",
		"COMMERCIAL-LICENSE.md": "# Commercial fixture terms\n",
		filepath.Join("cmd", "memora", "main.go"): `package main

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
`,
	}
	if withLicense {
		files["LICENSE"] = "Release fixture license\n"
	}
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func releaseFiles(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names
}

func extractBinary(t *testing.T, archive string) string {
	t.Helper()
	file, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer gzipReader.Close()
	reader := tar.NewReader(gzipReader)
	header, err := reader.Next()
	if err != nil || header.Name != "memora" {
		t.Fatalf("first archive entry = %#v, %v", header, err)
	}
	path := filepath.Join(t.TempDir(), "memora")
	output, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(output, reader); err != nil {
		_ = output.Close()
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
