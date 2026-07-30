package security_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/security"
)

func TestSecureOutputPathRejectsTraversalAndSymlinkParents(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"../outside.md", "/absolute.md", "escape/private.md"} {
		if _, err := security.SecureJoin(root, relative); err == nil {
			t.Fatalf("SecureJoin(%q) succeeded", relative)
		}
	}
	if got, err := security.SecureJoin(root, "db/table/row.md"); err != nil ||
		got != filepath.Join(root, "db", "table", "row.md") {
		t.Fatalf("SecureJoin() = %q, %v", got, err)
	}
}

func TestOutputRootMustBeAbsoluteNormalizedAndNotASymlink(t *testing.T) {
	t.Parallel()

	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "vault")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	unclean := target + string(filepath.Separator) + ".." + string(filepath.Separator) + filepath.Base(target)
	for _, path := range []string{"relative", unclean, link} {
		if err := security.ValidateOutputRoot(path); err == nil {
			t.Fatalf("ValidateOutputRoot(%q) succeeded", path)
		}
	}
	if err := security.ValidateOutputRoot(filepath.Join(t.TempDir(), "new-vault")); err != nil {
		t.Fatal(err)
	}
}
