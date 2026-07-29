package logical

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/HW-Yue/Memora/internal/result"
)

const DefaultTextCharacters = 1200

type Kind string

const (
	KindInteger    Kind = "INTEGER"
	KindBoolean    Kind = "BOOLEAN"
	KindTimestamp  Kind = "TIMESTAMP"
	KindText       Kind = "TEXT"
	KindRelationID Kind = "RELATION_ID"
)

type Definition struct {
	Kind          Kind
	MaxCharacters int
}

type Constraint struct {
	Name       string
	Definition Definition
	Nullable   bool
}

type Error struct {
	Code     result.Code
	Column   string
	Expected string
	Actual   int
	Limit    int
}

func (err *Error) Error() string {
	switch err.Code {
	case result.CodeValueTooLong:
		return fmt.Sprintf("column %q contains %d characters; limit is %d", err.Column, err.Actual, err.Limit)
	case result.CodeConstraint:
		return fmt.Sprintf("column %q requires %s", err.Column, err.Expected)
	default:
		return fmt.Sprintf("invalid logical type declaration %q", err.Expected)
	}
}

func (err *Error) StableCode() string {
	return string(err.Code)
}

func ParseDeclaration(source string) (Definition, error) {
	declaration := strings.ToUpper(strings.TrimSpace(source))
	switch declaration {
	case string(KindInteger):
		return Definition{Kind: KindInteger}, nil
	case string(KindBoolean):
		return Definition{Kind: KindBoolean}, nil
	case string(KindTimestamp):
		return Definition{Kind: KindTimestamp}, nil
	case string(KindRelationID):
		return Definition{Kind: KindRelationID}, nil
	case string(KindText):
		return Definition{Kind: KindText, MaxCharacters: DefaultTextCharacters}, nil
	}
	if !strings.HasPrefix(declaration, string(KindText)+"(") || !strings.HasSuffix(declaration, ")") {
		return Definition{}, invalidDeclaration(source)
	}
	argument := strings.TrimSuffix(strings.TrimPrefix(declaration, string(KindText)+"("), ")")
	if argument == "" || strings.Contains(argument, ",") {
		return Definition{}, invalidDeclaration(source)
	}
	limit, err := strconv.ParseInt(argument, 10, 64)
	maximumInt := int64(^uint(0) >> 1)
	if err != nil || limit <= 0 || limit > maximumInt {
		return Definition{}, invalidDeclaration(source)
	}
	return Definition{Kind: KindText, MaxCharacters: int(limit)}, nil
}

func Validate(constraint Constraint, value any) (any, error) {
	if err := validDefinition(constraint.Definition); err != nil {
		return nil, err
	}
	if value == nil {
		if constraint.Nullable {
			return nil, nil
		}
		return nil, constraintError(constraint, "a non-NULL value")
	}
	switch constraint.Definition.Kind {
	case KindInteger:
		integer, ok := integerValue(value)
		if !ok {
			return nil, constraintError(constraint, "INTEGER")
		}
		return integer, nil
	case KindBoolean:
		boolean, ok := value.(bool)
		if !ok {
			return nil, constraintError(constraint, "BOOLEAN")
		}
		return boolean, nil
	case KindTimestamp:
		return timestampValue(constraint, value)
	case KindText:
		text, ok := value.(string)
		if !ok || !utf8.ValidString(text) {
			return nil, constraintError(constraint, "valid UTF-8 TEXT")
		}
		length := utf8.RuneCountInString(text)
		if length > constraint.Definition.MaxCharacters {
			return nil, &Error{
				Code: result.CodeValueTooLong, Column: constraint.Name,
				Actual: length, Limit: constraint.Definition.MaxCharacters,
			}
		}
		return text, nil
	case KindRelationID:
		relationID, ok := value.(string)
		if !ok || !validRelationID(relationID) {
			return nil, constraintError(constraint, "RELATION_ID beginning with row_")
		}
		return relationID, nil
	default:
		return nil, invalidDeclaration(string(constraint.Definition.Kind))
	}
}

func validDefinition(definition Definition) error {
	switch definition.Kind {
	case KindText:
		if definition.MaxCharacters <= 0 {
			return invalidDeclaration(string(definition.Kind))
		}
	case KindInteger, KindBoolean, KindTimestamp, KindRelationID:
		if definition.MaxCharacters != 0 {
			return invalidDeclaration(string(definition.Kind))
		}
	default:
		return invalidDeclaration(string(definition.Kind))
	}
	return nil
}

func integerValue(value any) (int64, bool) {
	switch number := value.(type) {
	case int:
		return int64(number), true
	case int8:
		return int64(number), true
	case int16:
		return int64(number), true
	case int32:
		return int64(number), true
	case int64:
		return number, true
	case uint:
		if uint64(number) <= math.MaxInt64 {
			return int64(number), true
		}
	case uint8:
		return int64(number), true
	case uint16:
		return int64(number), true
	case uint32:
		return int64(number), true
	case uint64:
		if number <= math.MaxInt64 {
			return int64(number), true
		}
	case json.Number:
		if strings.ContainsAny(string(number), ".eE") {
			return 0, false
		}
		integer, err := strconv.ParseInt(string(number), 10, 64)
		return integer, err == nil
	}
	return 0, false
}

func timestampValue(constraint Constraint, value any) (time.Time, error) {
	switch timestamp := value.(type) {
	case time.Time:
		return timestamp.UTC(), nil
	case string:
		parsed, err := time.Parse(time.RFC3339Nano, timestamp)
		if err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, constraintError(constraint, "RFC3339 TIMESTAMP")
}

func validRelationID(value string) bool {
	if !strings.HasPrefix(value, "row_") || len(value) == len("row_") {
		return false
	}
	for _, character := range value[len("row_"):] {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func constraintError(constraint Constraint, expected string) error {
	return &Error{Code: result.CodeConstraint, Column: constraint.Name, Expected: expected}
}

func invalidDeclaration(source string) error {
	return &Error{Code: result.CodeValidation, Expected: source}
}
