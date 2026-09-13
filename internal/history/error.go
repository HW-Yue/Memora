package history

import "github.com/HW-Yue/Memora/internal/result"

type Error struct {
	Code    result.Code
	Message string
}

func (err *Error) Error() string      { return err.Message }
func (err *Error) StableCode() string { return string(err.Code) }
