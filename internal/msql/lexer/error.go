package lexer

import "fmt"

type ErrorCode string

const (
	ErrorUnexpectedCharacter    ErrorCode = "unexpected_character"
	ErrorUnterminatedString     ErrorCode = "unterminated_string"
	ErrorUnterminatedComment    ErrorCode = "unterminated_comment"
	ErrorUnterminatedIdentifier ErrorCode = "unterminated_identifier"
	ErrorInvalidParameter       ErrorCode = "invalid_parameter"
)

type Error struct {
	Code    ErrorCode
	Span    Span
	Message string
}

func (err *Error) Error() string {
	return fmt.Sprintf("MSQL lexer %s at %d:%d: %s", err.Code, err.Span.Start.Line, err.Span.Start.Column, err.Message)
}
