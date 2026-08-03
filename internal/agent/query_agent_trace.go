package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	protocolmsql "github.com/HW-Yue/Memora/protocol/msql"
)

type queryTracedMSQL struct {
	executor *Runtime
	recorder *TraceRecorder
	clock    QueryClock
	turn     uint64
}

func (executor *queryTracedMSQL) ExecuteMSQL(
	ctx context.Context,
	request protocolmsql.Request,
) (protocolmsql.Envelope, error) {
	input, encodeErr := json.Marshal(request)
	if encodeErr != nil {
		return protocolmsql.Envelope{}, fmt.Errorf("%w: MSQL request could not be encoded", ErrInvalidQueryToolCall)
	}
	started := queryNow(executor.clock)
	envelope, err := executor.executor.ExecuteMSQL(ctx, request)
	finished := queryNow(executor.clock)
	var output []byte
	if err == nil {
		output, encodeErr = json.Marshal(envelope)
		if encodeErr != nil {
			err = fmt.Errorf("%w: MSQL result could not be encoded", ErrInvalidQueryToolCall)
		}
	}
	status, errorCode := queryTraceStatus(err, "MSQL_EXECUTION_FAILED")
	_, traceErr := executor.recorder.Append(TraceDraft{
		Turn: executor.turn, Kind: TraceKindMSQL, Operation: "msql.execute",
		StartedAt: started, FinishedAt: finished, Status: status, ErrorCode: errorCode,
		Input: DigestTracePayload(input), Output: DigestTracePayload(output),
	})
	if traceErr != nil {
		return protocolmsql.Envelope{}, fmt.Errorf("%w: %v", ErrQueryTrace, traceErr)
	}
	return envelope, err
}

func queryAppendBootstrap(
	recorder *TraceRecorder,
	input BootstrapRequest,
	output BootstrapFrame,
	started time.Time,
	finished time.Time,
	operationErr error,
) error {
	inputBytes, _ := json.Marshal(input)
	var outputBytes []byte
	if operationErr == nil {
		outputBytes, _ = json.Marshal(output)
	}
	status, errorCode := queryTraceStatus(operationErr, "BOOTSTRAP_FAILED")
	_, err := recorder.Append(TraceDraft{
		Turn: 1, Kind: TraceKindBootstrap, Operation: "bootstrap.assemble",
		StartedAt: started, FinishedAt: finished, Status: status, ErrorCode: errorCode,
		Input: DigestTracePayload(inputBytes), Output: DigestTracePayload(outputBytes),
	})
	if err != nil {
		return fmt.Errorf("%w: %v", ErrQueryTrace, err)
	}
	return nil
}

func queryAppendProvider(
	recorder *TraceRecorder,
	turn uint64,
	input ProviderRequest,
	output ProviderResponse,
	started time.Time,
	finished time.Time,
	operationErr error,
) error {
	inputBytes, _ := json.Marshal(input)
	var outputBytes []byte
	usage := ProviderUsage{}
	if operationErr == nil {
		outputBytes, _ = json.Marshal(output)
		usage = output.Usage
	}
	status, errorCode := queryTraceStatus(operationErr, "PROVIDER_FAILED")
	_, err := recorder.Append(TraceDraft{
		Turn: turn, Kind: TraceKindProvider, Operation: "provider.complete",
		StartedAt: started, FinishedAt: finished, Status: status, ErrorCode: errorCode,
		Input: DigestTracePayload(inputBytes), Output: DigestTracePayload(outputBytes),
		Model: input.Model, Usage: &usage,
	})
	if err != nil {
		return fmt.Errorf("%w: %v", ErrQueryTrace, err)
	}
	return nil
}

func queryAppendTool(
	recorder *TraceRecorder,
	turn uint64,
	input []byte,
	output []byte,
	started time.Time,
	finished time.Time,
	operationErr error,
) error {
	status, errorCode := queryTraceStatus(operationErr, queryToolErrorCode(operationErr))
	_, err := recorder.Append(TraceDraft{
		Turn: turn, Kind: TraceKindTool, Operation: "tool.execute_msql",
		StartedAt: started, FinishedAt: finished, Status: status, ErrorCode: errorCode,
		Input: DigestTracePayload(input), Output: DigestTracePayload(output),
	})
	if err != nil {
		return fmt.Errorf("%w: %v", ErrQueryTrace, err)
	}
	return nil
}

func queryAppendToolFailure(
	recorder *TraceRecorder,
	turn uint64,
	message ProviderMessage,
	clock QueryClock,
	errorCode string,
) error {
	input, _ := json.Marshal(message)
	started := queryNow(clock)
	finished := queryNow(clock)
	_, err := recorder.Append(TraceDraft{
		Turn: turn, Kind: TraceKindTool, Operation: "tool.execute_msql",
		StartedAt: started, FinishedAt: finished, Status: TraceStatusFailed, ErrorCode: errorCode,
		Input: DigestTracePayload(input), Output: DigestTracePayload(nil),
	})
	if err != nil {
		return fmt.Errorf("%w: %v", ErrQueryTrace, err)
	}
	return nil
}

func queryToolErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrQueryTransactionControl):
		return "QUERY_TRANSACTION_CONTROL"
	case errors.Is(err, ErrQueryBudget):
		return "QUERY_BUDGET"
	case errors.Is(err, ErrInvalidQueryToolCall):
		return "QUERY_TOOL_CALL_INVALID"
	default:
		return "QUERY_TOOL_FAILED"
	}
}

func queryTraceStatus(err error, fallback string) (TraceStatus, string) {
	if err == nil {
		return TraceStatusSucceeded, ""
	}
	if errors.Is(err, context.Canceled) {
		return TraceStatusCancelled, "CONTEXT_CANCELLED"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return TraceStatusCancelled, "CONTEXT_DEADLINE"
	}
	return TraceStatusFailed, fallback
}

func queryNow(clock QueryClock) time.Time {
	return clock.Now().UTC()
}
