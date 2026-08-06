//go:build darwin

package answerevaluation

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func publishFileExclusive(source, destination string) error {
	err := unix.RenameatxNp(unix.AT_FDCWD, source, unix.AT_FDCWD, destination, unix.RENAME_EXCL)
	if errors.Is(err, unix.EEXIST) {
		return ErrReportOutputExists
	}
	// macOS ExFAT does not expose renameatx_np(RENAME_EXCL). The caller has
	// already synced the complete staging file and checked the destination.
	// Recheck before the ordinary atomic rename so the single-process report
	// publisher never replaces an existing receipt.
	if errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EINVAL) {
		if _, lookupErr := os.Lstat(destination); lookupErr == nil {
			return ErrReportOutputExists
		} else if !errors.Is(lookupErr, os.ErrNotExist) {
			return fmt.Errorf("%w: inspect destination", ErrInvalidReportOutput)
		}
		if renameErr := os.Rename(source, destination); renameErr != nil {
			if _, lookupErr := os.Lstat(destination); lookupErr == nil {
				return ErrReportOutputExists
			}
			return fmt.Errorf("%w: publish report", ErrInvalidReportOutput)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: publish report", ErrInvalidReportOutput)
	}
	return nil
}
