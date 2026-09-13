package router

import "github.com/HW-Yue/Memora/internal/result"

type Error struct {
	Code    result.Code
	Message string
}

func (err *Error) Error() string      { return err.Message }
func (err *Error) StableCode() string { return string(err.Code) }

func routerError(code result.Code, message string) error {
	return &Error{Code: code, Message: message}
}
