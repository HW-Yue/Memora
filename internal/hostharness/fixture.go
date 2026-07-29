package hostharness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"

	"github.com/HW-Yue/Memora/internal/msql/parser"
	"github.com/HW-Yue/Memora/internal/result"
)

type Error struct {
	Code    result.Code
	Message string
}

func (err *Error) Error() string {
	return "scripted host harness: " + err.Message
}

func (err *Error) StableCode() string {
	return string(err.Code)
}

func LoadFile(path string) (Fixture, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return Fixture{}, harnessError(result.CodeInternal, "read fixture: %v", err)
	}
	var fixture Fixture
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(&fixture); err != nil {
		return Fixture{}, harnessError(result.CodeValidation, "decode fixture: %v", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("unexpected trailing JSON value")
		}
		return Fixture{}, harnessError(result.CodeValidation, "decode fixture: %v", err)
	}
	if err := fixture.Validate(); err != nil {
		return Fixture{}, err
	}
	return fixture, nil
}

func (fixture Fixture) Validate() error {
	if fixture.Version != Version {
		return harnessError(result.CodeValidation, "version = %q, want %q", fixture.Version, Version)
	}
	if strings.TrimSpace(fixture.Name) == "" {
		return harnessError(result.CodeValidation, "fixture name is empty")
	}
	var calls []Call
	seen := make(map[string]bool)
	for turnIndex, turn := range fixture.Turns {
		if strings.TrimSpace(turn.User) == "" {
			return harnessError(result.CodeValidation, "turn %d user message is empty", turnIndex)
		}
		if strings.TrimSpace(turn.Reply) == "" {
			return harnessError(result.CodeValidation, "turn %d reply is empty", turnIndex)
		}
		for actionIndex, action := range turn.Actions {
			if err := validateAction(action, false); err != nil {
				return harnessError(
					result.CodeValidation,
					"turn %d action %d: %v",
					turnIndex,
					actionIndex,
					err,
				)
			}
			if seen[action.Call.ID] {
				return harnessError(result.CodeValidation, "call id %q is duplicated", action.Call.ID)
			}
			seen[action.Call.ID] = true
			calls = append(calls, action.Call)
		}
	}
	if len(calls) != len(fixture.ExpectedToolSequence) {
		return harnessError(
			result.CodeValidation,
			"expected tool sequence has %d calls, script has %d",
			len(fixture.ExpectedToolSequence),
			len(calls),
		)
	}
	for index := range calls {
		got := calls[index]
		want := fixture.ExpectedToolSequence[index]
		if !reflect.DeepEqual(got, want) {
			return harnessError(
				result.CodeValidation,
				"expected tool sequence drift at %d: got %#v, want %#v",
				index,
				got,
				want,
			)
		}
	}
	for index, assertion := range fixture.FinalAssertions {
		if err := validateAction(assertion, true); err != nil {
			return harnessError(result.CodeValidation, "final assertion %d: %v", index, err)
		}
	}
	return nil
}

func validateAction(action Action, final bool) error {
	if action.Call.ID == "" {
		return fmt.Errorf("call id is empty")
	}
	if action.Call.Command != "query" && action.Call.Command != "exec" {
		return fmt.Errorf("command %q is not query or exec", action.Call.Command)
	}
	batch, err := parser.ParseBatch(action.Call.MSQL)
	if err != nil {
		return fmt.Errorf("MSQL is invalid: %w", err)
	}
	if action.Call.Command == "query" {
		for _, statement := range batch.Statements {
			if !readOnlyKind(statement.Kind) {
				return fmt.Errorf("query call contains mutation %q", statement.Kind)
			}
		}
	}
	if final && action.Inject != nil {
		return fmt.Errorf("final assertion cannot inject an outcome")
	}
	if action.Inject != nil {
		if !result.IsRegisteredCode(action.Inject.Code) || action.Inject.Message == "" {
			return fmt.Errorf("injected error code or message is invalid")
		}
	}
	return nil
}

func readOnlyKind(kind string) bool {
	switch kind {
	case "SHOW", "DESCRIBE", "SELECT", "MATCH", "OPEN_ROUTE":
		return true
	default:
		return false
	}
}

func harnessError(code result.Code, format string, arguments ...any) error {
	return &Error{Code: code, Message: fmt.Sprintf(format, arguments...)}
}
