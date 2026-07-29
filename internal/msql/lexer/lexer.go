package lexer

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

type scanner struct {
	source string
	offset int
	line   int
	column int
}

func Lex(source string) ([]Token, error) {
	scanner := scanner{source: source, line: 1, column: 1}
	tokens := make([]Token, 0, len(source)/4+1)
	for {
		if err := scanner.skipIgnored(); err != nil {
			return nil, err
		}
		if scanner.atEnd() {
			position := scanner.position()
			return append(tokens, Token{Kind: KindEOF, Span: Span{Start: position, End: position}}), nil
		}

		start := scanner.position()
		r, size := scanner.peekRune()
		if r == utf8.RuneError && size == 1 {
			scanner.advanceRune()
			return nil, scanner.error(ErrorUnexpectedCharacter, start, "invalid UTF-8 byte")
		}

		var token Token
		var err error
		switch {
		case isIdentifierStart(r):
			token = scanner.scanWord(start)
		case isASCIIDigitRune(r):
			token = scanner.scanNumber(start)
		case r == '\'':
			token, err = scanner.scanQuoted(start, '\'', KindString, ErrorUnterminatedString)
		case r == '`':
			token, err = scanner.scanQuoted(start, '`', KindQuotedIdentifier, ErrorUnterminatedIdentifier)
		case r == '"':
			token, err = scanner.scanQuoted(start, '"', KindQuotedIdentifier, ErrorUnterminatedIdentifier)
		case r == ':':
			token, err = scanner.scanNamedParameter(start)
		case r == '?':
			scanner.advanceRune()
			token = scanner.token(start, KindParameter, "?")
		case r == '(':
			token = scanner.single(start, KindLeftParen)
		case r == ')':
			token = scanner.single(start, KindRightParen)
		case r == ',':
			token = scanner.single(start, KindComma)
		case r == '.':
			token = scanner.single(start, KindDot)
		case r == ';':
			token = scanner.single(start, KindSemicolon)
		case r == '*':
			token = scanner.single(start, KindStar)
		case strings.ContainsRune("=<>!+-/%", r):
			token = scanner.scanOperator(start)
		default:
			scanner.advanceRune()
			return nil, scanner.error(ErrorUnexpectedCharacter, start, "unexpected character")
		}
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, token)
	}
}

func (scanner *scanner) skipIgnored() error {
	for !scanner.atEnd() {
		r, _ := scanner.peekRune()
		switch {
		case unicode.IsSpace(r):
			scanner.advanceRune()
		case scanner.hasPrefix("--"):
			scanner.advanceRune()
			scanner.advanceRune()
			for !scanner.atEnd() {
				r, _ = scanner.peekRune()
				if r == '\n' {
					break
				}
				scanner.advanceRune()
			}
		case scanner.hasPrefix("/*"):
			start := scanner.position()
			scanner.advanceRune()
			scanner.advanceRune()
			for !scanner.atEnd() && !scanner.hasPrefix("*/") {
				scanner.advanceRune()
			}
			if scanner.atEnd() {
				return scanner.error(ErrorUnterminatedComment, start, "unterminated block comment")
			}
			scanner.advanceRune()
			scanner.advanceRune()
		default:
			return nil
		}
	}
	return nil
}

func (scanner *scanner) scanWord(start Position) Token {
	for !scanner.atEnd() {
		r, size := scanner.peekRune()
		if (r == utf8.RuneError && size == 1) || !isIdentifierContinue(r) {
			break
		}
		scanner.advanceRune()
	}
	lexeme := scanner.source[start.Offset:scanner.offset]
	return scanner.token(start, KindWord, lexeme)
}

func (scanner *scanner) scanNumber(start Position) Token {
	for scanner.peekASCIIByteIsDigit() {
		scanner.advanceRune()
	}
	if scanner.hasPrefix(".") && scanner.peekASCIIByteAtIsDigit(1) {
		scanner.advanceRune()
		for scanner.peekASCIIByteIsDigit() {
			scanner.advanceRune()
		}
	}
	if scanner.hasPrefix("e") || scanner.hasPrefix("E") {
		exponentOffset := scanner.offset + 1
		if exponentOffset < len(scanner.source) && (scanner.source[exponentOffset] == '+' || scanner.source[exponentOffset] == '-') {
			exponentOffset++
		}
		if exponentOffset < len(scanner.source) && isASCIIDigit(scanner.source[exponentOffset]) {
			scanner.advanceRune()
			if scanner.hasPrefix("+") || scanner.hasPrefix("-") {
				scanner.advanceRune()
			}
			for scanner.peekASCIIByteIsDigit() {
				scanner.advanceRune()
			}
		}
	}
	lexeme := scanner.source[start.Offset:scanner.offset]
	return scanner.token(start, KindNumber, lexeme)
}

