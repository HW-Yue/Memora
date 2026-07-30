package security

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/HW-Yue/Memora/internal/result"
)

func ValidateOutputRoot(root string) error {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return securityError(result.CodeValidation, "output root must be absolute and normalized")
	}
	return validateRoot(root)
}

func SecureJoin(root, relative string) (string, error) {
	if err := ValidateOutputRoot(root); err != nil {
		return "", err
	}
	if relative == "" || filepath.IsAbs(relative) || filepath.Clean(relative) != relative ||
		relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", securityError(result.CodeValidation, "output path escapes its authorized root")
	}
	candidate := filepath.Join(root, relative)
	contained, err := filepath.Rel(root, candidate)
	if err != nil || contained == ".." || strings.HasPrefix(contained, ".."+string(filepath.Separator)) {
		return "", securityError(result.CodeValidation, "output path escapes its authorized root")
	}
	if err := validateRelativePath(root, contained); err != nil {
		return "", err
	}
	return candidate, nil
}

func validateRoot(root string) error {
	current := root
	for {
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			parent := filepath.Dir(current)
			if parent == current {
				return nil
			}
			current = parent
			continue
		}
		if err != nil {
			return securityError(result.CodeInternal, "output path could not be inspected")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return securityError(result.CodePermissionDenied, "output path contains a symbolic link")
		}
		if !info.IsDir() {
			return securityError(result.CodeValidation, "output path parent is not a directory")
		}
		return nil
	}
}

func validateRelativePath(root, relative string) error {
	current := root
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return securityError(result.CodeInternal, "output path could not be inspected")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return securityError(result.CodePermissionDenied, "output path contains a symbolic link")
		}
		if !info.IsDir() && current != filepath.Join(root, relative) {
			return securityError(result.CodeValidation, "output path parent is not a directory")
		}
	}
	return nil
}
