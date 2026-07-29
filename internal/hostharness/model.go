package hostharness

import (
	"context"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
)

const Version = "memora.host-transcript/v1"

type Fixture struct {
	Version              string   `json:"version"`
	Name                 string   `json:"name"`
	Turns                []Turn   `json:"turns"`
	ExpectedToolSequence []Call   `json:"expected_tool_sequence"`
	FinalAssertions      []Action `json:"final_assertions"`
}

type Turn struct {
	User        string           `json:"user"`
	Actions     []Action         `json:"actions"`
	Reply       string           `json:"reply"`
	ReplyExpect ReplyExpectation `json:"reply_expect"`
}

type Action struct {
	Call   Call        `json:"call"`
	Inject *Injection  `json:"inject,omitempty"`
	Expect Expectation `json:"expect"`
}

type Call struct {
	ID      string                  `json:"id"`
	Command string                  `json:"command"`
	MSQL    string                  `json:"msql"`
	Input   executor.StatementInput `json:"input,omitempty"`
}

type Injection struct {
	Code      result.Code `json:"code"`
	Message   string      `json:"message"`
	Retryable bool        `json:"retryable"`
}

type Expectation struct {
	OK            *bool          `json:"ok,omitempty"`
	Code          result.Code    `json:"code,omitempty"`
	Status        result.Status  `json:"status,omitempty"`
	Rows          *int           `json:"rows,omitempty"`
	Revision      *uint64        `json:"revision,omitempty"`
	RowContains   map[string]any `json:"row_contains,omitempty"`
	ErrorContains string         `json:"error_contains,omitempty"`
}

type ReplyExpectation struct {
	Contains      []string `json:"contains,omitempty"`
	Excludes      []string `json:"excludes,omitempty"`
	MaxCharacters int      `json:"max_characters,omitempty"`
}

type Response struct {
	Envelope *result.Envelope
}

type Tool interface {
	Invoke(context.Context, Call) (Response, error)
}

type ToolFunc func(context.Context, Call) (Response, error)

func (function ToolFunc) Invoke(ctx context.Context, call Call) (Response, error) {
	return function(ctx, call)
}

type Report struct {
	Calls           []Call
	Replies         []string
	Injected        int
	FinalAssertions int
}
