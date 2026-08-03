package answerrelease

import "errors"

var (
	ErrInvalidOutput = errors.New("query release output is invalid")
	ErrOutputExists  = errors.New("query release output already exists")
)

func PublishReport(string, Report) error { return ErrInvalidOutput }
