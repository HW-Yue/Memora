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
	for _, command := range []string{"assimilate", "doctor", "exec", "feedback", "maintain", "mutate", "query", "reflect", "schema"} {
		if !strings.Contains(frontmatter, "Bash(memora "+command+" *)") {
			t.Errorf("Claude allowed-tools omits %q", command)
		}
	}
	if strings.Contains(frontmatter, "install.sh") || strings.Contains(frontmatter, "Bash(memora *)") {
		t.Fatal("Claude adapter grants bootstrap or broad future command permission")
	}
	if claudeadapter.CanonicalBody(generated) != claudeadapter.CanonicalBody(string(canonical)) {
		t.Fatal("Claude adapter changed canonical Skill instructions")
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
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
