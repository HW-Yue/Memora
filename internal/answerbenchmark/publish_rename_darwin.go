//go:build darwin

package answerbenchmark

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func renameDirectoryExclusive(source, destination string) error {
	err := unix.RenameatxNp(unix.AT_FDCWD, source, unix.AT_FDCWD, destination, unix.RENAME_EXCL)
	if errors.Is(err, unix.EEXIST) {
		return ErrOutputExists
	}
	// macOS does not expose renameatx_np(RENAME_EXCL) on some external
	// filesystems (notably ExFAT). The caller already created and fsynced the
	// complete staging directory and checked the destination; fall back to the
	// ordinary atomic rename without ever replacing an existing destination.
	if errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EINVAL) {
		if _, lookupErr := os.Lstat(destination); lookupErr == nil {
			return ErrOutputExists
		} else if !errors.Is(lookupErr, os.ErrNotExist) {
			return fmt.Errorf("%w: inspect destination: %v", ErrInvalidOutput, lookupErr)
		}
		if renameErr := os.Rename(source, destination); renameErr != nil {
			if _, lookupErr := os.Lstat(destination); lookupErr == nil {
				return ErrOutputExists
			}
			return fmt.Errorf("%w: publish report directory: %v", ErrInvalidOutput, renameErr)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: publish report directory: %v", ErrInvalidOutput, err)
	}
	return nil
}
