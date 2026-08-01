package instancemove

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/HW-Yue/Memora/internal/daemon"
	"github.com/HW-Yue/Memora/internal/instancebackup"
)

const ReceiptVersion = "memora.instance-move-receipt/v1"

type Options struct {
	Executable string
	Inspect    func(string) (daemon.State, error)
	Stop       func(context.Context, string) error
	Start      func(context.Context, string, string) (daemon.State, error)
	Backup     instancebackup.Options
}

type Receipt struct {
	Version             string `json:"version"`
	Status              string `json:"status"`
	Source              string `json:"source"`
	Backup              string `json:"backup"`
	Target              string `json:"target"`
	InstanceID          string `json:"instance_id"`
	BackupHash          string `json:"backup_hash"`
	SourceRetained      bool   `json:"source_retained"`
	TargetDaemonRunning bool   `json:"target_daemon_running"`
}

func Move(ctx context.Context, source, backup, target string, options Options) (Receipt, error) {
	if err := validatePaths(source, backup, target); err != nil {
		return Receipt{}, err
	}
	inspect := options.Inspect
	if inspect == nil {
		inspect = daemon.Inspect
	}
	stop := options.Stop
	if stop == nil {
		stop = daemon.Stop
	}
	start := options.Start
	if start == nil {
		start = daemon.Start
	}

	before, err := inspect(source)
	if err != nil {
		return Receipt{}, fmt.Errorf("inspect source daemon: %w", err)
	}
	if before.Running && options.Executable == "" {
		return Receipt{}, errors.New("daemon executable is required to preserve running state")
	}
	stopped := false
	if before.Running {
		if err := stop(ctx, source); err != nil {
			return Receipt{}, fmt.Errorf("stop source daemon: %w", err)
		}
		stopped = true
	}
	restartSource := func(moveErr error) error {
		if !stopped {
			return moveErr
		}
		_, restartErr := start(context.WithoutCancel(ctx), options.Executable, source)
		if restartErr == nil {
			return moveErr
		}
		return errors.Join(moveErr, fmt.Errorf("restart source daemon: %w", restartErr))
	}

	backupReceipt, err := instancebackup.Create(ctx, source, backup, options.Backup)
	if err != nil {
		return Receipt{}, restartSource(fmt.Errorf("create portable backup: %w", err))
	}
	if _, err := instancebackup.Restore(ctx, backup, target, options.Backup); err != nil {
		return Receipt{}, restartSource(fmt.Errorf("restore target Instance: %w", err))
	}
	targetRunning := false
	if before.Running {
		if _, err := start(ctx, options.Executable, target); err != nil {
			return Receipt{}, restartSource(fmt.Errorf("start target daemon: %w", err))
		}
		targetRunning = true
	}
	return Receipt{
		Version:             ReceiptVersion,
		Status:              "moved",
		Source:              source,
		Backup:              backup,
		Target:              target,
		InstanceID:          backupReceipt.InstanceID,
		BackupHash:          backupReceipt.Hash,
		SourceRetained:      true,
		TargetDaemonRunning: targetRunning,
	}, nil
}

func validatePaths(paths ...string) error {
	for _, path := range paths {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return errors.New("move paths must be absolute and normalized")
		}
	}
	for left := 0; left < len(paths); left++ {
		for right := left + 1; right < len(paths); right++ {
			if nested(paths[left], paths[right]) || nested(paths[right], paths[left]) {
				return errors.New("move paths must be distinct and disjoint")
			}
		}
	}
	return nil
}

func nested(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && (relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))))
}
