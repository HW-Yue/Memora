package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/instance"
)

// newInstance makes a real instance directory, so the test deletes something the
// product would actually recognize rather than an empty folder.
func newInstance(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "instance")
	if _, err := instance.Initialize(context.Background(), root, instance.Options{}); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestInstanceDestroyRemovesTheDirectoryItWasToldTo(t *testing.T) {
	root := newInstance(t)
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	if code := runInstance([]string{"destroy", "--yes", "--data-dir", root}, stdout, stderr); code != ExitOK {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("the directory is still there: %v", err)
	}
	if !strings.Contains(stdout.String(), "removed Memora instance") {
		t.Fatalf("the report must say what it removed: %q", stdout.String())
	}
}

// Deleting the wrong thing is the failure that matters, so every path that is not
// "an instance, named explicitly, approved" has to refuse — and refuse without
// touching the directory.
func TestInstanceDestroyRefusesAnythingItWasNotToldToDelete(t *testing.T) {
	real := newInstance(t)
	notAnInstance := filepath.Join(t.TempDir(), "documents")
	if err := os.MkdirAll(notAnInstance, 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		args []string
	}{
		{"no approval", []string{"destroy", "--data-dir", real}},
		{"no directory", []string{"destroy", "--yes"}},
		{"relative path", []string{"destroy", "--yes", "--data-dir", "relative/place"}},
		{"not an instance", []string{"destroy", "--yes", "--data-dir", notAnInstance}},
		{"unknown action", []string{"drop", "--yes", "--data-dir", real}},
	}
	for _, testCase := range cases {
		stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
		if code := runInstance(testCase.args, stdout, stderr); code == ExitOK {
			t.Fatalf("%s: must not succeed", testCase.name)
		}
	}
	// Nothing was removed on the way: not the instance, not an ordinary directory.
	if _, err := os.Stat(real); err != nil {
		t.Fatalf("a refused destroy removed the instance: %v", err)
	}
	if _, err := os.Stat(notAnInstance); err != nil {
		t.Fatalf("a refused destroy removed an ordinary directory: %v", err)
	}
}

// A fresh HOME has no instance, and the first command against one used to start a
// daemon for the empty directory and then wait out the whole start timeout:
// "context deadline exceeded" reads like a broken daemon, not like a missing
// instance. A write-path exercise on a throwaway HOME lost its whole session to
// this, so the refusal names the missing thing and the command that creates it.
func TestACommandAgainstAnEmptyHomeSaysThereIsNoInstance(t *testing.T) {
	home := t.TempDir()
	dataDir := filepath.Join(home, "Library", "Application Support", "Memora", "instances", "default")
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	started := false
	dependencies := Dependencies{
		HomeDir: func() (string, error) { return home, nil },
		Executable: func() (string, error) {
			started = true
			return "", os.ErrNotExist
		},
	}
	code := RunWithDependencies([]string{"query", "SHOW DATABASES LIMIT 8 COMPACT"}, stdout, stderr, BuildInfo{}, dependencies)
	if code == ExitOK {
		t.Fatalf("a query with no instance must fail: %s", stdout.String())
	}
	if started {
		t.Fatal("no daemon may be started for a directory that holds no instance")
	}
	message := stderr.String()
	if !strings.Contains(message, "no Memora instance at") || !strings.Contains(message, "memora init") {
		t.Fatalf("the refusal must name the missing instance and the fix: %q", message)
	}
	if !strings.Contains(message, dataDir) {
		t.Fatalf("the refusal must name the path it looked at: %q", message)
	}
}
