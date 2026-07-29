package row

import (
	"fmt"

	"github.com/HW-Yue/Memora/internal/result"
)

type Error struct {
	Code     result.Code
	Object   string
	Expected uint64
	Actual   uint64
	Message  string
}

func (err *Error) Error() string {
	switch err.Code {
	case result.CodeRevisionConflict:
		return fmt.Sprintf("%s revision is %d; expected %d", err.Object, err.Actual, err.Expected)
	default:
		return err.Message
	}
}

func (err *Error) StableCode() string {
	return string(err.Code)
}

func rowError(code result.Code, message string) error {
	return &Error{Code: code, Message: message}
}

func revisionError(object string, expected, actual uint64) error {
	return &Error{Code: result.CodeRevisionConflict, Object: object, Expected: expected, Actual: actual}
}
