package parser

import (
	"strings"

	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/msql/lexer"
)

func (parser *parser) parseExpression(minimumPrecedence int) (ast.Expression, error) {
	left, err := parser.parseUnary()
	if err != nil {
		return ast.Expression{}, err
	}
	for {
		operator, precedence, ok := binaryOperator(parser.peek())
		if !ok || precedence < minimumPrecedence {
			break
		}
		parser.advance()
		right, err := parser.parseExpression(precedence + 1)
		if err != nil {
			return ast.Expression{}, err
		}
		leftCopy := left
		rightCopy := right
		left = ast.Expression{
			Kind: "binary", Operator: operator, Left: &leftCopy, Right: &rightCopy,
			Span: lexer.Span{Start: left.Span.Start, End: right.Span.End},
		}
	}
	return left, nil
}

func (parser *parser) parseUnary() (ast.Expression, error) {
	token := parser.peek()
	operator := ""
	if token.Kind == lexer.KindOperator && (token.Lexeme == "+" || token.Lexeme == "-") {
		operator = token.Lexeme
	} else if parser.checkWord("NOT") {
		operator = "NOT"
	}
	if operator == "" {
		return parser.parsePrimary()
	}
	parser.advance()
	operand, err := parser.parseUnary()
	if err != nil {
		return ast.Expression{}, err
	}
	return ast.Expression{
		Kind: "unary", Operator: operator, Operand: &operand,
		Span: lexer.Span{Start: token.Span.Start, End: operand.Span.End},
	}, nil
}

func (parser *parser) parsePrimary() (ast.Expression, error) {
	token := parser.peek()
	switch token.Kind {
	case lexer.KindNumber:
		parser.advance()
		return ast.Expression{Kind: "literal", Literal: &ast.Literal{Kind: "number", Value: token.Value}, Span: token.Span}, nil
	case lexer.KindString:
		parser.advance()
		return ast.Expression{Kind: "literal", Literal: &ast.Literal{Kind: "string", Value: token.Value}, Span: token.Span}, nil
	case lexer.KindParameter:
		parser.advance()
		parser.parameterOrdinal++
		parameter := ast.Parameter{Style: "named", Name: token.Value, Ordinal: parser.parameterOrdinal}
		if token.Lexeme == "?" {
			parameter.Style = "positional"
			parameter.Name = ""
		}
		return ast.Expression{Kind: "parameter", Parameter: &parameter, Span: token.Span}, nil
	case lexer.KindLeftParen:
		start := token.Span.Start
		parser.advance()
		expression, err := parser.parseExpression(1)
		if err != nil {
			return ast.Expression{}, err
		}
		closing, err := parser.expectKind(lexer.KindRightParen, ")")
		if err != nil {
			return ast.Expression{}, err
		}
		expression.Span = lexer.Span{Start: start, End: closing.Span.End}
		return expression, nil
	case lexer.KindWord:
		switch strings.ToUpper(token.Value) {
		case "NULL":
			parser.advance()
			return ast.Expression{Kind: "literal", Literal: &ast.Literal{Kind: "null"}, Span: token.Span}, nil
		case "TRUE", "FALSE":
			parser.advance()
			return ast.Expression{Kind: "literal", Literal: &ast.Literal{Kind: "boolean", Value: strings.ToLower(token.Value)}, Span: token.Span}, nil
		}
	}
	if token.Kind == lexer.KindWord || token.Kind == lexer.KindQuotedIdentifier {
		name, err := parser.parseName()
		if err != nil {
			return ast.Expression{}, err
		}
		return ast.Expression{Kind: "identifier", Name: &name, Span: name.Span}, nil
	}
	return ast.Expression{}, parser.unexpected("expression")
}

func binaryOperator(token lexer.Token) (string, int, bool) {
	if token.Kind == lexer.KindWord {
		switch strings.ToUpper(token.Value) {
		case "OR":
			return "OR", 1, true
		case "AND":
			return "AND", 2, true
		}
	}
	if token.Kind == lexer.KindStar {
		return "*", 5, true
	}
	if token.Kind != lexer.KindOperator {
		return "", 0, false
	}
	switch token.Lexeme {
	case "=", "!=", "<>", "<", "<=", ">", ">=":
		return token.Lexeme, 3, true
	case "+", "-":
		return token.Lexeme, 4, true
	case "/", "%":
		return token.Lexeme, 5, true
	default:
		return "", 0, false
	}
}
