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
	"ADD": {}, "ALL": {}, "ALTER": {}, "AND": {}, "ANTI": {}, "ARCHIVE": {}, "ARCHIVED": {},
	"AS": {}, "AT": {}, "BEGIN": {},
	"ATLAS": {}, "BY": {}, "BYTES": {}, "CANDIDATES": {}, "CATALOG": {}, "COMMIT": {}, "COMPACT": {}, "CREATE": {}, "DATABASE": {},
	"DATABASES": {}, "DELETE": {}, "DESCRIBE": {}, "DOCTOR": {}, "DROP": {},
	"EXPLAIN": {}, "FALSE": {}, "FOR": {}, "FROM": {}, "HISTORY": {}, "INSERT": {},
	"INSTANCE": {}, "INTO": {}, "LEXICAL": {}, "LIMIT": {},
	"NOT": {}, "NULL": {}, "OF": {}, "ON": {}, "OPEN": {}, "OR": {},
	"ONLY": {}, "ORDER": {}, "PURGE": {}, "PURPOSE": {}, "READ": {},
	"RENAME": {}, "RESTORE": {}, "ROLLBACK": {}, "ROUTE": {}, "ROUTES": {}, "ROW": {},
	"SCOPE": {}, "SELECT": {}, "SEMANTICS": {}, "SET": {}, "SHOW": {}, "SPACE": {}, "START": {},
	"COLUMN": {}, "COLUMNS": {}, "TABLE": {}, "TABLES": {}, "TO": {},
	"TRANSACTION": {}, "TRUE": {}, "UNARCHIVE": {}, "UPDATE": {}, "USING": {}, "VALUES": {},
	"VECTOR": {}, "WHERE": {}, "INCLUDING": {},
	"RELATE": {}, "UNRELATE": {},
}
