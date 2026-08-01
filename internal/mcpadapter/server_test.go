package mcpadapter_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/mcpadapter"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/security"
)

func TestModernDiscoverListAndCallUseDaemonBoundary(t *testing.T) {
	t.Parallel()

	var gotDir, gotSource string
	var gotStatements []executor.StatementInput
	server := mcpadapter.New(mcpadapter.Config{
		DataDir: "/private/memora",
		Version: "0.1.0",
		Execute: func(_ context.Context, dataDir, source string, statements []executor.StatementInput) (result.Envelope, error) {
			gotDir, gotSource, gotStatements = dataDir, source, statements
			return result.NewEnvelope("daemon-request", result.NewStatement(0, source, "mcp")), nil
		},
	})
	authorization := security.Authorization{
		Version: security.AuthorizationVersion, Actor: "mcp:test",
		AuthorizedDatabases: []string{"work"}, DefaultLevel: security.LevelRead,
	}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":"d","method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`,
		`{"jsonrpc":"2.0","id":"l","method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`,
		mustRequest(t, "c", authorization),
	}, "\n") + "\n"
	var output bytes.Buffer
	if err := server.Serve(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	responses := decodeLines(t, output.String())
	if len(responses) != 3 || responses[0]["error"] != nil || responses[1]["error"] != nil || responses[2]["error"] != nil {
		t.Fatalf("responses = %#v", responses)
	}
	if gotDir != "/private/memora" || gotSource != "SHOW DATABASES LIMIT 8" || len(gotStatements) != 1 {
		t.Fatalf("execute = %q, %q, %#v", gotDir, gotSource, gotStatements)
	}
	callResult := responses[2]["result"].(map[string]any)
	if callResult["resultType"] != "complete" || callResult["isError"] != false || callResult["structuredContent"] == nil {
		t.Fatalf("call result = %#v", callResult)
	}
}

func TestLegacyInitializeAndToolList(t *testing.T) {
	t.Parallel()

	server := mcpadapter.New(mcpadapter.Config{DataDir: "/private/memora"})
	input := "" +
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}` + "\n" +
		`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}` + "\n"
	var output bytes.Buffer
	if err := server.Serve(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	responses := decodeLines(t, output.String())
	if len(responses) != 2 {
		t.Fatalf("responses = %#v", responses)
	}
	initialize := responses[0]["result"].(map[string]any)
	if initialize["protocolVersion"] != "2025-11-25" {
		t.Fatalf("initialize = %#v", initialize)
	}
}

func TestInvalidAuthorizationIsToolErrorWithoutDaemonCall(t *testing.T) {
	t.Parallel()

	called := false
	server := mcpadapter.New(mcpadapter.Config{
		DataDir: "/private/memora",
		Execute: func(context.Context, string, string, []executor.StatementInput) (result.Envelope, error) {
			called = true
			return result.Envelope{}, nil
		},
	})
	request := mustRequest(t, "invalid", security.Authorization{})
	var output bytes.Buffer
	if err := server.Serve(context.Background(), strings.NewReader(request+"\n"), &output); err != nil {
		t.Fatal(err)
	}
	response := decodeLines(t, output.String())[0]
	callResult := response["result"].(map[string]any)
	if called || callResult["isError"] != true {
		t.Fatalf("called = %t, result = %#v", called, callResult)
	}
}

func TestProtocolErrorsAreStable(t *testing.T) {
	t.Parallel()

	server := mcpadapter.New(mcpadapter.Config{DataDir: "/private/memora"})
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":"missing-meta","method":"server/discover","params":{}}`,
		`{"jsonrpc":"2.0","id":"wrong-version","method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"1900-01-01","io.modelcontextprotocol/clientCapabilities":{}}}}`,
		`{"jsonrpc":"2.0","id":"unknown","method":"unknown","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	if err := server.Serve(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	responses := decodeLines(t, output.String())
	want := []float64{-32602, -32022, -32601}
	for index, response := range responses {
		errorValue := response["error"].(map[string]any)
		if errorValue["code"] != want[index] {
			t.Fatalf("response %d = %#v", index, response)
		}
	}
}

func TestFailedEnvelopeIsStructuredToolError(t *testing.T) {
	t.Parallel()

	server := mcpadapter.New(mcpadapter.Config{
		DataDir: "/private/memora",
		Execute: func(context.Context, string, string, []executor.StatementInput) (result.Envelope, error) {
			return result.FailedRequest("daemon", result.CodePermissionDenied, "denied", false), nil
		},
	})
	authorization := security.Authorization{
		Version: security.AuthorizationVersion, Actor: "mcp:test",
		AuthorizedDatabases: []string{"work"}, DefaultLevel: security.LevelRead,
	}
	var output bytes.Buffer
	if err := server.Serve(context.Background(), strings.NewReader(mustRequest(t, "failed", authorization)+"\n"), &output); err != nil {
		t.Fatal(err)
	}
	callResult := decodeLines(t, output.String())[0]["result"].(map[string]any)
	if callResult["isError"] != true || callResult["structuredContent"] == nil {
		t.Fatalf("result = %#v", callResult)
	}
}

func TestLegacyToolsRequireInitializedNotification(t *testing.T) {
	t.Parallel()

	server := mcpadapter.New(mcpadapter.Config{DataDir: "/private/memora"})
	input := "" +
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}` + "\n"
	var output bytes.Buffer
	if err := server.Serve(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	responses := decodeLines(t, output.String())
	if len(responses) != 2 || responses[1]["error"] == nil {
		t.Fatalf("responses = %#v", responses)
	}
}

func TestOversizedLineReturnsParseErrorAndStops(t *testing.T) {
	t.Parallel()

	server := mcpadapter.New(mcpadapter.Config{DataDir: "/private/memora", MaxLineSize: 64})
	var output bytes.Buffer
	err := server.Serve(context.Background(), strings.NewReader(strings.Repeat("x", 128)+"\n"), &output)
	if !errors.Is(err, mcpadapter.ErrMessageTooLarge) {
		t.Fatalf("Serve() error = %v", err)
	}
	response := decodeLines(t, output.String())[0]
	if response["error"].(map[string]any)["code"] != float64(-32700) {
		t.Fatalf("response = %#v", response)
	}
}

func mustRequest(t *testing.T, id string, authorization security.Authorization) string {
	t.Helper()
	request := map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "tools/call",
		"params": map[string]any{
			"name": "memora_execute",
			"arguments": map[string]any{
				"source":     "SHOW DATABASES LIMIT 8",
				"statements": []any{map[string]any{"authorization": authorization}},
			},
			"_meta": map[string]any{
				"io.modelcontextprotocol/protocolVersion":    "2026-07-28",
				"io.modelcontextprotocol/clientCapabilities": map[string]any{},
			},
		},
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func decodeLines(t *testing.T, output string) []map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(output), "\n")
	responses := make([]map[string]any, len(lines))
	for index, line := range lines {
		if err := json.Unmarshal([]byte(line), &responses[index]); err != nil {
			t.Fatalf("decode line %d: %v: %q", index, err, line)
		}
	}
	return responses
}
