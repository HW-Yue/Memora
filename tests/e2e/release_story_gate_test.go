//go:build e2e

package e2e_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/storygate"
)

func TestReleaseBinaryPassesRealCodexAndClaudeStoryJourneys(t *testing.T) {
	root := e2eRepositoryRoot(t)
	sandbox := t.TempDir()
	binary := filepath.Join(sandbox, "memora")
	e2eCommand(t, root, "go", "build", "-o", binary, "./cmd/memora")
	canonical := filepath.Join(root, "skills", "memora")
	reports := map[string]storygate.Report{}
	for _, host := range []string{"codex", "claude"} {
		report, err := storygate.Run(context.Background(), storygate.RunOptions{
			Binary: binary, DataDir: filepath.Join(sandbox, host, "instance"),
			CanonicalDir: canonical, AdapterDir: filepath.Join(sandbox, host, "adapter"), Host: host,
		})
		if err != nil {
			t.Fatalf("%s runtime story gate: %v\n%#v", host, err, report)
		}
		reports[host] = report
	}
	if err := storygate.ValidatePair(reports["codex"], reports["claude"]); err != nil {
		t.Fatal(err)
	}
}
