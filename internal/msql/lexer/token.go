package lexer

import "strings"

type Kind uint8

const (
	KindEOF Kind = iota
	KindWord
	KindQuotedIdentifier
	KindString
	KindNumber
	KindParameter
	KindLeftParen
	KindRightParen
	KindComma
	KindDot
	KindSemicolon
	KindStar
	KindOperator
)

type Position struct {
	Offset int
	Line   int
	Column int
}

type Span struct {
	Start Position
	End   Position
}

type Token struct {
	Kind   Kind
	Lexeme string
	Value  string
	Span   Span
}

func (token Token) IsKeyword(keyword string) bool {
	if token.Kind != KindWord || !strings.EqualFold(token.Value, keyword) {
		return false
	}
	_, ok := keywords[strings.ToUpper(keyword)]
	return ok
}

var keywords = map[string]struct{}{
	"ADD": {}, "AGAINST": {}, "ALTER": {}, "AND": {}, "ANTI": {}, "AS": {}, "AT": {}, "BEGIN": {},
	"BY": {}, "COMMIT": {}, "COMPACT": {}, "CREATE": {}, "DATABASE": {},
	"DATABASES": {}, "DELETE": {}, "DESCRIBE": {}, "DOCTOR": {}, "DROP": {},
	"EXPLAIN": {}, "EXPORT": {}, "FALSE": {}, "FROM": {}, "HISTORY": {}, "INSERT": {},
	"INSTALL": {}, "INSTANCE": {}, "INTO": {}, "LIMIT": {}, "MATCH": {},
	"NOT": {}, "NULL": {}, "OF": {}, "ON": {}, "OPEN": {}, "OR": {},
	"ORDER": {}, "PACK": {}, "PACKAGE": {}, "PURGE": {}, "PURPOSE": {}, "READ": {},
	"REINDEX": {}, "RENAME": {}, "ROLLBACK": {}, "ROUTE": {}, "ROUTES": {}, "ROW": {},
	"SCOPE": {}, "SELECT": {}, "SEMANTICS": {}, "SET": {}, "SHOW": {}, "START": {},
	"COLUMN": {}, "COLUMNS": {}, "TABLE": {}, "TABLES": {}, "TO": {},
	"TRANSACTION": {}, "TRUE": {}, "UPDATE": {}, "VALUES": {}, "WHERE": {},
}
