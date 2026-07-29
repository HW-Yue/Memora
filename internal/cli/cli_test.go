package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
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

func golden(t *testing.T, name string) string {
	t.Helper()

	content, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read golden file %q: %v", name, err)
	}
	return string(content)
}
