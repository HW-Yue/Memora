package devgate

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
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
		"memora service",
		"UNARCHIVE",
		"SHOW ROUTE CANDIDATES",
		"SHOW LEXICAL LOCATIONS",
	}
	skillFiles := []string{
		"skills/memora/SKILL.md",
		"skills/memora/contract.json",
		"skills/memora/host-contract.json",
		"skills/memora/references/product-manual.md",
		"skills/memora/agents/openai.yaml",
		"adapters/codex/.agents/skills/memora/SKILL.md",
		"adapters/codex/.agents/skills/memora/agents/openai.yaml",
		"adapters/codex/.codex/rules/memora.rules",
		"adapters/claude-code/.claude/skills/memora/SKILL.md",
		"scripts/prototype_smoke.py",
		"docs/development/macos-launch-agent-v1.md",
		"internal/adminui/dist/index.html",
		"internal/adminui/dist/assets/app.js",
		"internal/adminui/dist/assets/catalog.js",
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

	shared := []string{
		"SKILL.md",
		"contract.json",
		"host-contract.json",
		"references/product-manual.md",
		"scripts/check.sh",
		"scripts/install.sh",
	}
	for _, rel := range shared {
		canonical, err := os.ReadFile(filepath.Join(root, "skills/memora", rel))
		if err != nil {
			t.Fatal(err)
		}
		for _, copyRel := range []string{
			filepath.Join("adapters/codex/.agents/skills/memora", rel),
			filepath.Join("adapters/claude-code/.claude/skills/memora", rel),
		} {
			got, err := os.ReadFile(filepath.Join(root, copyRel))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(canonical, got) {
				t.Errorf("%s does not match skills/memora/%s", copyRel, rel)
			}
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

func TestInstallScriptEnablesCGOForSourceBuild(t *testing.T) {
	root := repoRoot(t)
	for _, rel := range []string{
		"skills/memora/scripts/install.sh",
		"adapters/codex/.agents/skills/memora/scripts/install.sh",
		"adapters/claude-code/.claude/skills/memora/scripts/install.sh",
	} {
		body, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(body, []byte(`GOBIN="$work_dir/go-bin" go install`)) {
			t.Errorf("%s go install path does not enable CGO", rel)
		}
		if !bytes.Contains(body, []byte("CGO_ENABLED=1")) {
			t.Errorf("%s has no CGO_ENABLED=1", rel)
		}
	}
}

func TestAdapterManifestsMatchFiles(t *testing.T) {
	root := repoRoot(t)
	type fileEntry struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
	}
	type manifest struct {
		CanonicalDigest    string      `json:"canonical_digest"`
		ProtocolDigest     string      `json:"protocol_digest"`
		TaskContractDigest string      `json:"task_contract_digest"`
		Files              []fileEntry `json:"files"`
	}
	cases := []struct {
		dir, skill, contract, host string
	}{
		{
			dir:      "adapters/codex",
			skill:    ".agents/skills/memora/SKILL.md",
			contract: ".agents/skills/memora/contract.json",
			host:     ".agents/skills/memora/host-contract.json",
		},
		{
			dir:      "adapters/claude-code",
			skill:    ".claude/skills/memora/SKILL.md",
			contract: ".claude/skills/memora/contract.json",
			host:     ".claude/skills/memora/host-contract.json",
		},
	}
	for _, tc := range cases {
		raw, err := os.ReadFile(filepath.Join(root, tc.dir, "manifest.json"))
		if err != nil {
			t.Fatal(err)
		}
		var man manifest
		if err := json.Unmarshal(raw, &man); err != nil {
			t.Fatalf("%s/manifest.json: %v", tc.dir, err)
		}
		for _, f := range man.Files {
			data, err := os.ReadFile(filepath.Join(root, tc.dir, f.Path))
			if err != nil {
				t.Errorf("%s missing %s: %v", tc.dir, f.Path, err)
				continue
			}
			sum := sha256.Sum256(data)
			got := hex.EncodeToString(sum[:])
			if got != f.SHA256 {
				t.Errorf("%s %s sha256 = %s, manifest has %s", tc.dir, f.Path, got, f.SHA256)
			}
		}
		digest := func(rel string) string {
			t.Helper()
			data, err := os.ReadFile(filepath.Join(root, tc.dir, rel))
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(data)
			return hex.EncodeToString(sum[:])
		}
		if got := digest(tc.skill); got != man.CanonicalDigest {
			t.Errorf("%s canonical_digest = %s, SKILL.md is %s", tc.dir, man.CanonicalDigest, got)
		}
		if got := digest(tc.contract); got != man.ProtocolDigest {
			t.Errorf("%s protocol_digest = %s, contract.json is %s", tc.dir, man.ProtocolDigest, got)
		}
		if got := digest(tc.host); got != man.TaskContractDigest {
			t.Errorf("%s task_contract_digest = %s, host-contract.json is %s", tc.dir, man.TaskContractDigest, got)
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
