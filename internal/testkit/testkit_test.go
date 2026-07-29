package testkit

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSandboxResolve(t *testing.T) {
	t.Parallel()

	sandbox := NewSandbox(t)
	path, err := sandbox.Resolve("data/table.mdata")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !strings.HasPrefix(path, sandbox.Root+string(filepath.Separator)) {
		t.Fatalf("Resolve() path %q escaped root %q", path, sandbox.Root)
	}

	for _, unsafe := range []string{"../escape", "/tmp/escape"} {
		if _, err := sandbox.Resolve(unsafe); err == nil {
			t.Errorf("Resolve(%q) succeeded, want rejection", unsafe)
		}
	}
}

func TestSandboxRejectsRealUserHome(t *testing.T) {
	t.Parallel()

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir() error = %v", err)
	}
	sandbox := NewSandbox(t)
	if err := sandbox.Validate(home); err == nil {
		t.Fatalf("Validate(%q) succeeded outside sandbox %q", home, sandbox.Root)
	}
}

func TestSandboxRejectsSymlinkEscape(t *testing.T) {
	t.Parallel()

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir() error = %v", err)
	}
	sandbox := NewSandbox(t)
	link := filepath.Join(sandbox.Root, "outside")
	if err := os.Symlink(home, link); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	if err := sandbox.Validate(filepath.Join(link, "memora-test")); err == nil {
		t.Fatal("Validate() accepted a path escaping through a symlink")
	}
}

func TestFakeClock(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 7, 29, 9, 30, 0, 0, time.UTC)
	clock := NewFakeClock(start)
	if got := clock.Now(); !got.Equal(start) {
		t.Fatalf("Now() = %v, want %v", got, start)
	}

	clock.Advance(5 * time.Minute)
	if got, want := clock.Now(), start.Add(5*time.Minute); !got.Equal(want) {
		t.Fatalf("Now() after Advance = %v, want %v", got, want)
	}
}

func TestFakeIDs(t *testing.T) {
	t.Parallel()

	ids := NewFakeIDs("row_001", "row_002")
	for _, want := range []string{"row_001", "row_002"} {
		got, err := ids.Next()
		if err != nil {
			t.Fatalf("Next() error = %v", err)
		}
		if got != want {
			t.Fatalf("Next() = %q, want %q", got, want)
		}
	}
	if _, err := ids.Next(); !errors.Is(err, ErrIDsExhausted) {
		t.Fatalf("Next() exhausted error = %v, want %v", err, ErrIDsExhausted)
	}
}

func TestFaultsFailOnNthHit(t *testing.T) {
	t.Parallel()

	boom := errors.New("injected failure")
	faults := NewFaults()
	faults.FailOn("after-write", 2, boom)

	if err := faults.Hit("after-write"); err != nil {
		t.Fatalf("first Hit() error = %v", err)
	}
	if err := faults.Hit("after-write"); !errors.Is(err, boom) {
		t.Fatalf("second Hit() error = %v, want %v", err, boom)
	}
	if err := faults.Hit("after-write"); err != nil {
		t.Fatalf("third Hit() error = %v", err)
	}
	if got := faults.Hits("after-write"); got != 3 {
		t.Fatalf("Hits() = %d, want 3", got)
	}
}

func TestCompareGolden(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "output.golden")
	if err := CompareGolden(path, []byte("first\n"), true); err != nil {
		t.Fatalf("CompareGolden(update) error = %v", err)
	}
	if err := CompareGolden(path, []byte("first\n"), false); err != nil {
		t.Fatalf("CompareGolden(match) error = %v", err)
	}
	if err := CompareGolden(path, []byte("second\n"), false); err == nil {
		t.Fatal("CompareGolden(mismatch) succeeded, want error")
	}
}

func TestReadFixture(t *testing.T) {
	t.Parallel()

	content := ReadFixture(t, "sample.txt")
	if got, want := string(content), "fixture-content\n"; got != want {
		t.Fatalf("ReadFixture() = %q, want %q", got, want)
	}
}
