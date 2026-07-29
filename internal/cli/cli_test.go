package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/testkit"
)

func TestRun(t *testing.T) {
	t.Parallel()

	build := BuildInfo{
		Version: "0.1.0-test",
		Commit:  "abc1234",
		BuiltAt: "2026-07-29T00:00:00Z",
	}

	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{
			name:       "help",
			args:       []string{"help"},
			wantCode:   0,
			wantStdout: golden(t, "help.golden"),
		},
		{
			name:       "no arguments shows help",
			wantCode:   0,
			wantStdout: golden(t, "help.golden"),
		},
		{
			name:       "JSON version",
			args:       []string{"version", "--json"},
			wantCode:   0,
			wantStdout: golden(t, "version-json.golden"),
		},
		{
			name:       "text version",
			args:       []string{"version"},
			wantCode:   0,
			wantStdout: "memora 0.1.0-test (abc1234)\n",
		},
		{
			name:       "unknown command",
			args:       []string{"wat"},
			wantCode:   2,
			wantStderr: golden(t, "unknown-command.golden"),
		},
		{
			name:       "invalid version option",
			args:       []string{"version", "--yaml"},
			wantCode:   2,
			wantStderr: "memora: unknown option for version: \"--yaml\"\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var stdout bytes.Buffer
			var stderr bytes.Buffer

			gotCode := Run(tt.args, &stdout, &stderr, build)
			if gotCode != tt.wantCode {
				t.Fatalf("Run() code = %d, want %d", gotCode, tt.wantCode)
			}
			if got := stdout.String(); got != tt.wantStdout {
				t.Errorf("stdout mismatch\n--- got ---\n%s--- want ---\n%s", got, tt.wantStdout)
			}
			if got := stderr.String(); got != tt.wantStderr {
				t.Errorf("stderr mismatch\n--- got ---\n%s--- want ---\n%s", got, tt.wantStderr)
			}
		})
	}
}

func TestRunInit(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	dataDir := filepath.Join(home, "custom-instance")
	createdAt := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	dependencies := Dependencies{
		HomeDir: func() (string, error) { return home, nil },
		Clock:   testkit.NewFakeClock(createdAt),
		IDs:     testkit.NewFakeIDs("018f2f7e-7b5d-7c31-8a29-53f27d8f93c1"),
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := RunWithDependencies(
		[]string{"init", "--data-dir", dataDir},
		&stdout,
		&stderr,
		BuildInfo{},
		dependencies,
	)
	if code != ExitOK {
		t.Fatalf("first init code = %d, stderr = %s", code, &stderr)
	}
	want := fmt.Sprintf("Initialized Memora instance 018f2f7e-7b5d-7c31-8a29-53f27d8f93c1 at %s\n", dataDir)
	if got := stdout.String(); got != want {
		t.Fatalf("first init stdout = %q, want %q", got, want)
	}

	stdout.Reset()
	stderr.Reset()
	code = RunWithDependencies(
		[]string{"init", "--data-dir", dataDir},
		&stdout,
		&stderr,
		BuildInfo{},
		dependencies,
	)
	if code != ExitOK {
		t.Fatalf("second init code = %d, stderr = %s", code, &stderr)
	}
	want = fmt.Sprintf("Memora instance 018f2f7e-7b5d-7c31-8a29-53f27d8f93c1 already initialized at %s\n", dataDir)
	if got := stdout.String(); got != want {
		t.Fatalf("second init stdout = %q, want %q", got, want)
	}
}

func TestRunInitRejectsRelativeDataDir(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := RunWithDependencies(
		[]string{"init", "--data-dir", "relative/path"},
		&stdout,
		&stderr,
		BuildInfo{},
		Dependencies{HomeDir: os.UserHomeDir},
	)
	if code != ExitUsage {
		t.Fatalf("init code = %d, want %d", code, ExitUsage)
	}
	if got, want := stderr.String(), "memora: --data-dir must be an absolute path\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}

func golden(t *testing.T, name string) string {
	t.Helper()

	content, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read golden file %q: %v", name, err)
	}
	return string(content)
}
