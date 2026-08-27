package install_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBootstrapRequiresConsentAndSupportedMacArchitecture(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t)
	result := fixture.run()
	if result.code == 0 || !strings.Contains(result.stderr, "requires explicit user authorization") {
		t.Fatalf("without consent code=%d stderr=%q", result.code, result.stderr)
	}
	if _, err := os.Stat(fixture.binary); !os.IsNotExist(err) {
		t.Fatalf("bootstrap changed install target without consent: %v", err)
	}

	result = fixture.run("--yes", "--os", "linux")
	if result.code == 0 || !strings.Contains(result.stderr, "supports only macOS") {
		t.Fatalf("linux code=%d stderr=%q", result.code, result.stderr)
	}
	result = fixture.run("--yes", "--arch", "386")
	if result.code == 0 || !strings.Contains(result.stderr, "unsupported architecture") {
		t.Fatalf("386 code=%d stderr=%q", result.code, result.stderr)
	}
}

func TestBootstrapInstallsVerifiedReleaseUpgradesOldBinaryAndRepeatsInit(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t)
	fixture.release("0.1.0", false)
	writeExecutable(t, fixture.binary, fakeMemora("0.0.9"))
	first := fixture.run("--yes")
	if first.code != 0 || !strings.Contains(first.stdout, "installed Memora 0.1.0") {
		t.Fatalf("first install code=%d stdout=%q stderr=%q", first.code, first.stdout, first.stderr)
	}
	installed, err := os.ReadFile(fixture.binary)
	if err != nil || !bytes.Contains(installed, []byte(`"version":"0.1.0"`)) {
		t.Fatalf("installed binary = %q, %v", installed, err)
	}
	firstDownloads := countLines(t, fixture.downloadLog)
	if firstDownloads != 2 {
		t.Fatalf("release downloads = %d, want archive and checksums", firstDownloads)
	}

	second := fixture.run("--yes")
	if second.code != 0 || !strings.Contains(second.stdout, "already installed") {
		t.Fatalf("repeat code=%d stdout=%q stderr=%q", second.code, second.stdout, second.stderr)
	}
	if got := countLines(t, fixture.downloadLog); got != firstDownloads {
		t.Fatalf("repeat downloaded release again: %d -> %d", firstDownloads, got)
	}
	commands, _ := os.ReadFile(fixture.commandLog)
	if strings.Count(string(commands), "init") != 2 || strings.Count(string(commands), "doctor") != 2 {
		t.Fatalf("post-install calls = %q", commands)
	}
}

func TestBootstrapRejectsChecksumFailureWithoutReplacingOldBinary(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t)
	fixture.release("0.1.0", true)
	old := fakeMemora("0.0.9")
	writeExecutable(t, fixture.binary, old)
	result := fixture.run("--yes")
	if result.code == 0 || !strings.Contains(result.stderr, "checksum verification failed") {
		t.Fatalf("checksum failure code=%d stderr=%q", result.code, result.stderr)
	}
	got, err := os.ReadFile(fixture.binary)
	if err != nil || string(got) != old {
		t.Fatalf("checksum failure replaced old binary: %v", err)
	}
	if _, err := os.Stat(fixture.goLog); !os.IsNotExist(err) {
		t.Fatalf("checksum failure attempted source fallback: %v", err)
	}
}

func TestBootstrapDiagnosesGatekeeperBlockWithoutReplacingOldBinary(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t)
	fixture.releaseContent("0.1.0", "#!/bin/sh\nexit 126\n", false)
	old := fakeMemora("0.0.9")
	writeExecutable(t, fixture.binary, old)
	result := fixture.run("--yes")
	if result.code == 0 || !strings.Contains(result.stderr, "macOS Gatekeeper blocked") ||
		!strings.Contains(result.stderr, "Privacy & Security") {
		t.Fatalf("Gatekeeper failure code=%d stderr=%q", result.code, result.stderr)
	}
	got, err := os.ReadFile(fixture.binary)
	if err != nil || string(got) != old {
		t.Fatalf("Gatekeeper failure replaced old binary: %v", err)
	}
}