func (scanner *scanner) scanQuoted(start Position, delimiter rune, kind Kind, unterminated ErrorCode) (Token, error) {
	scanner.advanceRune()
	var value strings.Builder
	for !scanner.atEnd() {
		valuePosition := scanner.position()
		r, size := scanner.peekRune()
		if r == utf8.RuneError && size == 1 {
			scanner.advanceRune()
			return Token{}, scanner.error(ErrorUnexpectedCharacter, valuePosition, "invalid UTF-8 byte in quoted value")
		}
		scanner.advanceRune()
		if r != delimiter {
			value.WriteRune(r)
			continue
		}
		if next, _ := scanner.peekRune(); !scanner.atEnd() && next == delimiter {
			scanner.advanceRune()
			value.WriteRune(delimiter)
			continue
		}
		return scanner.token(start, kind, value.String()), nil
	}
	return Token{}, scanner.error(unterminated, start, "unterminated quoted value")
}

func (scanner *scanner) scanNamedParameter(start Position) (Token, error) {
	scanner.advanceRune()
	if scanner.atEnd() {
		return Token{}, scanner.error(ErrorInvalidParameter, start, "named parameter requires an identifier")
	}
	r, size := scanner.peekRune()
	if (r == utf8.RuneError && size == 1) || !isIdentifierStart(r) {
		return Token{}, scanner.error(ErrorInvalidParameter, start, "named parameter requires an identifier")
	}
	valueStart := scanner.offset
	for !scanner.atEnd() {
		r, size = scanner.peekRune()
		if (r == utf8.RuneError && size == 1) || !isIdentifierContinue(r) {
			break
		}
		scanner.advanceRune()
	}
	return scanner.token(start, KindParameter, scanner.source[valueStart:scanner.offset]), nil
}

func (scanner *scanner) scanOperator(start Position) Token {
	first, _ := scanner.peekRune()
	scanner.advanceRune()
	if !scanner.atEnd() {
		second, _ := scanner.peekRune()
		if (first == '!' && second == '=') || (first == '<' && (second == '>' || second == '=')) || (first == '>' && second == '=') {
			scanner.advanceRune()
		}
	}
	return scanner.token(start, KindOperator, "")
}

func (scanner *scanner) single(start Position, kind Kind) Token {
	scanner.advanceRune()
	return scanner.token(start, kind, "")
}

func (scanner *scanner) token(start Position, kind Kind, value string) Token {
	return Token{
		Kind:   kind,
		Lexeme: scanner.source[start.Offset:scanner.offset],
		Value:  value,
		Span:   Span{Start: start, End: scanner.position()},
	}
}

func (scanner *scanner) error(code ErrorCode, start Position, message string) *Error {
	return &Error{Code: code, Span: Span{Start: start, End: scanner.position()}, Message: message}
}

func (scanner *scanner) position() Position {
	return Position{Offset: scanner.offset, Line: scanner.line, Column: scanner.column}
}

func (scanner *scanner) atEnd() bool {
	return scanner.offset >= len(scanner.source)
}

func (scanner *scanner) hasPrefix(prefix string) bool {
	return strings.HasPrefix(scanner.source[scanner.offset:], prefix)
}

func (scanner *scanner) peekRune() (rune, int) {
	if scanner.atEnd() {
		return 0, 0
	}
	return utf8.DecodeRuneInString(scanner.source[scanner.offset:])
}

func (scanner *scanner) advanceRune() (rune, int) {
	r, size := scanner.peekRune()
	if size == 0 {
		return 0, 0
	}
	scanner.offset += size
	if r == '\n' {
		scanner.line++
		scanner.column = 1
	} else {
		scanner.column++
	}
	return r, size
}

func (scanner *scanner) peekASCIIByteIsDigit() bool {
	return scanner.offset < len(scanner.source) && isASCIIDigit(scanner.source[scanner.offset])
}

func (scanner *scanner) peekASCIIByteAtIsDigit(relative int) bool {
	offset := scanner.offset + relative
	return offset < len(scanner.source) && isASCIIDigit(scanner.source[offset])
}

func isASCIIDigit(value byte) bool {
	return value >= '0' && value <= '9'
}

func isASCIIDigitRune(value rune) bool {
	return value >= '0' && value <= '9'
}

func isIdentifierStart(r rune) bool {
	return r == '_' || unicode.IsLetter(r)
}

func isIdentifierContinue(r rune) bool {
	return isIdentifierStart(r) || unicode.IsDigit(r) || unicode.IsMark(r)
}
