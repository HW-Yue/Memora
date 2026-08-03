package router_test

import (
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/router"
)

func TestNormalizeAliasesPreservesBoundedTrimmedOrder(t *testing.T) {
	t.Parallel()
	aliases, err := router.NormalizeAliases("runtime", []string{" agent loop ", "运行时"})
	if err != nil || len(aliases) != 2 || aliases[0] != "agent loop" || aliases[1] != "运行时" {
		t.Fatalf("NormalizeAliases() = %#v, %v", aliases, err)
	}
	aliases, err = router.NormalizeAliases("runtime", nil)
	if err != nil || aliases == nil || len(aliases) != 0 {
		t.Fatalf("NormalizeAliases(nil) = %#v, %v", aliases, err)
	}
}

func TestNormalizeAliasesRejectsInvalidOrUnboundedSurface(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		aliases []string
	}{
		{name: "blank", aliases: []string{" "}},
		{name: "same-as-name", aliases: []string{"RUNTIME"}},
		{name: "duplicate", aliases: []string{"Agent", "agent"}},
		{name: "too-many", aliases: []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}},
		{name: "too-long", aliases: []string{strings.Repeat("a", router.MaxAliasRunes+1)}},
		{name: "too-many-bytes", aliases: []string{
			strings.Repeat("甲", 60) + "1", strings.Repeat("乙", 60) + "2", strings.Repeat("丙", 60) + "3",
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := router.NormalizeAliases("runtime", test.aliases)
			assertCode(t, err, result.CodeValidation)
		})
	}
}

func TestAliasesAfterRenameRemovesCurrentNameAndCapsHistory(t *testing.T) {
	t.Parallel()
	aliases, err := router.AliasesAfterRename([]string{"legacy", "current"}, "runtime", "legacy")
	if err != nil || len(aliases) != 2 || aliases[0] != "current" || aliases[1] != "runtime" {
		t.Fatalf("AliasesAfterRename() = %#v, %v", aliases, err)
	}
	_, err = router.AliasesAfterRename(
		[]string{"a", "b", "c", "d", "e", "f", "g", "h"}, "runtime", "next",
	)
	assertCode(t, err, result.CodeConstraint)
}
