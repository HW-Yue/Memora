package parser

import (
	"fmt"

	"github.com/HW-Yue/Memora/internal/msql/lexer"
)

type ErrorCode string

const (
	ErrorUnexpectedToken      ErrorCode = "unexpected_token"
	ErrorUnexpectedEOF        ErrorCode = "unexpected_eof"
	ErrorUnsupportedStatement ErrorCode = "unsupported_statement"
)

type Error struct {
	Code     ErrorCode
	Span     lexer.Span
	Expected string
	Found    string
}

func (err *Error) Error() string {
	if err.Expected == "" {
		return fmt.Sprintf("MSQL parser %s at %d:%d: found %s", err.Code, err.Span.Start.Line, err.Span.Start.Column, err.Found)
	}
	return fmt.Sprintf("MSQL parser %s at %d:%d: expected %s, found %s", err.Code, err.Span.Start.Line, err.Span.Start.Column, err.Expected, err.Found)
}
