package cicheck_test

import (
	"fmt"
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

func TestGitHubReleaseWorkflowIsTagOnlyAndLeastPrivilege(t *testing.T) {
	t.Parallel()

	root := repositoryRoot(t)
	content, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatalf("read Release workflow: %v", err)
	}
	workflow := string(content)
	for _, required := range []string{
		"tags:",
		"contents: read",
		"contents: write",
		"persist-credentials: false",
		"validate-release-trigger",
		"/git/tags/",
		"./scripts/ci.sh",
		"macos-15-intel",
		"arch: arm64",
		"arch: amd64",
		"verify-publication",
		"clean-machine-acceptance.sh",
		"verify-clean-machine-acceptance",
		"memora-acceptance-",
		"- acceptance",
		"validate-release-draft",
		"--draft",
		"--generate-notes",
		"gh release upload",
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("Release workflow missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"pull_request:",
		"pull_request_target:",
		"workflow_dispatch:",
		"branches:",
		"--cleanup-tag",
	} {
		if strings.Contains(workflow, forbidden) {
			t.Errorf("Release workflow contains forbidden trigger or mutation %q", forbidden)
		}
	}
	if strings.Index(workflow, "contents: write") < strings.Index(workflow, "publish:") {
		t.Error("Release workflow grants write permission before the publish job")
	}
}

func TestRuntimeAndHostContractsContainNoRetiredSemanticRetrieval(t *testing.T) {
	t.Parallel()

	root := repositoryRoot(t)
	forbidden := []string{
		"MAT" + "CH ",
		"FROM DATA" + "BASE",
		"cos" + "ine",
		"vec" + "tor",
		"embed" + "ding",
		"query" + "_terms",
		"index" + "_terms",
		"agent" + "index",
		"mechanical" + "index",
		"retry" + "_reindex",
		"charN" + "Gram",
	}
	for _, directory := range []string{"cmd", "internal", "skills", "adapters", "benchmarks", "scripts"} {
		err := filepath.WalkDir(filepath.Join(root, directory), func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || path == filepath.Join(root, "internal", "cicheck", "ci_test.go") {
				return nil
			}
			switch filepath.Ext(path) {
			case ".go", ".json", ".md", ".sh", ".yaml", ".rules", ".golden":
			default:
				return nil
			}
			content, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			lower := strings.ToLower(string(content))
			for _, token := range forbidden {
				candidate := token
				if token != "MATCH " && token != "FROM DATABASE" {
					candidate = strings.ToLower(token)
				}
				if strings.Contains(string(content), candidate) ||
					(token != "MATCH " && token != "FROM DATABASE" && strings.Contains(lower, candidate)) {
					return fmt.Errorf("%s contains retired retrieval token %q", path, token)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestPersonalFreeCommercialPaidLicenseShipsEverywhere(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	files := []string{
		"LICENSE", "COMMERCIAL-LICENSE.md",
		"adapters/codex/.agents/skills/memora/LICENSE",
		"adapters/codex/.agents/skills/memora/COMMERCIAL-LICENSE.md",
		"adapters/claude-code/.claude/skills/memora/LICENSE",
		"adapters/claude-code/.claude/skills/memora/COMMERCIAL-LICENSE.md",
	}
	for _, name := range files {
		content, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Errorf("read %s: %v", name, err)
			continue
		}
		text := string(content)
		if !strings.Contains(text, "commercial") && !strings.Contains(text, "Commercial") {
			t.Errorf("%s omits commercial-use terms", name)
		}
	}
	license, _ := os.ReadFile(filepath.Join(root, "LICENSE"))
	for _, required := range []string{"PolyForm Noncommercial License 1.0.0", "Personal Uses", "Commercial use requires a separate paid commercial license"} {
		if !strings.Contains(string(license), required) {
			t.Errorf("root license omits %q", required)
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
