package answerevaluation

import "errors"

var (
	ErrInvalidReportOutput = errors.New("answer evaluation report output is invalid")
	ErrReportOutputExists  = errors.New("answer evaluation report output already exists")
)

func PublishReport(string, Report) error { return ErrInvalidReportOutput }
