package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAcquireEnforcesSingleDaemon(t *testing.T) {
	t.Parallel()

	dataDir := filepath.Join(t.TempDir(), "instance")
	first, err := Acquire(dataDir)
	if err != nil {
		t.Fatalf("first Acquire() error = %v", err)
	}
	defer func() { _ = first.Close() }()

	if _, err := Acquire(dataDir); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Acquire() error = %v, want %v", err, ErrAlreadyRunning)
	}
	state, err := Inspect(dataDir)
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if !state.Running || state.PID != os.Getpid() {
		t.Fatalf("Inspect() = %#v, want running PID %d", state, os.Getpid())
	}
}

func TestAcquireMaintenanceExcludesDaemonWithoutPublishingPID(t *testing.T) {
	t.Parallel()

	dataDir := filepath.Join(t.TempDir(), "instance")
	lease, err := AcquireMaintenance(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Close() }()
	if _, err := Acquire(dataDir); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("Acquire() during maintenance = %v", err)
	}
	paths, err := RuntimePaths(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(paths.PIDFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("maintenance lease published a PID file: %v", err)
	}
}

func TestInspectCleansStalePID(t *testing.T) {
	t.Parallel()

	dataDir := filepath.Join(t.TempDir(), "instance")
	paths, err := RuntimePaths(dataDir)
	if err != nil {
		t.Fatalf("RuntimePaths() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.PIDFile), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(paths.PIDFile, []byte("999999\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	state, err := Inspect(dataDir)
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if state.Running {
		t.Fatalf("Inspect() = %#v, want stopped", state)
	}
	if _, err := os.Stat(paths.PIDFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale PID file remains: %v", err)
	}
}

func TestRunReleasesLeaseAfterCancellation(t *testing.T) {
	t.Parallel()

	dataDir := filepath.Join(t.TempDir(), "instance")
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan State, 1)
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, dataDir, ready)
	}()
	state := <-ready
	if !state.Running {
		t.Fatalf("ready state = %#v, want running", state)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	lease, err := Acquire(dataDir)
	if err != nil {
		t.Fatalf("Acquire() after cancellation error = %v", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestInspectRejectsCorruptPIDWhileLocked(t *testing.T) {
	t.Parallel()

	dataDir := filepath.Join(t.TempDir(), "instance")
	lease, err := Acquire(dataDir)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	defer func() { _ = lease.Close() }()
	if err := os.WriteFile(lease.paths.PIDFile, []byte("not-a-pid\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := Inspect(dataDir); !errors.Is(err, ErrCorruptPID) {
		t.Fatalf("Inspect() error = %v, want %v", err, ErrCorruptPID)
	}
}
