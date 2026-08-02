package lexical

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

var ErrInvalidText = errors.New("lexical text is invalid")

// Terms applies Memora's deterministic lexical tokenization. Repeated terms
// are preserved so callers can either count frequency or deduplicate them.
func Terms(value string) ([]string, error) {
	if !utf8.ValidString(value) {
		return nil, ErrInvalidText
	}
	result := make([]string, 0)
	word := make([]rune, 0)
	han := make([]rune, 0)
	flushWord := func() {
		if len(word) > 0 {
			result = append(result, strings.ToLower(string(word)))
			word = word[:0]
		}
	}
	flushHan := func() {
		switch len(han) {
		case 0:
		case 1:
			result = append(result, string(han))
		default:
			for index := 0; index+1 < len(han); index++ {
				result = append(result, string(han[index:index+2]))
			}
		}
		han = han[:0]
	}
	for _, character := range value {
		switch {
		case unicode.Is(unicode.Han, character):
			flushWord()
			han = append(han, character)
		case unicode.IsLetter(character) || unicode.IsDigit(character):
			flushHan()
			word = append(word, unicode.ToLower(character))
		default:
			flushWord()
			flushHan()
		}
	}
	flushWord()
	flushHan()
	return result, nil
}

// UniqueTerms preserves first occurrence order while removing duplicates.
func UniqueTerms(value string) ([]string, error) {
	values, err := Terms(value)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, term := range values {
		if _, exists := seen[term]; exists {
			continue
		}
		seen[term] = struct{}{}
		result = append(result, term)
	}
	return result, nil
}
