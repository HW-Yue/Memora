package lexer

import (
	"errors"
	"reflect"
	"testing"
)

func TestLexWordsPreserveCaseAndUnicode(t *testing.T) {
	t.Parallel()

	tokens, err := Lex("SeLeCt 项目.笔记 FROM 工作库;")
	if err != nil {
		t.Fatalf("Lex() error = %v", err)
	}
	want := []tokenView{
		{KindWord, "SeLeCt", "SeLeCt"}, {KindWord, "项目", "项目"}, {KindDot, ".", ""},
		{KindWord, "笔记", "笔记"}, {KindWord, "FROM", "FROM"}, {KindWord, "工作库", "工作库"},
		{KindSemicolon, ";", ""}, {KindEOF, "", ""},
	}
	assertTokens(t, tokens, want)
	if !tokens[0].IsKeyword("SELECT") || tokens[1].IsKeyword("项目") {
		t.Fatalf("keyword classification failed: %#v", tokens[:2])
	}
}

func TestLexStringsQuotedIdentifiersAndParameters(t *testing.T) {
	t.Parallel()

	tokens, err := Lex("INSERT INTO `项目``表` (\"标题\", body) VALUES (:title, 'AI''s memory'), (?, 12.50)")
	if err != nil {
		t.Fatalf("Lex() error = %v", err)
	}
	want := []tokenView{
		{KindWord, "INSERT", "INSERT"}, {KindWord, "INTO", "INTO"}, {KindQuotedIdentifier, "`项目``表`", "项目`表"},
		{KindLeftParen, "(", ""}, {KindQuotedIdentifier, "\"标题\"", "标题"}, {KindComma, ",", ""}, {KindWord, "body", "body"},
		{KindRightParen, ")", ""}, {KindWord, "VALUES", "VALUES"}, {KindLeftParen, "(", ""},
		{KindParameter, ":title", "title"}, {KindComma, ",", ""}, {KindString, "'AI''s memory'", "AI's memory"},
		{KindRightParen, ")", ""}, {KindComma, ",", ""}, {KindLeftParen, "(", ""}, {KindParameter, "?", "?"},
		{KindComma, ",", ""}, {KindNumber, "12.50", "12.50"}, {KindRightParen, ")", ""}, {KindEOF, "", ""},
	}
	assertTokens(t, tokens, want)
}

func TestLexSkipsCommentsAndTracksUnicodeSpan(t *testing.T) {
	t.Parallel()

	tokens, err := Lex("-- first\nSELECT 项目; /* tail */")
	if err != nil {
		t.Fatalf("Lex() error = %v", err)
	}
	if got := tokens[0].Span.Start; got != (Position{Offset: 9, Line: 2, Column: 1}) {
		t.Fatalf("SELECT start = %#v", got)
	}
	if got := tokens[1].Span; got.Start.Column != 8 || got.End.Column != 10 || got.End.Offset-got.Start.Offset != 6 {
		t.Fatalf("Unicode span = %#v", got)
	}
	if tokens[len(tokens)-1].Kind != KindEOF || tokens[len(tokens)-1].Span.Start.Line != 2 {
		t.Fatalf("EOF token = %#v", tokens[len(tokens)-1])
	}
}

func TestLexOperators(t *testing.T) {
	t.Parallel()

	tokens, err := Lex("a = 1 AND b != 2 OR c <> 3 AND d <= 4 AND e >= 5 + 6 - 7 * 8 / 9 % 2")
	if err != nil {
		t.Fatalf("Lex() error = %v", err)
	}
	var operators []string
	for _, token := range tokens {
		if token.Kind == KindOperator || token.Kind == KindStar {
			operators = append(operators, token.Lexeme)
		}
	}
	want := []string{"=", "!=", "<>", "<=", ">=", "+", "-", "*", "/", "%"}
	if !reflect.DeepEqual(operators, want) {
		t.Fatalf("operators = %v, want %v", operators, want)
	}
}

func TestLexNumbersAndRejectsNonASCIINumericSyntax(t *testing.T) {
	t.Parallel()

	tokens, err := Lex("1 12.50 3e4 5E-2")
	if err != nil {
		t.Fatalf("Lex() error = %v", err)
	}
	want := []tokenView{
		{KindNumber, "1", "1"}, {KindNumber, "12.50", "12.50"},
		{KindNumber, "3e4", "3e4"}, {KindNumber, "5E-2", "5E-2"}, {KindEOF, "", ""},
	}
	assertTokens(t, tokens, want)

	_, err = Lex("SELECT ١")
	var lexError *Error
	if !errors.As(err, &lexError) || lexError.Code != ErrorUnexpectedCharacter || lexError.Span.Start.Column != 8 {
		t.Fatalf("Lex() non-ASCII number error = %#v", err)
	}
}

func TestLexReportsPreciseErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		source string
		code   ErrorCode
		line   int
		column int
	}{
		{"illegal character", "SELECT @bad", ErrorUnexpectedCharacter, 1, 8},
		{"unterminated string", "SELECT 'bad", ErrorUnterminatedString, 1, 8},
		{"unterminated comment", "SELECT /* bad", ErrorUnterminatedComment, 1, 8},
		{"unterminated identifier", "SELECT `bad", ErrorUnterminatedIdentifier, 1, 8},
		{"invalid parameter", "SELECT :1", ErrorInvalidParameter, 1, 8},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := Lex(test.source)
			var lexError *Error
			if !errors.As(err, &lexError) {
				t.Fatalf("Lex() error = %v, want lexer Error", err)
			}
			if lexError.Code != test.code || lexError.Span.Start.Line != test.line || lexError.Span.Start.Column != test.column {
				t.Fatalf("Lex() error = %#v", lexError)
			}
		})
	}
}

type tokenView struct {
	kind   Kind
	lexeme string
	value  string
}

func assertTokens(t *testing.T, tokens []Token, want []tokenView) {
	t.Helper()
	got := make([]tokenView, len(tokens))
	for index, token := range tokens {
		got[index] = tokenView{token.Kind, token.Lexeme, token.Value}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tokens =\n%#v\nwant =\n%#v", got, want)
	}
}