func TestBootstrapFallsBackToGoOnlyWhenReleaseUnavailable(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t)
	result := fixture.run("--yes")
	if result.code != 0 || !strings.Contains(result.stdout, "built Memora 0.1.0 from source") {
		t.Fatalf("source fallback code=%d stdout=%q stderr=%q", result.code, result.stdout, result.stderr)
	}
	if _, err := os.Stat(fixture.goLog); err != nil {
		t.Fatalf("Go fallback was not called: %v", err)
	}

	withoutGo := newFixture(t)
	if err := os.Remove(filepath.Join(withoutGo.tools, "go")); err != nil {
		t.Fatal(err)
	}
	result = withoutGo.run("--yes")
	if result.code == 0 || !strings.Contains(result.stderr, "release unavailable and Go is not installed") {
		t.Fatalf("offline without Go code=%d stderr=%q", result.code, result.stderr)
	}
}

type fixture struct {
	t            *testing.T
	root         string
	tools        string
	releaseDir   string
	installDir   string
	binary       string
	dataDir      string
	commandLog   string
	downloadLog  string
	goLog        string
	sourceBinary string
}

type commandResult struct {
	code           int
	stdout, stderr string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	value := &fixture{
		t: t, root: root, tools: filepath.Join(root, "tools"), releaseDir: filepath.Join(root, "release"),
		installDir: filepath.Join(root, "bin"), dataDir: filepath.Join(root, "data"),
		commandLog: filepath.Join(root, "commands.log"), downloadLog: filepath.Join(root, "downloads.log"),
		goLog: filepath.Join(root, "go.log"),
	}
	value.binary = filepath.Join(value.installDir, "memora")
	for _, directory := range []string{value.tools, value.releaseDir, value.installDir} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeExecutable(t, filepath.Join(value.tools, "curl"), `#!/bin/sh
set -eu
out=""
url=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output|-o) out="$2"; shift 2 ;;
    http*) url="$1"; shift ;;
    *) shift ;;
  esac
done
printf '%s\n' "$url" >> "$FAKE_DOWNLOAD_LOG"
name=${url##*/}
[ -f "$FAKE_RELEASE_DIR/$name" ] || exit 22
cp "$FAKE_RELEASE_DIR/$name" "$out"
`)
	writeExecutable(t, filepath.Join(value.tools, "go"), `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$FAKE_GO_LOG"
out=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-o" ]; then out="$2"; break; fi
  shift
done
[ -n "$out" ] || out="$GOBIN/memora"
mkdir -p "${out%/*}"
cp "$FAKE_SOURCE_BINARY" "$out"
chmod 755 "$out"
`)
	writeExecutable(t, filepath.Join(value.tools, "spctl"), "#!/bin/sh\nexit 1\n")
	linkUtilities(t, value.tools)
	value.sourceBinary = filepath.Join(root, "source-memora")
	writeExecutable(t, value.sourceBinary, fakeMemora("0.1.0"))
	return value
}

// The fixture's PATH is its own tools directory and nothing else, so the real
// utilities install.sh needs are linked into it. Half of what these tests
// assert is about a program being *absent*: the source-fallback case deletes
// the fake go to check that the installer reports Go is not installed, and
// that is only true if the machine underneath cannot supply one. Leaving
// /usr/bin on the PATH made it depend on the runner — GitHub's Linux image
// ships a go, so the installer found it, built the real module, and the
// assertion failed on a difference between runners rather than in the code.
//
// curl, go and spctl are deliberately not in this list: those three are the
// fakes, and linking the real ones would let a test reach the network.
var fixtureUtilities = []string{
	"awk", "chmod", "cp", "grep", "gzip", "head", "mkdir", "mktemp",
	"mv", "rm", "sed", "shasum", "sort", "tar", "tr", "uname",
}

