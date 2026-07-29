package lexer

import "testing"

func FuzzLexNeverPanicsAndReturnsOrderedSpans(f *testing.F) {
	seeds := []string{
		"SELECT * FROM notes;",
		"SELECT '未完成",
		"/* comment */ SHOW DATABASES;",
		"INSERT INTO t VALUES (:名字, ?);",
		"\x00\xff",
	}
	for _, seed := range seeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, source string) {
		tokens, err := Lex(source)
		if err != nil {
			return
		}
		previous := 0
		for _, token := range tokens {
			if token.Span.Start.Offset < previous || token.Span.End.Offset < token.Span.Start.Offset || token.Span.End.Offset > len(source) {
				t.Fatalf("invalid token span %#v for %d-byte input", token, len(source))
			}
			previous = token.Span.End.Offset
		}
	})
}
