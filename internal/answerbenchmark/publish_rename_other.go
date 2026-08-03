//go:build !darwin

package answerbenchmark

import (
	"errors"
	"fmt"
	"os"
)

func renameDirectoryExclusive(source, destination string) error {
	if _, err := os.Lstat(destination); err == nil {
		return ErrOutputExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: inspect destination: %v", ErrInvalidOutput, err)
	}
	if err := os.Rename(source, destination); err != nil {
		if _, lookupErr := os.Lstat(destination); lookupErr == nil {
			return ErrOutputExists
		}
		return fmt.Errorf("%w: publish report directory: %v", ErrInvalidOutput, err)
	}
	return nil
}