func linkUtilities(t *testing.T, tools string) {
	t.Helper()
	for _, name := range fixtureUtilities {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Fatalf("fixture utility %q is not on this machine: %v", name, err)
		}
		if err := os.Symlink(path, filepath.Join(tools, name)); err != nil {
			t.Fatal(err)
		}
	}
}

func (fixture *fixture) release(version string, badChecksum bool) {
	fixture.releaseContent(version, fakeMemora(version), badChecksum)
}

func (fixture *fixture) releaseContent(version, binary string, badChecksum bool) {
	fixture.t.Helper()
	asset := "memora_" + version + "_darwin_arm64.tar.gz"
	archive := filepath.Join(fixture.releaseDir, asset)
	file, err := os.Create(archive)
	if err != nil {
		fixture.t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range []struct {
		name    string
		mode    int64
		content string
	}{
		{name: "memora", mode: 0o755, content: binary},
		{name: "LICENSE", mode: 0o644, content: "Fixture license\n"},
		{name: "COMMERCIAL-LICENSE.md", mode: 0o644, content: "Fixture commercial terms\n"},
		{name: "README.md", mode: 0o644, content: "# Fixture\n"},
	} {
		content := []byte(entry.content)
		if err := tarWriter.WriteHeader(&tar.Header{
			Name: entry.name, Mode: entry.mode, Size: int64(len(content)),
		}); err != nil {
			fixture.t.Fatal(err)
		}
		if _, err := tarWriter.Write(content); err != nil {
			fixture.t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		fixture.t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		fixture.t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		fixture.t.Fatal(err)
	}
	encoded, _ := os.ReadFile(archive)
	digest := fmt.Sprintf("%x", sha256.Sum256(encoded))
	if badChecksum {
		digest = strings.Repeat("0", 64)
	}
	if err := os.WriteFile(filepath.Join(fixture.releaseDir, "checksums.txt"), []byte(digest+"  "+asset+"\n"), 0o644); err != nil {
		fixture.t.Fatal(err)
	}
}

func (fixture *fixture) run(arguments ...string) commandResult {
	fixture.t.Helper()
	script := filepath.Join(repositoryRoot(fixture.t), "skills", "memora", "scripts", "install.sh")
	args := []string{script, "--version", "0.1.0", "--install-dir", fixture.installDir, "--data-dir", fixture.dataDir, "--os", "darwin", "--arch", "arm64"}
	args = append(args, arguments...)
	command := exec.Command("/bin/sh", args...)
	command.Env = append(os.Environ(),
		"PATH="+fixture.tools,
		"MEMORA_RELEASE_BASE=https://example.invalid/releases/download",
		"FAKE_RELEASE_DIR="+fixture.releaseDir,
		"FAKE_DOWNLOAD_LOG="+fixture.downloadLog,
		"FAKE_GO_LOG="+fixture.goLog,
		"FAKE_SOURCE_BINARY="+fixture.sourceBinary,
		"MEMORA_INSTALL_COMMAND_LOG="+fixture.commandLog,
	)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	code := 0
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		} else {
			fixture.t.Fatal(err)
		}
	}
	return commandResult{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func fakeMemora(version string) string {
	return fmt.Sprintf(`#!/bin/sh
set -eu
if [ "$1" = "version" ]; then printf '{"name":"memora","version":"%s","commit":"test","built_at":"test"}\n'; exit 0; fi
if [ -n "${MEMORA_INSTALL_COMMAND_LOG:-}" ]; then printf '%%s\n' "$1" >> "$MEMORA_INSTALL_COMMAND_LOG"; fi
case "$1" in
  init) exit 0 ;;
  daemon) [ "$2" = "ping" ] && exit 1; [ "$2" = "start" ] && exit 0; exit 2 ;;
  doctor) printf '{"status":"healthy"}\n'; exit 0 ;;
esac
exit 2
`, version)
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return len(strings.Fields(string(encoded)))
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
