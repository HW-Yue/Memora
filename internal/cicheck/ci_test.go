package cicheck_test

import (
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
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

func TestRuntimeAndHostContractsContainNoRetiredRowRetrieval(t *testing.T) {
	t.Parallel()

	root := repositoryRoot(t)
	forbidden := []string{
		"\"" + "MAT" + "CH" + "\"",
		"FROM DATA" + "BASE",
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
				if token != "\"MATCH\"" && token != "FROM DATABASE" {
					candidate = strings.ToLower(token)
				}
				if strings.Contains(string(content), candidate) ||
					(token != "\"MATCH\"" && token != "FROM DATABASE" && strings.Contains(lower, candidate)) {
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

func TestHistoricalAIHarnessesStayOutOfActiveTree(t *testing.T) {
	t.Parallel()

	root := repositoryRoot(t)
	for _, name := range []string{
		"internal/benchmark",
		"internal/runtimegate",
		"benchmarks/ai-native-v1.json",
	} {
		if _, err := os.Stat(filepath.Join(root, name)); err == nil {
			t.Errorf("historical AI harness remains active: %s", name)
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("inspect %s: %v", name, err)
		}
	}
}

func TestHardCodedSkillQueryRunnerStaysOutOfActiveTree(t *testing.T) {
	t.Parallel()

	path := filepath.Join(repositoryRoot(t), "internal", "skillquery")
	if _, err := os.Stat(path); err == nil {
		t.Fatal("hard-coded Skill query runner remains active: internal/skillquery")
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inspect internal/skillquery: %v", err)
	}
}

func TestRetiredRowAgentIndexGenerationStaysOutOfActiveTree(t *testing.T) {
	t.Parallel()

	path := filepath.Join(repositoryRoot(t), "internal", "generation")
	if _, err := os.Stat(path); err == nil {
		t.Fatal("retired Row Agent-index generation service remains active: internal/generation")
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inspect internal/generation: %v", err)
	}
}

func TestRoutePredictorPackagesDoNotImportFactStores(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	forbidden := map[string]struct{}{
		"github.com/HW-Yue/Memora/internal/row":       {},
		"github.com/HW-Yue/Memora/internal/history":   {},
		"github.com/HW-Yue/Memora/internal/relation":  {},
		"github.com/HW-Yue/Memora/internal/nativerow": {},
	}
	for _, name := range []string{"routelexical", "routevector", "routeexact"} {
		directory := filepath.Join(root, "internal", name)
		if _, err := os.Stat(directory); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			t.Fatal(err)
		}
		err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
				return err
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			for _, imported := range file.Imports {
				path, err := strconv.Unquote(imported.Path.Value)
				if err != nil {
					return err
				}
				if _, blocked := forbidden[path]; blocked {
					return fmt.Errorf("%s imports fact store %q", name, path)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestAgentPackagesOnlyImportNeutralMSQLProtocol(t *testing.T) {
	t.Parallel()

	directory := filepath.Join(repositoryRoot(t), "internal", "agent")
	if err := checkAgentImports(directory); err != nil {
		t.Fatal(err)
	}
}

func TestAgentImportGuardRejectsDatabaseRuntimeDependency(t *testing.T) {
	t.Parallel()

	directory := filepath.Join(t.TempDir(), "agent")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte("package agent\nimport _ \"github.com/HW-Yue/Memora/internal/store\"\n")
	if err := os.WriteFile(filepath.Join(directory, "bad.go"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkAgentImports(directory); err == nil || !strings.Contains(err.Error(), "internal/store") {
		t.Fatalf("checkAgentImports() error = %v", err)
	}
}

func checkAgentImports(directory string) error {
	const (
		modulePrefix    = "github.com/HW-Yue/Memora/"
		agentPrefix     = modulePrefix + "internal/agent"
		neutralProtocol = modulePrefix + "protocol/msql"
	)
	return filepath.WalkDir(directory, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || filepath.Ext(path) != ".go" {
			return walkErr
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		testFile := strings.HasSuffix(path, "_test.go")
		for _, imported := range file.Imports {
			importPath, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				return err
			}
			if importPath == neutralProtocol || standardLibraryImport(importPath) {
				continue
			}
			if testFile && (importPath == agentPrefix || strings.HasPrefix(importPath, agentPrefix+"/")) {
				continue
			}
			return fmt.Errorf("%s imports forbidden Agent dependency %q", path, importPath)
		}
		return nil
	})
}

func standardLibraryImport(importPath string) bool {
	first, _, _ := strings.Cut(importPath, "/")
	return first != "" && !strings.Contains(first, ".")
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
