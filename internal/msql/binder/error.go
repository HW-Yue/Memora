package binder

import (
	"fmt"

	"github.com/HW-Yue/Memora/internal/result"
)

type Error struct {
	Code    result.Code
	Message string
}

func (err *Error) Error() string {
	return fmt.Sprintf("MSQL binder %s: %s", err.Code, err.Message)
}

func (err *Error) StableCode() string {
	return string(err.Code)
}

func bindError(code result.Code, message string) error {
	return &Error{Code: code, Message: message}
}
