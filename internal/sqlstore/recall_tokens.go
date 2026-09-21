package sqlstore

import (
	"strings"
	"unicode"
)

// Chinese is written without spaces, so a tokenizer that splits on separators
// sees a whole sentence as one token, while FTS5's trigram tokenizer needs three
// characters at a time and cannot answer the two-character words Chinese is
// actually made of. The rule here is dictionary-free and deliberately dumb: every
// run of letters and digits contributes the overlapping pairs of its characters,
// and a run of one character contributes itself.
//
// The pairs are what makes 实习 findable inside 后端开发实习, and 实习经历 — three
// pairs — is matched as the pair sequence it is, so a Row that only says 实习
// shares a token with it instead of being excluded by an exact-string test.
// What the pairs cost is precision: a Row carrying the same pairs from different
// positions is a candidate too. BM25 counts how many of the query's tokens a Row
// actually carries, so such a Row ranks below one that carries more of it, and
// the answer stays ordered rather than merely filtered.
//
// The index and the query call this one function. Two tokenizers that drift
// apart — even by a rule as small as which characters count — make recall look
// broken in a way no test of either side alone can see.
func recallTokens(folded string) []string {
	tokens := []string{}
	run := make([]rune, 0, 32)
	flush := func() {
		switch {
		case len(run) == 1:
			tokens = append(tokens, string(run[0]))
		case len(run) > 1:
			for index := 0; index+1 < len(run); index++ {
				tokens = append(tokens, string(run[index:index+2]))
			}
		}
		run = run[:0]
	}
	for _, character := range folded {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			run = append(run, character)
			continue
		}
		flush()
	}
	flush()
	return tokens
}

// recallIndexText is the token stream a payload is indexed as. It is what the
// FTS5 table holds, so it carries no case or width of its own: every token is
// already the folded form, and FTS5's own folding has nothing left to do.
func recallIndexText(folded string) string {
	return strings.Join(recallTokens(folded), " ")
}

// recallMatchQuery turns a query into an FTS5 MATCH expression over that stream.
//
// The tokens are the query's own, so the expression asks for a Row that carries
// any of them and lets BM25 rank coverage: requiring all of them would turn a
// phrase back into the exact-string test this index exists to avoid. Each token
// is quoted because the query is content, not FTS5 syntax — an ordinary word like
// `or` or `near` is an operator when it is pasted in bare.
func recallMatchQuery(folded string) string {
	tokens := recallTokens(folded)
	quoted := make([]string, 0, len(tokens))
	for _, token := range tokens {
		quoted = append(quoted, `"`+token+`"`)
	}
	return strings.Join(quoted, " OR ")
}
