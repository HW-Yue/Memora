//go:build darwin

package answerbenchmark

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

func renameDirectoryExclusive(source, destination string) error {
	err := unix.RenameatxNp(unix.AT_FDCWD, source, unix.AT_FDCWD, destination, unix.RENAME_EXCL)
	if errors.Is(err, unix.EEXIST) {
		return ErrOutputExists
	}
	if err != nil {
		return fmt.Errorf("%w: publish report directory: %v", ErrInvalidOutput, err)
	}
	return nil
}
