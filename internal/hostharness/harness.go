package hostharness

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/HW-Yue/Memora/internal/result"
)

type Harness struct {
	tool Tool
}

func New(tool Tool) *Harness {
	return &Harness{tool: tool}
}

func (harness *Harness) Replay(ctx context.Context, fixture Fixture) (Report, error) {
	if err := fixture.Validate(); err != nil {
		return Report{}, err
	}
	if harness == nil || harness.tool == nil {
		return Report{}, harnessError(result.CodeInternal, "tool is not configured")
	}
	report := Report{Calls: []Call{}, Replies: []string{}}
	for turnIndex, turn := range fixture.Turns {
		for actionIndex, action := range turn.Actions {
			if err := ctx.Err(); err != nil {
				return report, harnessError(result.CodeCancelled, "replay cancelled: %v", err)
			}
			report.Calls = append(report.Calls, action.Call)
			response, toolErr := Response{}, error(nil)
			if action.Inject != nil {
				envelope := result.FailedRequest(
					action.Call.ID,
					action.Inject.Code,
					action.Inject.Message,
					action.Inject.Retryable,
				)
				response.Envelope = &envelope
				report.Injected++
			} else {
				response, toolErr = harness.tool.Invoke(ctx, action.Call)
			}
			if err := checkExpectation(action.Expect, response, toolErr); err != nil {
				return report, harnessError(
					result.CodeValidation,
					"turn %d action %d (%s): %v",
					turnIndex,
					actionIndex,
					action.Call.ID,
					err,
				)
			}
		}
		if err := checkReply(turn.Reply, turn.ReplyExpect); err != nil {
			return report, harnessError(result.CodeValidation, "turn %d reply: %v", turnIndex, err)
		}
		report.Replies = append(report.Replies, turn.Reply)
	}
	for index, assertion := range fixture.FinalAssertions {
		response, toolErr := harness.tool.Invoke(ctx, assertion.Call)
		if err := checkExpectation(assertion.Expect, response, toolErr); err != nil {
			return report, harnessError(result.CodeValidation, "final assertion %d: %v", index, err)
		}
		report.FinalAssertions++
	}
	return report, nil
}

func checkExpectation(expectation Expectation, response Response, toolErr error) error {
	if expectation.ErrorContains != "" {
		if toolErr == nil || !strings.Contains(toolErr.Error(), expectation.ErrorContains) {
			return fmt.Errorf("tool error = %v, want containing %q", toolErr, expectation.ErrorContains)
		}
		return nil
	}
	if toolErr != nil {
		return fmt.Errorf("unexpected tool error: %w", toolErr)
	}
	if response.Envelope == nil {
		return fmt.Errorf("tool returned no result envelope")
	}
	envelope := *response.Envelope
	if err := envelope.Validate(); err != nil {
		return fmt.Errorf("invalid result envelope: %w", err)
	}
	if expectation.OK != nil && envelope.OK != *expectation.OK {
		return fmt.Errorf("ok = %t, want %t", envelope.OK, *expectation.OK)
	}
	if expectation.Code != "" {
		if got := envelopeCode(envelope); got != expectation.Code {
			return fmt.Errorf("error code = %q, want %q", got, expectation.Code)
		}
	}
	if expectation.Status != "" {
		first, err := firstResult(envelope)
		if err != nil {
			return err
		}
		if first.Status != expectation.Status {
			return fmt.Errorf("status = %q, want %q", first.Status, expectation.Status)
		}
	}
	if expectation.Rows != nil {
		first, err := firstResult(envelope)
		if err != nil {
			return err
		}
		if len(first.Rows) != *expectation.Rows {
			return fmt.Errorf("row count = %d, want %d", len(first.Rows), *expectation.Rows)
		}
	}
	if expectation.Revision != nil {
		first, err := firstResult(envelope)
		if err != nil {
			return err
		}
		if first.Revision == nil || *first.Revision != *expectation.Revision {
			return fmt.Errorf("revision = %v, want %d (result = %#v)", first.Revision, *expectation.Revision, first)
		}
	}
	if len(expectation.RowContains) > 0 {
		first, err := firstResult(envelope)
		if err != nil {
			return err
		}
		if len(first.Rows) == 0 {
			return fmt.Errorf("result has no row for row_contains")
		}
		for name, want := range expectation.RowContains {
			got, exists := first.Rows[0][name]
			if !exists || !reflect.DeepEqual(got, want) {
				return fmt.Errorf("first row %q = %#v, want %#v", name, got, want)
			}
		}
	}
	return nil
}

func checkReply(reply string, expectation ReplyExpectation) error {
	for _, fragment := range expectation.Contains {
		if !strings.Contains(reply, fragment) {
			return fmt.Errorf("does not contain %q", fragment)
		}
	}
	for _, fragment := range expectation.Excludes {
		if strings.Contains(reply, fragment) {
			return fmt.Errorf("contains forbidden fragment %q", fragment)
		}
	}
	if expectation.MaxCharacters > 0 && utf8.RuneCountInString(reply) > expectation.MaxCharacters {
		return fmt.Errorf(
			"has %d characters, maximum is %d",
			utf8.RuneCountInString(reply),
			expectation.MaxCharacters,
		)
	}
	return nil
}

func firstResult(envelope result.Envelope) (result.StatementResult, error) {
	if len(envelope.Results) == 0 {
		return result.StatementResult{}, fmt.Errorf("result envelope has no statement result")
	}
	return envelope.Results[0], nil
}

func envelopeCode(envelope result.Envelope) result.Code {
	if envelope.Error != nil {
		return envelope.Error.Code
	}
	for _, statement := range envelope.Results {
		if statement.Error != nil {
			return statement.Error.Code
		}
	}
	return ""
}
