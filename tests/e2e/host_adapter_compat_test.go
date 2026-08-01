//go:build e2e

package e2e_test

import (
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/claudeadapter"
	"github.com/HW-Yue/Memora/internal/codexadapter"
)

func TestCodexAndClaudeAdaptersBindTheSameCanonicalProtocol(t *testing.T) {
	t.Parallel()

	root := e2eRepositoryRoot(t)
	canonical := filepath.Join(root, "skills", "memora")
	codex, err := codexadapter.Build(canonical)
	if err != nil {
		t.Fatal(err)
	}
	claude, err := claudeadapter.Build(canonical)
	if err != nil {
		t.Fatal(err)
	}
	if codex.Manifest.CanonicalDigest != claude.Manifest.CanonicalDigest {
		t.Fatalf("host canonical digests differ: %q != %q", codex.Manifest.CanonicalDigest, claude.Manifest.CanonicalDigest)
	}
	if codex.Manifest.ProtocolDigest != claude.Manifest.ProtocolDigest {
		t.Fatalf("host protocol digests differ: %q != %q", codex.Manifest.ProtocolDigest, claude.Manifest.ProtocolDigest)
	}
	if codex.Manifest.TaskContractDigest != claude.Manifest.TaskContractDigest {
		t.Fatalf("host Task contract digests differ: %q != %q",
			codex.Manifest.TaskContractDigest, claude.Manifest.TaskContractDigest)
	}
}
