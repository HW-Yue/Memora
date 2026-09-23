package cli

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/daemon"
	"github.com/HW-Yue/Memora/internal/ipc"
)

// ensureDaemon used to look only at "is something running". Something running is
// not the question: a daemon from another build answers the socket perfectly and
// commits writes this client cannot read back.
func TestEnsureDaemonRefusesADaemonOfAnotherEngineProtocol(t *testing.T) {
	dataDir := t.TempDir()
	serveInstance(t, dataDir, ipc.EngineProtocol+1)

	err := ensureDaemon(context.Background(), dataDir, io.Discard, Dependencies{})
	var skewed *ipc.SkewedError
	if !errors.As(err, &skewed) {
		t.Fatalf("ensureDaemon() error = %v, want an engine protocol skew", err)
	}
	if skewed.Server != ipc.EngineProtocol+1 || skewed.Client != ipc.EngineProtocol {
		t.Fatalf("skew = %+v, want client %d and server %d", skewed, ipc.EngineProtocol, ipc.EngineProtocol+1)
	}
}

func TestEnsureDaemonAcceptsADaemonOfTheSameEngineProtocol(t *testing.T) {
	dataDir := t.TempDir()
	serveInstance(t, dataDir, ipc.EngineProtocol)

	if err := ensureDaemon(context.Background(), dataDir, io.Discard, Dependencies{}); err != nil {
		t.Fatalf("ensureDaemon() error = %v", err)
	}
}

// serveInstance runs one daemon for dataDir in this process, reporting the given
// engine protocol as its own.
func serveInstance(t *testing.T, dataDir string, engineProtocol int) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan daemon.State, 1)
	done := make(chan error, 1)
	go func() {
		done <- daemon.Run(ctx, dataDir, daemon.Identity{Version: "test", EngineProtocol: engineProtocol}, ready)
	}()
	select {
	case <-ready:
	case err := <-done:
		cancel()
		t.Fatalf("daemon exited before it was ready: %v", err)
	case <-time.After(20 * time.Second):
		cancel()
		t.Fatal("daemon did not become ready")
	}
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("daemon exited with %v", err)
			}
		case <-time.After(20 * time.Second):
			t.Error("daemon did not stop")
		}
	})
}
