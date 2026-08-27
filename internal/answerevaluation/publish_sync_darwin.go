//go:build darwin

package answerevaluation

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

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
