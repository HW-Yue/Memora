package codexadapter_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/codexadapter"
)

func TestBuildUsesCanonicalSkillAndLeastPrivilegeCodexSurfaces(t *testing.T) {
	t.Parallel()

	root := repositoryRoot(t)
	bundle, err := codexadapter.Build(filepath.Join(root, "skills", "memora"))
	if err != nil {
		t.Fatal(err)
	}
	canonical, _ := os.ReadFile(filepath.Join(root, "skills", "memora", "SKILL.md"))
	if got := string(bundle.Files[".agents/skills/memora/SKILL.md"].Content); got != string(canonical) {
		t.Fatal("Codex Skill drifted from the canonical source")
	}
	if bundle.Manifest.Version != "memora.codex-adapter/v2" || len(bundle.Manifest.TaskContractDigest) != 64 {
		t.Fatalf("Codex host contract identity = %#v", bundle.Manifest)
	}
	if _, ok := bundle.Files[".agents/skills/memora/host-contract.json"]; !ok {
		t.Fatal("Codex bundle omits the real host task contract")
	}
	for _, path := range []string{
		".agents/skills/memora/LICENSE",
		".agents/skills/memora/COMMERCIAL-LICENSE.md",
	} {
		content := string(bundle.Files[path].Content)
		if !strings.Contains(content, "Commercial use requires a separate paid commercial license") &&
			!strings.Contains(content, "Any commercial use requires a separate written, paid commercial") {
			t.Errorf("Codex bundle legal file %q omits the commercial-use boundary", path)
		}
	}
	metadata := string(bundle.Files[".agents/skills/memora/agents/openai.yaml"].Content)
	for _, token := range []string{`display_name: "Memora"`, "allow_implicit_invocation: true"} {
		if !strings.Contains(metadata, token) {
			t.Errorf("Codex metadata omits %q", token)
		}
	}
	rules := string(bundle.Files[".codex/rules/memora.rules"].Content)
	for _, command := range []string{"assimilate", "doctor", "exec", "feedback", "maintain", "mutate", "query", "reflect", "schema"} {
		if !strings.Contains(rules, `"`+command+`"`) {
			t.Errorf("Codex rules omit %q", command)
		}
	}
	for _, forbidden := range []string{`pattern = ["memora"]`, "decision = \"forbidden\""} {
		if strings.Contains(rules, forbidden) {
			t.Errorf("Codex rules contain unsafe broad token %q", forbidden)
		}
	}
	for _, approvalGated := range []string{"memora upgrade --apply", "memora doctor repair"} {
		if !strings.Contains(rules, `"`+approvalGated+`"`) {
			t.Errorf("Codex rules do not approval-gate %q", approvalGated)
		}
	}
}

func TestInstallCreatesDiscoverableCodexBundleDeterministically(t *testing.T) {
	t.Parallel()

	root := repositoryRoot(t)
	bundle, err := codexadapter.Build(filepath.Join(root, "skills", "memora"))
	if err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	if err := bundle.Install(destination); err != nil {
		t.Fatal(err)
	}
	firstManifest, err := os.ReadFile(filepath.Join(destination, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := bundle.Install(destination); err != nil {
		t.Fatal(err)
	}
	secondManifest, _ := os.ReadFile(filepath.Join(destination, "manifest.json"))
	if string(firstManifest) != string(secondManifest) {
		t.Fatal("Codex adapter manifest is not deterministic")
	}
	info, err := os.Stat(filepath.Join(destination, ".agents", "skills", "memora", "scripts", "install.sh"))
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("bootstrap mode = %v, %v", info, err)
	}
}

func TestCheckedInCodexAdapterMatchesGenerator(t *testing.T) {
	t.Parallel()

	root := repositoryRoot(t)
	bundle, err := codexadapter.Build(filepath.Join(root, "skills", "memora"))
	if err != nil {
		t.Fatal(err)
	}
	if err := bundle.Compare(filepath.Join(root, "adapters", "codex")); err != nil {
		t.Fatal(err)
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
