package parser

import (
	"strings"

	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/msql/lexer"
)

// BatchItem preserves the position of both valid and invalid statements so an
// executor can return one ordered result per statement.
type BatchItem struct {
	Index     int
	Kind      string
	Span      lexer.Span
	Statement *ast.Statement
	Issue     *Error
}

// ParseBatchItems recovers only at semicolon tokens produced by the Lexer.
// Semicolons inside strings, quoted identifiers, and comments are never
// recovery boundaries.
func ParseBatchItems(source string) ([]BatchItem, error) {
	tokens, err := lexer.Lex(source)
	if err != nil {
		return nil, err
	}
	reader := parser{tokens: tokens}
	items := make([]BatchItem, 0)
	for {
		for reader.matchKind(lexer.KindSemicolon) {
		}
		if reader.checkKind(lexer.KindEOF) {
			break
		}

		index := len(items)
		startToken := reader.peek()
		kind := recoveryKind(startToken)
		statement, parseErr := reader.parseStatement()
		if parseErr != nil {
			issue := indexedIssue(parseErr, index)
			items = append(items, BatchItem{
				Index: index, Kind: kind,
				Span:  reader.recoverStatement(startToken.Span.Start, issue.Span),
				Issue: issue,
			})
			continue
		}
		if !reader.checkKind(lexer.KindEOF) && !reader.matchKind(lexer.KindSemicolon) {
			issue := indexedIssue(reader.unexpected("; or end of request"), index)
			items = append(items, BatchItem{
				Index: index, Kind: statement.Kind,
				Span:  reader.recoverStatement(startToken.Span.Start, issue.Span),
				Issue: issue,
			})
			continue
		}
		copy := statement
		items = append(items, BatchItem{
			Index: index, Kind: statement.Kind, Span: statement.Span, Statement: &copy,
		})
	}
	if len(items) == 0 {
		token := reader.peek()
		return nil, &Error{
			Code: ErrorEmptyBatch, Span: token.Span,
			Expected: "at least one statement", Found: "EOF", StatementIndex: -1,
		}
	}
	return items, nil
}

func (parser *parser) recoverStatement(start lexer.Position, issue lexer.Span) lexer.Span {
	end := issue.End
	if parser.current > 0 && parser.previous().Span.End.Offset > end.Offset {
		end = parser.previous().Span.End
	}
	for !parser.checkKind(lexer.KindEOF) && !parser.checkKind(lexer.KindSemicolon) {
		token := parser.advance()
		if token.Kind == lexer.KindParameter {
			parser.parameterOrdinal++
		}
		end = token.Span.End
	}
	if parser.checkKind(lexer.KindSemicolon) {
		parser.advance()
	}
	return lexer.Span{Start: start, End: end}
}

func indexedIssue(err error, index int) *Error {
	issue, ok := withStatementIndex(err, index).(*Error)
	if ok {
		return issue
	}
	token := lexer.Token{Kind: lexer.KindEOF}
	return &Error{
		Code: ErrorUnexpectedToken, Span: token.Span,
		Expected: "valid statement", Found: "parser error", StatementIndex: index,
	}
}

func recoveryKind(token lexer.Token) string {
	if token.Kind != lexer.KindWord {
		return "PARSE"
	}
	kind := strings.ToUpper(token.Value)
	if kind == "START" {
		return "BEGIN"
	}
	return kind
}
