package storygate_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/HW-Yue/Memora/internal/storygate"
)

func TestF72ReleaseEvidenceCoversEveryProductStoryWithRealProofFiles(t *testing.T) {
	t.Parallel()
	evidence := storygate.ReleaseEvidence()
	if err := storygate.Validate(evidence); err != nil {
		t.Fatal(err)
	}
	root := repositoryRoot(t)
	for _, item := range evidence {
		for _, proof := range item.Proofs {
			if info, err := os.Stat(filepath.Join(root, proof)); err != nil || info.IsDir() {
				t.Errorf("%s proof %q is unavailable: %v", item.Story, proof, err)
			}
		}
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve story gate path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
}
