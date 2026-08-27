//go:build darwin

package answerbenchmark

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// ExFAT on macOS supports file fsync but may reject fsync on a directory.
// File contents are still synced before publication; an unsupported directory
// sync must not make an otherwise atomic report unusable on an external disk.
func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	if err := directory.Sync(); err != nil {
		if errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EINVAL) {
			return nil
		}
		return err
	}
	return nil
}
