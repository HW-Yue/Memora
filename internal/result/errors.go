package result

type Code string

const (
	CodeInvalidRequest     Code = "invalid_request"
	CodeParseError         Code = "parse_error"
	CodeUnsupported        Code = "unsupported_statement"
	CodeValidation         Code = "validation_error"
	CodeNotFound           Code = "not_found"
	CodeAlreadyExists      Code = "already_exists"
	CodeRevisionConflict   Code = "revision_conflict"
	CodeConstraint         Code = "constraint_violation"
	CodeValueTooLong       Code = "value_too_long"
	CodeTransactionAborted Code = "transaction_aborted"
	CodeInvalidTransaction Code = "invalid_transaction_state"
	CodeCancelled          Code = "cancelled"
	CodeDeadlineExceeded   Code = "deadline_exceeded"
	CodeOutputTruncated    Code = "output_truncated"
	CodeInternal           Code = "internal_error"
)

var registeredCodes = map[Code]struct{}{
	CodeInvalidRequest: {}, CodeParseError: {}, CodeUnsupported: {}, CodeValidation: {},
	CodeNotFound: {}, CodeAlreadyExists: {}, CodeRevisionConflict: {}, CodeConstraint: {},
	CodeValueTooLong: {}, CodeTransactionAborted: {}, CodeInvalidTransaction: {},
	CodeCancelled: {}, CodeDeadlineExceeded: {}, CodeOutputTruncated: {}, CodeInternal: {},
}

func IsRegisteredCode(code Code) bool {
	_, exists := registeredCodes[code]
	return exists
}

type ResultError struct {
	Code           Code           `json:"code"`
	Message        string         `json:"message"`
	Retryable      bool           `json:"retryable"`
	StatementIndex *int           `json:"statement_index,omitempty"`
	Details        map[string]any `json:"details,omitempty"`
}

type Notice struct {
	Code    Code           `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}
