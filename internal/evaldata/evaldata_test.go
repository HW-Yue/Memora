package evaldata_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/evaldata"
)

func TestPrepareResumesInterruptedHTTPArtifactAndVerifiesDigest(t *testing.T) {
	payload := []byte(strings.Repeat("memora-evaluation-payload\n", 4096))
	digest := sha256.Sum256(payload)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		if requests == 1 {
			writer.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write(payload[:len(payload)/2])
			panic(http.ErrAbortHandler)
		}
		wantRange := "bytes=" + strconv.Itoa(len(payload)/2) + "-"
		if request.Header.Get("Range") != wantRange {
			t.Errorf("Range = %q, want %q", request.Header.Get("Range"), wantRange)
		}
		writer.Header().Set("Content-Range", "bytes "+strconv.Itoa(len(payload)/2)+"-"+strconv.Itoa(len(payload)-1)+"/"+strconv.Itoa(len(payload)))
		writer.WriteHeader(http.StatusPartialContent)
		_, _ = writer.Write(payload[len(payload)/2:])
	}))
	defer server.Close()

	registry := evaldata.Registry{Version: evaldata.RegistryVersion, Datasets: []evaldata.Dataset{{
		ID: "fixture", Title: "Fixture", Role: "resume test",
		License: evaldata.License{SPDX: "CC0-1.0", URL: "https://example.test/license"},
		Source: evaldata.Source{Kind: "http", Artifacts: []evaldata.Artifact{{
			Path: "payload.bin", URL: server.URL + "/payload.bin", Bytes: int64(len(payload)),
			SHA256: hex.EncodeToString(digest[:]),
		}}},
	}}}
	root := filepath.Join(t.TempDir(), "evaluation")
	receipt, err := evaldata.Prepare(context.Background(), registry, strings.Repeat("a", 64), evaldata.Options{
		Root: root, HTTPRetries: 2,
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "raw", "fixture", "payload.bin"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(got) != string(payload) || requests != 2 {
		t.Fatalf("artifact bytes/requests = %d/%d, want %d/2", len(got), requests, len(payload))
	}
	if len(receipt.Datasets) != 1 || receipt.Datasets[0].Bytes != int64(len(payload)) {
		t.Fatalf("receipt = %#v", receipt)
	}
}

func TestLoadRegistryRejectsUnknownFieldsAndUnsafePaths(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "registry.json")
	if err := os.WriteFile(path, []byte(`{"version":"memora.external-evaluation-registry/v1","unexpected":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := evaldata.LoadRegistry(path); err == nil || !strings.Contains(err.Error(), "decode registry") {
		t.Fatalf("LoadRegistry() error = %v", err)
	}
	registry := evaldata.Registry{Version: evaldata.RegistryVersion, Datasets: []evaldata.Dataset{{
		ID: "fixture", Title: "Fixture", Role: "role", License: evaldata.License{SPDX: "CC0-1.0", URL: "https://example.test/license"},
		Source: evaldata.Source{Kind: "http", Artifacts: []evaldata.Artifact{{Path: "../escape", URL: "https://example.test/file", Bytes: 1, SHA256: strings.Repeat("a", 64)}}},
	}}}
	encoded, err := json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := evaldata.LoadRegistry(path); err == nil || !strings.Contains(err.Error(), "artifact") {
		t.Fatalf("LoadRegistry() unsafe path error = %v", err)
	}
}

func TestVerifyOnlyChecksExistingArtifactWithoutCreatingRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing")
	payload := []byte("offline")
	digest := sha256.Sum256(payload)
	artifactRoot := filepath.Join(root, "raw", "fixture")
	if err := os.MkdirAll(artifactRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifactRoot, "data"), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	registry := evaldata.Registry{Version: evaldata.RegistryVersion, Datasets: []evaldata.Dataset{{
		ID: "fixture", Title: "Fixture", Role: "role", License: evaldata.License{SPDX: "CC0-1.0", URL: "https://example.test/license"},
		Source: evaldata.Source{Kind: "http", Artifacts: []evaldata.Artifact{{Path: "data", URL: "https://example.test/data", Bytes: int64(len(payload)), SHA256: hex.EncodeToString(digest[:])}}},
	}}}
	if _, err := evaldata.Prepare(context.Background(), registry, strings.Repeat("b", 64), evaldata.Options{Root: root, VerifyOnly: true}); err != nil {
		t.Fatalf("Prepare(verify-only) error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "state")); !os.IsNotExist(err) {
		t.Fatalf("verify-only created state directory, stat error = %v", err)
	}
}

func TestPrepareRejectsHomeAndRoot(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	registry := evaldata.Registry{Version: evaldata.RegistryVersion, Datasets: []evaldata.Dataset{{
		ID: "fixture", Title: "Fixture", Role: "role", License: evaldata.License{SPDX: "CC0-1.0", URL: "https://example.test/license"},
		Source: evaldata.Source{Kind: "http", Artifacts: []evaldata.Artifact{{Path: "data", URL: "https://example.test/data", Bytes: 1, SHA256: strings.Repeat("a", 64)}}},
	}}}
	if _, err := evaldata.Prepare(context.Background(), registry, strings.Repeat("c", 64), evaldata.Options{Root: home}); err == nil {
		t.Fatal("Prepare(home) unexpectedly succeeded")
	}
	if _, err := evaldata.Prepare(context.Background(), registry, strings.Repeat("c", 64), evaldata.Options{Root: string(filepath.Separator)}); err == nil {
		t.Fatal("Prepare(/) unexpectedly succeeded")
	}
}

func TestPrepareRestartsWhenServerIgnoresRange(t *testing.T) {
	payload := []byte(strings.Repeat("restart", 1024))
	digest := sha256.Sum256(payload)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		if request.Header.Get("Range") == "" {
			t.Error("resume request omitted Range header")
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write(payload)
	}))
	defer server.Close()
	root := filepath.Join(t.TempDir(), "evaluation")
	targetRoot := filepath.Join(root, "raw", "fixture")
	if err := os.MkdirAll(targetRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(targetRoot, "payload.part"), payload[:100], 0o644); err != nil {
		t.Fatal(err)
	}
	registry := HTTPRegistry(server.URL+"/payload", "payload", payload, hex.EncodeToString(digest[:]))
	if _, err := evaldata.Prepare(context.Background(), registry, strings.Repeat("d", 64), evaldata.Options{Root: root, HTTPRetries: 1}); err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(targetRoot, "payload"))
	if err != nil || string(got) != string(payload) || requests != 1 {
		t.Fatalf("restart result bytes/requests/error = %d/%d/%v", len(got), requests, err)
	}
}

func TestPrepareDoesNotOverwriteCorruptPublishedArtifact(t *testing.T) {
	payload := []byte("expected")
	digest := sha256.Sum256(payload)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer server.Close()
	root := filepath.Join(t.TempDir(), "evaluation")
	targetRoot := filepath.Join(root, "raw", "fixture")
	if err := os.MkdirAll(targetRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(targetRoot, "payload")
	if err := os.WriteFile(target, []byte("bad"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := HTTPRegistry(server.URL+"/payload", "payload", payload, hex.EncodeToString(digest[:]))
	if _, err := evaldata.Prepare(context.Background(), registry, strings.Repeat("e", 64), evaldata.Options{Root: root}); err == nil || !strings.Contains(err.Error(), "size mismatch") {
		t.Fatalf("Prepare() error = %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "bad" || requests != 0 {
		t.Fatalf("published artifact changed: %q requests=%d error=%v", got, requests, err)
	}
}

func TestVerifyOnlyRejectsGitRevisionDrift(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	root := filepath.Join(t.TempDir(), "evaluation")
	target := filepath.Join(root, "sources", "fixture")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{{"init", "--quiet"}, {"config", "user.email", "fixture@example.test"}, {"config", "user.name", "Fixture"}} {
		command := exec.Command("git", append([]string{"-C", target}, arguments...)...)
		if encoded, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", arguments, err, encoded)
		}
	}
	if err := os.WriteFile(filepath.Join(target, "data"), []byte("fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{{"add", "data"}, {"commit", "--quiet", "-m", "fixture"}} {
		command := exec.Command("git", append([]string{"-C", target}, arguments...)...)
		if encoded, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", arguments, err, encoded)
		}
	}
	registry := evaldata.Registry{Version: evaldata.RegistryVersion, Datasets: []evaldata.Dataset{{
		ID: "fixture", Title: "Fixture", Role: "role", License: evaldata.License{SPDX: "CC0-1.0", URL: "https://example.test/license"},
		Source: evaldata.Source{Kind: "git", URL: "https://example.test/repository.git", Revision: strings.Repeat("f", 40)},
	}}}
	if _, err := evaldata.Prepare(context.Background(), registry, strings.Repeat("f", 64), evaldata.Options{Root: root, VerifyOnly: true}); err == nil || !strings.Contains(err.Error(), "revision mismatch") {
		t.Fatalf("Prepare() error = %v", err)
	}
}

func TestCommittedRegistryLoads(t *testing.T) {
	path, err := filepath.Abs(filepath.Join("..", "..", "benchmarks", "external", "evaluation-registry-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	registry, digest, err := evaldata.LoadRegistry(path)
	if err != nil {
		t.Fatalf("LoadRegistry() error = %v", err)
	}
	if len(registry.Datasets) != 5 || len(digest) != 64 {
		t.Fatalf("registry datasets/digest = %d/%q", len(registry.Datasets), digest)
	}
}

func HTTPRegistry(url, path string, payload []byte, digest string) evaldata.Registry {
	return evaldata.Registry{Version: evaldata.RegistryVersion, Datasets: []evaldata.Dataset{{
		ID: "fixture", Title: "Fixture", Role: "role", License: evaldata.License{SPDX: "CC0-1.0", URL: "https://example.test/license"},
		Source: evaldata.Source{Kind: "http", Artifacts: []evaldata.Artifact{{Path: path, URL: url, Bytes: int64(len(payload)), SHA256: digest}}},
	}}}
}
