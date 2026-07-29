package catalog

import "fmt"

type Code string

const (
	CodeValidation    Code = "validation_error"
	CodeNotFound      Code = "not_found"
	CodeAlreadyExists Code = "already_exists"
)

type Error struct {
	Code   Code
	Object string
	Name   string
	Field  string
}

func (err *Error) Error() string {
	switch err.Code {
	case CodeValidation:
		return fmt.Sprintf("catalog %s %q requires %s", err.Object, err.Name, err.Field)
	case CodeNotFound:
		return fmt.Sprintf("catalog %s %q was not found", err.Object, err.Name)
	case CodeAlreadyExists:
		return fmt.Sprintf("catalog %s name or alias %q already exists", err.Object, err.Name)
	default:
		return fmt.Sprintf("catalog %s %q failed", err.Object, err.Name)
	}
}
