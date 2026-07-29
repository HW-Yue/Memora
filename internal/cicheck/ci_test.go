package cicheck_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCIScriptListsStages(t *testing.T) {
	t.Parallel()

	root := repositoryRoot(t)
	command := exec.Command(filepath.Join(root, "scripts", "ci.sh"), "--list")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("ci.sh --list error = %v, output = %s", err, output)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "stages.golden"))
	if err != nil {
		t.Fatalf("read stages golden: %v", err)
	}
	if got := string(output); got != string(want) {
		t.Fatalf("stage list mismatch\n--- got ---\n%s--- want ---\n%s", got, want)
	}
}

func TestCIScriptPropagatesTestFailure(t *testing.T) {
	t.Parallel()

	root := repositoryRoot(t)
	fakeGo := executable(t, "fake-go", "#!/bin/sh\nexit 17\n")
	command := exec.Command(filepath.Join(root, "scripts", "ci.sh"), "--stage", "unit")
	command.Env = append(os.Environ(), "MEMORA_CI_GO="+fakeGo)
	err := command.Run()
	exitError, ok := err.(*exec.ExitError)
	if !ok || exitError.ExitCode() != 17 {
		t.Fatalf("ci.sh failure = %v, want exit code 17", err)
	}
}

func TestCIScriptRejectsUnformattedFiles(t *testing.T) {
	t.Parallel()

	root := repositoryRoot(t)
	fakeGofmt := executable(t, "fake-gofmt", "#!/bin/sh\nprintf 'internal/bad.go\\n'\n")
	command := exec.Command(filepath.Join(root, "scripts", "ci.sh"), "--stage", "format")
	command.Env = append(os.Environ(), "MEMORA_CI_GOFMT="+fakeGofmt)
	err := command.Run()
	exitError, ok := err.(*exec.ExitError)
	if !ok || exitError.ExitCode() != 1 {
		t.Fatalf("ci.sh format failure = %v, want exit code 1", err)
	}
}

func TestGitHubWorkflowContract(t *testing.T) {
	t.Parallel()

	root := repositoryRoot(t)
	content, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("read CI workflow: %v", err)
	}
	workflow := string(content)
	for _, required := range []string{
		"pull_request:",
		"push:",
		"contents: read",
		"runs-on: macos-latest",
		"actions/checkout@v6",
		"actions/setup-go@v6",
		"go-version-file: go.mod",
		"./scripts/ci.sh",
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("workflow missing %q", required)
		}
	}
	for _, forbidden := range []string{"release create", "upload-artifact", "contents: write"} {
		if strings.Contains(workflow, forbidden) {
			t.Errorf("PR CI contains publishing capability %q", forbidden)
		}
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test file")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
}

func executable(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatalf("write executable: %v", err)
	}
	return path
}
