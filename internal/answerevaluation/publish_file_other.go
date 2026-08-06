//go:build !darwin

package answerevaluation

import (
	"errors"
	"fmt"
	"os"
)

func publishFileExclusive(source, destination string) error {
	if err := os.Link(source, destination); err != nil {
		if errors.Is(err, os.ErrExist) {
			return ErrReportOutputExists
		}
		return fmt.Errorf("%w: publish report", ErrInvalidReportOutput)
	}
	return nil
}
