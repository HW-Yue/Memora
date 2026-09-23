package cli

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/daemon"
	"github.com/HW-Yue/Memora/internal/ipc"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
)

// A CLI round trip carries a deadline of its own. Without one the client sends
// no TimeoutMS and the daemon runs the request under none either, so a CLI that
// meets a busy instance waits for a signal rather than for an answer.
func TestACLIRoundTripCarriesADeadline(t *testing.T) {
	dataDir := t.TempDir()
	runDaemonForTest(t, dataDir)

	deadline := time.Time{}
	dependencies := Dependencies{
		ExecuteMSQL: func(
			ctx context.Context, _, _ string, _ []executor.StatementInput, _ bool,
		) (result.Envelope, error) {
			deadline, _ = ctx.Deadline()
			return result.NewEnvelope("deadline-test"), nil
		},
	}
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	code := RunWithDependencies(
		[]string{"query", "--data-dir", dataDir, "SHOW DATABASES LIMIT 8 COMPACT"},
		stdout, stderr, BuildInfo{}, dependencies,
	)
	if code != ExitOK {
		t.Fatalf("query exited %d: %s", code, stderr.String())
	}
	if deadline.IsZero() {
		t.Fatal("the daemon round trip carried no deadline")
	}
	if remaining := time.Until(deadline); remaining <= 0 || remaining > daemonRequestTimeout {
		t.Fatalf("deadline is %v away, want within %v", remaining, daemonRequestTimeout)
	}
}

// runDaemonForTest serves one temporary instance in this process, so that the
// commands under test find something running for that directory.
func runDaemonForTest(t *testing.T, dataDir string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan daemon.State, 1)
	done := make(chan error, 1)
	go func() {
		done <- daemon.Run(ctx, dataDir, daemon.Identity{Version: "test", EngineProtocol: ipc.EngineProtocol}, ready)
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
