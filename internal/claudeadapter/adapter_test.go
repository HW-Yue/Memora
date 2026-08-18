package claudeadapter_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/claudeadapter"
)

func TestBuildAddsOnlyClaudeHostMetadataToCanonicalSkill(t *testing.T) {
	t.Parallel()

	root := repositoryRoot(t)
	bundle, err := claudeadapter.Build(filepath.Join(root, "skills", "memora"))
	if err != nil {
		t.Fatal(err)
	}
	generated := string(bundle.Files[".claude/skills/memora/SKILL.md"].Content)
	frontmatter := strings.TrimSuffix(generated, claudeadapter.CanonicalBody(generated))
	canonical, _ := os.ReadFile(filepath.Join(root, "skills", "memora", "SKILL.md"))
	if bundle.Manifest.Version != "memora.claude-code-adapter/v2" || len(bundle.Manifest.TaskContractDigest) != 64 {
		t.Fatalf("Claude host contract identity = %#v", bundle.Manifest)
	}
	if _, ok := bundle.Files[".claude/skills/memora/host-contract.json"]; !ok {
		t.Fatal("Claude bundle omits the real host task contract")
	}
	for _, command := range []string{"assimilate", "exec", "feedback", "maintain", "mutate", "query", "reflect", "schema"} {
		if !strings.Contains(frontmatter, "Bash(memora "+command+" *)") {
			t.Errorf("Claude allowed-tools omits %q", command)
		}
	}
	if !strings.Contains(frontmatter, "Bash(memora doctor)") ||
		strings.Contains(frontmatter, "Bash(memora doctor *)") {
		t.Fatal("Claude adapter must allow only the exact read-only doctor command implicitly")
	}
	if strings.Contains(frontmatter, "install.sh") || strings.Contains(frontmatter, "Bash(memora *)") {
		t.Fatal("Claude adapter grants bootstrap or broad future command permission")
	}
	if claudeadapter.CanonicalBody(generated) != claudeadapter.CanonicalBody(string(canonical)) {
		t.Fatal("Claude adapter changed canonical Skill instructions")
	}
	for _, path := range []string{
		".claude/skills/memora/LICENSE",
		".claude/skills/memora/COMMERCIAL-LICENSE.md",
	} {
		content := string(bundle.Files[path].Content)
		if !strings.Contains(content, "Commercial use requires a separate paid commercial license") &&
			!strings.Contains(content, "Any commercial use requires a separate written, paid commercial") {
			t.Errorf("Claude bundle legal file %q omits the commercial-use boundary", path)
		}
	}
}

func TestCheckedInClaudeAdapterMatchesDeterministicGenerator(t *testing.T) {
	t.Parallel()

	root := repositoryRoot(t)
	bundle, err := claudeadapter.Build(filepath.Join(root, "skills", "memora"))
	if err != nil {
		t.Fatal(err)
	}
	if err := bundle.Compare(filepath.Join(root, "adapters", "claude-code")); err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	if err := bundle.Install(destination); err != nil {
		t.Fatal(err)
	}
	if err := bundle.Install(destination); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(destination, ".claude", "skills", "memora", "scripts", "install.sh"))
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("Claude bootstrap mode = %v, %v", info, err)
	}
	// SKILL.md tells the host to run check.sh and to read the product manual.
	// Shipping SKILL.md without them leaves the installed Skill pointing at
	// files that do not exist.
	check, err := os.Stat(filepath.Join(destination, ".claude", "skills", "memora", "scripts", "check.sh"))
	if err != nil || check.Mode().Perm() != 0o755 {
		t.Fatalf("Claude check script mode = %v, %v", check, err)
	}
	manual, err := os.Stat(filepath.Join(
		destination, ".claude", "skills", "memora", "references", "product-manual.md",
	))
	if err != nil || manual.Size() == 0 {
		t.Fatalf("Claude product manual = %v, %v", manual, err)
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
