package mechanicalindex

import (
	"strings"
	"unicode"
)

func tokenize(fields []string, maxTerms, maxInputCharacters int) ([]string, bool) {
	seen := make(map[string]struct{}, maxTerms)
	terms := make([]string, 0, maxTerms)
	truncated := false
	consumed := 0
	add := func(term string) {
		if term == "" {
			return
		}
		if _, exists := seen[term]; exists {
			return
		}
		if len(terms) == maxTerms {
			truncated = true
			return
		}
		seen[term] = struct{}{}
		terms = append(terms, term)
	}
	for fieldIndex, field := range fields {
		var latin []rune
		var han []rune
		flushLatin := func() {
			if len(latin) == 0 {
				return
			}
			word := strings.ToLower(string(latin))
			add(word)
			if len(latin) >= 3 {
				lower := []rune(word)
				for index := 0; index+3 <= len(lower); index++ {
					add(string(lower[index : index+3]))
				}
			}
			latin = latin[:0]
		}
		flushHan := func() {
			if len(han) == 1 {
				add(string(han))
			} else {
				for index := 0; index+2 <= len(han); index++ {
					add(string(han[index : index+2]))
				}
			}
			han = han[:0]
		}
		runes := []rune(field)
		remaining := maxInputCharacters - consumed
		if len(runes) > remaining {
			runes = runes[:remaining]
			truncated = true
		}
		for _, value := range runes {
			if consumed == maxInputCharacters {
				truncated = true
				break
			}
			consumed++
			switch {
			case unicode.Is(unicode.Han, value):
				flushLatin()
				han = append(han, value)
			case unicode.IsLetter(value) || unicode.IsDigit(value):
				flushHan()
				latin = append(latin, value)
			default:
				flushLatin()
				flushHan()
			}
		}
		flushLatin()
		flushHan()
		if consumed == maxInputCharacters {
			for _, remainingField := range fields[fieldIndex+1:] {
				if remainingField != "" {
					truncated = true
					break
				}
			}
			break
		}
	}
	return terms, truncated
}
