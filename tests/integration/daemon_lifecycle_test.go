//go:build integration

package integration_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDaemonLifecycleCLI(t *testing.T) {
	root := repositoryRoot(t)
	temporary := t.TempDir()
	binary := filepath.Join(temporary, "memora")
	command(t, root, "go", "build", "-o", binary, "./cmd/memora")
	dataDir := filepath.Join(temporary, "instance")
	command(t, root, binary, "init", "--data-dir", dataDir)
	command(t, root, binary, "daemon", "start", "--data-dir", dataDir)
	defer func() {
		_, _ = runCommand(root, binary, "daemon", "stop", "--data-dir", dataDir)
	}()

	status := command(t, root, binary, "daemon", "status", "--data-dir", dataDir)
	if !strings.Contains(status, "running") {
		t.Fatalf("status = %q, want running", status)
	}
	ping := command(t, root, binary, "daemon", "ping", "--data-dir", dataDir)
	if !strings.Contains(ping, "pong") {
		t.Fatalf("ping = %q, want pong", ping)
	}
	version := command(t, root, binary, "version", "--json")
	if !strings.Contains(version, `"name":"memora"`) {
		t.Fatalf("version = %q", version)
	}
	assertGolden(t, "parse-success.golden", command(t, root, binary, "parse", "--data-dir", dataDir, "SHOW DATABASES; SELECT * FROM notes;"))
	parseFailure, err := runCommand(root, binary, "parse", "--data-dir", dataDir, "SELECT * notes")
	if err == nil {
		t.Fatalf("invalid parse succeeded: %s", parseFailure)
	}
	assertGolden(t, "parse-failure.golden", parseFailure)

	var wait sync.WaitGroup
	failures := make(chan string, 8)
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			output, err := runCommand(root, binary, "parse", "--data-dir", dataDir, "SELECT * FROM notes WHERE row_id = ?")
			if err != nil || !strings.Contains(output, `"ok":true`) {
				failures <- output
			}
		}()
	}
	wait.Wait()
	close(failures)
	for failure := range failures {
		t.Fatalf("concurrent parse failed: %s", failure)
	}
	if output, err := runCommand(root, binary, "daemon", "start", "--data-dir", dataDir); err == nil {
		t.Fatalf("second daemon start succeeded: %s", output)
	}
	command(t, root, binary, "daemon", "stop", "--data-dir", dataDir)
	status = command(t, root, binary, "daemon", "status", "--data-dir", dataDir)
	if !strings.Contains(status, "stopped") {
		t.Fatalf("status = %q, want stopped", status)
	}
	command(t, root, binary, "daemon", "start", "--data-dir", dataDir)
	assertGolden(t, "parse-success.golden", command(t, root, binary, "parse", "--data-dir", dataDir, "SHOW DATABASES; SELECT * FROM notes;"))
}

func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	want, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read golden %s: %v", name, err)
	}
	if got != string(want) {
		t.Fatalf("%s mismatch\n--- got ---\n%s--- want ---\n%s", name, got, want)
	}
}

func command(t *testing.T, directory, name string, args ...string) string {
	t.Helper()
	output, err := runCommand(directory, name, args...)
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, output)
	}
	return output
}

func runCommand(directory, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = directory
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test file")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
}
