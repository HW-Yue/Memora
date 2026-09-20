package devgate

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("no caller path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
}

func TestCIFormatStageSucceeds(t *testing.T) {
	cmd := exec.Command("bash", "scripts/ci.sh", "--stage", "format")
	cmd.Dir = repoRoot(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("format stage: %v\n%s", err, out)
	}
}

func TestCIStagesMatchSQLiteKernel(t *testing.T) {
	cmd := exec.Command("bash", "scripts/ci.sh", "--list")
	cmd.Dir = repoRoot(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("list stages: %v\n%s", err, out)
	}
	got := strings.Fields(string(out))
	want := []string{"format", "vet", "lint", "unit", "race", "cgo-build"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("stages = %q, want %q", got, want)
	}
}

func TestCIScriptDoesNotDisableCGO(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(repoRoot(t), "scripts/ci.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte("CGO_ENABLED=0 GOOS=")) || bytes.Contains(body, []byte("CGO_ENABLED=0 \"$go_command\" build")) {
		t.Fatal("ci.sh still builds with CGO_ENABLED=0; go-sqlite3 would link a mock that cannot open a database")
	}
	if !bytes.Contains(body, []byte("cgo-build")) {
		t.Fatal("ci.sh has no cgo-build stage")
	}
}

func TestCGOBuildRefusesDisabledCGO(t *testing.T) {
	cmd := exec.Command("bash", "scripts/ci.sh", "--stage", "cgo-build")
	cmd.Dir = repoRoot(t)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("cgo-build with CGO_ENABLED=0 succeeded:\n%s", out)
	}
	if !bytes.Contains(out, []byte("CGO_ENABLED=0")) {
		t.Fatalf("cgo-build failure did not mention CGO_ENABLED=0:\n%s", out)
	}
}

func TestReleaseWorkflowDoesNotCallMissingTools(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(repoRoot(t), ".github/workflows/release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, missing := range []string{
		"scripts/publication.sh",
		"scripts/smoke-release.sh",
		"scripts/clean-machine-acceptance.sh",
		"cmd/verify-publication",
		"cmd/validate-release-trigger",
		"cmd/build-publication",
		"cmd/verify-clean-machine-acceptance",
		"cmd/validate-release-draft",
	} {
		if bytes.Contains(body, []byte(missing)) {
			t.Errorf("release.yml still calls missing %s", missing)
		}
	}
}

func TestSkillSurfaceMatchesLiveCLI(t *testing.T) {
	root := repoRoot(t)
	help := cliHelp(t, root)
	deleted := []string{
		"memora assimilate",
		"memora capture",
		"memora decide",
		"memora feedback",
		"memora maintain",
		"memora reflect",
		"memora reindex",
		"memora upgrade",
		"doctor repair",
	}
	skillFiles := []string{
		"skills/memora/SKILL.md",
		"skills/memora/contract.json",
		"skills/memora/host-contract.json",
		"skills/memora/references/product-manual.md",
		"adapters/codex/.agents/skills/memora/SKILL.md",
		"adapters/claude-code/.claude/skills/memora/SKILL.md",
	}
	for _, rel := range skillFiles {
		body, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		for _, cmd := range deleted {
			if bytes.Contains(body, []byte(cmd)) {
				t.Errorf("%s still teaches %s", rel, cmd)
			}
		}
	}

	canonical, err := os.ReadFile(filepath.Join(root, "skills/memora/SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{
		"adapters/codex/.agents/skills/memora/SKILL.md",
		"adapters/claude-code/.claude/skills/memora/SKILL.md",
	} {
		got, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(canonical, got) {
			t.Errorf("%s does not match skills/memora/SKILL.md", rel)
		}
	}

	canonicalContract, err := os.ReadFile(filepath.Join(root, "skills/memora/contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{
		"adapters/codex/.agents/skills/memora/contract.json",
		"adapters/claude-code/.claude/skills/memora/contract.json",
	} {
		got, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(canonicalContract, got) {
			t.Errorf("%s does not match skills/memora/contract.json", rel)
		}
	}

	raw, err := os.ReadFile(filepath.Join(root, "skills/memora/contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		AllowedCommands []string `json:"allowed_commands"`
	}
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatal(err)
	}
	if len(contract.AllowedCommands) == 0 {
		t.Fatal("contract.json has no allowed_commands")
	}
	for _, cmd := range contract.AllowedCommands {
		if !strings.Contains(help, cmd) {
			t.Errorf("contract.json allows %q but CLI help does not list it", cmd)
		}
	}
}

func cliHelp(t *testing.T, root string) string {
	t.Helper()
	cmd := exec.Command("go", "run", "./cmd/memora", "help")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("memora help: %v\n%s", err, out)
	}
	return string(out)
}
