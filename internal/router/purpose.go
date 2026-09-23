package router

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/HW-Yue/Memora/internal/result"
)

// Every Route carries a `purpose`, and what is required of it is a description:
// a sentence saying what is kept here. A purpose that only repeats the name
// satisfies "not empty" and says nothing — and the retrieval path that reads
// those two fields is then choosing between bare labels, which is measurably
// worse retrieval, not a matter of taste. See
// docs/planning/route-purpose-contract.md and docs/decisions.md
// 「语义树的标签质量是可测量的检索损伤」.
//
// The comparison is folded rather than literal, because a rule compared raw is
// a rule answered with one extra space.

// FoldLabel is the comparable form of a Route label: padding gone, inner runs
// of whitespace collapsed, full-width forms and the ideographic space brought
// down to their half-width equivalents, and case folded away.
//
// The fold is written out here rather than taken from a Unicode normalization
// library: what it has to cover is the ways the same word gets typed — width,
// case, padding — and those are exactly the ways an agent reproduces a name in
// the purpose's place.
func FoldLabel(text string) string {
	folded := strings.Map(func(value rune) rune {
		switch {
		case value == '　': // ideographic space
			return ' '
		case value >= '！' && value <= '～': // full-width ASCII
			return value - 0xfee0
		case unicode.IsSpace(value):
			return ' '
		}
		return value
	}, text)
	return strings.ToLower(strings.Join(strings.Fields(folded), " "))
}

// PurposeRepeatsName reports whether this Route carries no description at all:
// either nothing was written, or what was written is the name again. The two
// are the same absence, and a path that could not tell them apart is how the
// tree rotted to 79% name-repeats without anything reporting it.
func PurposeRepeatsName(name, purpose string) bool {
	folded := FoldLabel(purpose)
	return folded == "" || folded == FoldLabel(name)
}

// CheckPurpose refuses a **new** Route whose purpose only repeats its name. An
// existing Route is never refused by this — see the notice on the mutation
// surfaces — because a library that already holds such Routes has to stay
// writable while its purposes are being filled in.
func CheckPurpose(name, purpose string) error {
	if !PurposeRepeatsName(name, purpose) {
		return nil
	}
	return routerError(result.CodeValidation, fmt.Sprintf(
		"Route %q needs a purpose that says what is kept here, not its own name again: a purpose "+
			"equal to the name (ignoring case, width and padding) is the description missing, and "+
			"the semantic tree is read through it", name))
}
