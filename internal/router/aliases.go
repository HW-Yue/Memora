package router

import (
	"strings"
	"unicode/utf8"

	"github.com/HW-Yue/Memora/internal/result"
)

const (
	MaxAliases        = 8
	MaxAliasRunes     = 64
	MaxAliasUTF8Bytes = 512
)

// NormalizeAliases validates the bounded semantic alias surface and preserves
// caller order. The returned slice is always non-nil and owns its storage.
func NormalizeAliases(name string, aliases []string) ([]string, error) {
	return normalizeAliases(name, aliases, result.CodeValidation)
}

// AliasesAfterRename preserves old names as aliases without allowing rename
// history to make Route reads unbounded. A name that becomes current is no
// longer retained as its own alias.
func AliasesAfterRename(aliases []string, oldName, newName string) ([]string, error) {
	updated := make([]string, 0, len(aliases)+1)
	for _, alias := range aliases {
		if strings.EqualFold(strings.TrimSpace(alias), strings.TrimSpace(newName)) {
			continue
		}
		updated = append(updated, alias)
	}
	if !containsAlias(updated, oldName) {
		updated = append(updated, oldName)
	}
	return normalizeAliases(newName, updated, result.CodeConstraint)
}

func normalizeAliases(name string, aliases []string, code result.Code) ([]string, error) {
	if len(aliases) > MaxAliases {
		return nil, routerError(code, "Router aliases must contain at most 8 values")
	}
	name = strings.TrimSpace(name)
	resultAliases := make([]string, 0, len(aliases))
	totalBytes := 0
	for _, alias := range aliases {
		if !utf8.ValidString(alias) {
			return nil, routerError(code, "Router alias must be valid UTF-8")
		}
		alias = strings.TrimSpace(alias)
		if alias == "" || utf8.RuneCountInString(alias) > MaxAliasRunes {
			return nil, routerError(code, "Router alias must contain 1–64 characters")
		}
		if strings.EqualFold(alias, name) {
			return nil, routerError(code, "Router alias must differ from the current name")
		}
		if containsAlias(resultAliases, alias) {
			return nil, routerError(code, "Router aliases must be unique")
		}
		totalBytes += len(alias)
		if totalBytes > MaxAliasUTF8Bytes {
			return nil, routerError(code, "Router aliases must contain at most 512 UTF-8 bytes")
		}
		resultAliases = append(resultAliases, alias)
	}
	return resultAliases, nil
}

func containsAlias(aliases []string, candidate string) bool {
	for _, alias := range aliases {
		if strings.EqualFold(strings.TrimSpace(alias), strings.TrimSpace(candidate)) {
			return true
		}
	}
	return false
}
