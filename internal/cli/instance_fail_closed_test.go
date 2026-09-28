package cli

import (
	"bytes"
	"os"
	"testing"

	"github.com/HW-Yue/Memora/internal/daemon"
)

// "Is anything writing to this instance?" is the question that decides whether a
// directory may be removed, and "I could not tell" is not "nothing is running".
// A daemon holds the instance lock for as long as it lives; a held lock whose PID
// cannot be read used to fall through to RemoveAll — the one mistake that cannot
// be undone, taken on the strength of a question that was never answered.
func TestInstanceDestroyRefusesWhenItCannotTellWhetherTheDaemonIsRunning(t *testing.T) {
	root := newInstance(t)
	// Something really is holding the instance: this is the daemon's own lease,
	// taken the way the daemon takes it. Reading its PID is what fails next.
	lease, err := daemon.Acquire(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Close() }()
	paths, err := daemon.RuntimePaths(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(paths.PIDFile); err != nil {
		t.Fatal(err)
	}

	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	if code := runInstance([]string{"destroy", "--yes", "--data-dir", root}, stdout, stderr); code == ExitOK {
		t.Fatalf("destroy must refuse when it cannot tell: %s", stdout.String())
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("the directory must still be there: %v", err)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("inspect")) {
		t.Fatalf("the refusal must say what it could not establish: %q", stderr.String())
	}
}

// Every Inspect failure has to refuse, not just the one shape: a runtime
// directory that cannot even be opened is the same unanswered question, and the
// question is the only thing standing between a typo and an irreversible delete.
func TestInstanceDestroyRefusesWhenInspectionCannotRunAtAll(t *testing.T) {
	root := newInstance(t)
	paths, err := daemon.RuntimePaths(root)
	if err != nil {
		t.Fatal(err)
	}
	// A directory where the daemon's lock file belongs: the lock cannot be opened
	// at all, so Inspect cannot answer. The instance itself stays readable — only
	// the daemon's own runtime object is unusable — so the refusal under test is
	// the one about the daemon, not an earlier one about the metadata.
	if err := os.MkdirAll(paths.LockFile, 0o700); err != nil {
		t.Fatal(err)
	}

	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	if code := runInstance([]string{"destroy", "--yes", "--data-dir", root}, stdout, stderr); code == ExitOK {
		t.Fatalf("destroy must refuse when the daemon cannot be inspected: %s", stdout.String())
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("the directory must still be there: %v", err)
	}
}
