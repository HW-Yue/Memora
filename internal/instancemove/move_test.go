package instancemove_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/HW-Yue/Memora/internal/daemon"
	"github.com/HW-Yue/Memora/internal/instance"
	"github.com/HW-Yue/Memora/internal/instancemove"
	"github.com/HW-Yue/Memora/internal/testkit"
)

func TestMoveRunningInstancePreservesSourceAndStartsTarget(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := filepath.Join(root, "source")
	backup := filepath.Join(root, "portable-backup")
	target := filepath.Join(root, "other-device", "instance")
	createInstance(t, source)
	if err := os.WriteFile(filepath.Join(source, "payload"), []byte("durable"), 0o600); err != nil {
		t.Fatal(err)
	}

	var calls []string
	receipt, err := instancemove.Move(context.Background(), source, backup, target, instancemove.Options{
		Executable: "/opt/memora/bin/memora",
		Inspect: func(path string) (daemon.State, error) {
			calls = append(calls, "inspect:"+path)
			return daemon.State{Running: true, PID: 41}, nil
		},
		Stop: func(_ context.Context, path string) error {
			calls = append(calls, "stop:"+path)
			return nil
		},
		Start: func(_ context.Context, executable, path string) (daemon.State, error) {
			calls = append(calls, "start:"+executable+":"+path)
			return daemon.State{Running: true, PID: 42}, nil
		},
	})
	if err != nil {
		t.Fatalf("Move() error = %v", err)
	}
	if got, want := calls, []string{
		"inspect:" + source,
		"stop:" + source,
		"start:/opt/memora/bin/memora:" + target,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %#v, want %#v", got, want)
	}
	if !receipt.SourceRetained || !receipt.TargetDaemonRunning || receipt.BackupHash == "" {
		t.Fatalf("receipt = %#v", receipt)
	}
	if got, err := os.ReadFile(filepath.Join(source, "payload")); err != nil || string(got) != "durable" {
		t.Fatalf("source payload = %q, %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(target, "payload")); err != nil || string(got) != "durable" {
		t.Fatalf("target payload = %q, %v", got, err)
	}
}

func TestMoveRestartsSourceWhenTargetStartFails(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := filepath.Join(root, "source")
	backup := filepath.Join(root, "backup")
	target := filepath.Join(root, "target")
	createInstance(t, source)
	var starts []string
	_, err := instancemove.Move(context.Background(), source, backup, target, instancemove.Options{
		Executable: "/memora",
		Inspect:    func(string) (daemon.State, error) { return daemon.State{Running: true}, nil },
		Stop:       func(context.Context, string) error { return nil },
		Start: func(_ context.Context, _ string, path string) (daemon.State, error) {
			starts = append(starts, path)
			if path == target {
				return daemon.State{}, errors.New("target start fault")
			}
			return daemon.State{Running: true}, nil
		},
	})
	if err == nil || !reflect.DeepEqual(starts, []string{target, source}) {
		t.Fatalf("Move() error = %v, starts = %#v", err, starts)
	}
}

func TestMoveStoppedInstanceLeavesTargetStopped(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := filepath.Join(root, "source")
	backup := filepath.Join(root, "backup")
	target := filepath.Join(root, "target")
	createInstance(t, source)
	called := false
	receipt, err := instancemove.Move(context.Background(), source, backup, target, instancemove.Options{
		Inspect: func(string) (daemon.State, error) { return daemon.State{}, nil },
		Stop: func(context.Context, string) error {
			called = true
			return nil
		},
		Start: func(context.Context, string, string) (daemon.State, error) {
			called = true
			return daemon.State{}, nil
		},
	})
	if err != nil {
		t.Fatalf("Move() error = %v", err)
	}
	if called || receipt.TargetDaemonRunning {
		t.Fatalf("lifecycle called = %t, receipt = %#v", called, receipt)
	}
}

func TestMoveFailureAfterStopRestartsSource(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := filepath.Join(root, "source")
	backup := filepath.Join(root, "backup")
	target := filepath.Join(root, "target")
	createInstance(t, source)
	if err := os.Mkdir(backup, 0o700); err != nil {
		t.Fatal(err)
	}
	var starts []string
	_, err := instancemove.Move(context.Background(), source, backup, target, instancemove.Options{
		Executable: "/memora",
		Inspect:    func(string) (daemon.State, error) { return daemon.State{Running: true}, nil },
		Stop:       func(context.Context, string) error { return nil },
		Start: func(_ context.Context, _ string, path string) (daemon.State, error) {
			starts = append(starts, path)
			return daemon.State{Running: true}, nil
		},
	})
	if err == nil || !reflect.DeepEqual(starts, []string{source}) {
		t.Fatalf("Move() error = %v, starts = %#v", err, starts)
	}
}

func TestMoveRejectsOverlappingPathsBeforeLifecycle(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	called := false
	_, err := instancemove.Move(context.Background(), root, filepath.Join(root, "backup"), filepath.Join(root, "target"), instancemove.Options{
		Inspect: func(string) (daemon.State, error) {
			called = true
			return daemon.State{}, nil
		},
	})
	if err == nil || called {
		t.Fatalf("Move() error = %v, lifecycle called = %t", err, called)
	}
}

func createInstance(t *testing.T, path string) {
	t.Helper()
	_, err := instance.Initialize(context.Background(), path, instance.Options{
		IDs: testkit.NewFakeIDs("018f2f7e-7b5d-7c31-8a29-53f27d8f93c1"),
	})
	if err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
}
