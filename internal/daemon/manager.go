package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

var ErrNotRunning = errors.New("daemon is not running")

const lifecycleTimeout = 5 * time.Second

func Start(ctx context.Context, executable, dataDir string) (State, error) {
	state, err := Inspect(dataDir)
	if err != nil {
		return State{}, err
	}
	if state.Running {
		return state, ErrAlreadyRunning
	}
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return State{}, fmt.Errorf("open null device: %w", err)
	}
	defer func() { _ = null.Close() }()
	command := exec.Command(executable, "daemon", "run", "--data-dir", dataDir)
	command.Stdin = null
	command.Stdout = null
	command.Stderr = null
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		return State{}, fmt.Errorf("start daemon process: %w", err)
	}
	_ = command.Process.Release()
	return waitForState(ctx, dataDir, true)
}

func Stop(ctx context.Context, dataDir string) error {
	state, err := Inspect(dataDir)
	if err != nil {
		return err
	}
	if !state.Running {
		return ErrNotRunning
	}
	process, err := os.FindProcess(state.PID)
	if err != nil {
		return fmt.Errorf("find daemon process: %w", err)
	}
	if err := process.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("signal daemon process: %w", err)
	}
	_, err = waitForState(ctx, dataDir, false)
	return err
}

func waitForState(parent context.Context, dataDir string, running bool) (State, error) {
	ctx, cancel := context.WithTimeout(parent, lifecycleTimeout)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		state, err := Inspect(dataDir)
		if err == nil && state.Running == running {
			if !running || Ping(ctx, dataDir) == nil {
				return state, nil
			}
		}
		select {
		case <-ctx.Done():
			return State{}, fmt.Errorf("wait for daemon running=%t: %w", running, ctx.Err())
		case <-ticker.C:
		}
	}
}
