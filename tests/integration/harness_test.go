//go:build integration

package integration_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/testkit"
)

func TestIntegrationHarnessUsesIsolatedDatadir(t *testing.T) {
	t.Parallel()

	sandbox := testkit.NewSandbox(t)
	path, err := sandbox.Resolve("instance/instance.meta")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte("test-instance"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}
